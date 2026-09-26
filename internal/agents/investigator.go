package agents

// investigator.go: the LLM agent loop that forms root cause hypotheses.
// Two-round agentic loop, same pattern used by SWE-agent and OpenHands:
//
//  1. Round 1: LLM sees pod logs + repo file tree -> emits search queries.
//  2. Local regex search runs against the cloned repo (no API calls).
//  3. Round 2: LLM sees logs + tree + search results -> emits hypotheses +
//     files to read in full. The files are read and attached to the state
//     for the fix proposer.
//
// Falls back to runWithoutRepo when no repo annotation is present, the repo
// is not on the allowlist, or clone fails. Falls back to stack-trace parsing
// when the LLM returns zero files to read.

import (
	"context"
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repo"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

const investigatorRound1Prompt = `You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

You are given:
- An anomaly cluster describing what's wrong with a service
- Pod logs from the crashing container (may be empty if logs weren't available)
- The file tree of the service's GitHub repository
- The detected programming language (if recognizable from the error)

Your job is to decide what to SEARCH for in the source code to investigate the incident. You'll emit search queries (regex patterns) that will be run against the local clone of the repo.

Think about:
- Error messages or stack traces in the logs → search for the error type (e.g. "KeyError", "NullPointerException", "panic", "TypeError", "SIGSEGV")
- Variable names or config keys mentioned in errors → search for those exact strings
- Function or class names in stack traces → search for the function/class name as a bare identifier (the local search is language-agnostic regex — do NOT prefix with "def", "func", "function", or "class"; just use the bare name)
- Import errors → search for the module name

Respond with a JSON object of shape:
{
  "search_queries": ["DB_PASSWORD", "KeyError", "main", "import os"]
}

Emit 3-8 search queries. Be specific — exact strings from the error are better than generic terms.`

const investigatorRound2Prompt = `You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

You are given:
- An anomaly cluster describing what's wrong with a service
- Pod logs from the crashing container
- The file tree of the service's GitHub repository
- Search results from running queries against the local repo clone
- The detected programming language (if recognizable from the error)
- The pod's runtime context (env vars, resources, crash vitals) when available

## STEP 1 — Classify the fault mechanism (BEFORE forming hypotheses)
Using the traceback, the crash vitals (restarts / last termination reason), and the logs, classify the fault into exactly ONE class:

- "transient_infra" — network/DNS/timeout errors (gaierror, ECONNREFUSED, ECONNRESET, DNS resolution failures, timeouts, 502/503/504, rate limiting, temporarily unavailable). The failing call is usually CORRECT — what is missing is resilience. The fix belongs in the RETRY layer: the task/job retry policy with backoff and max_retries (e.g. Celery autoretry_for + retry_backoff), connection reacquisition, circuit breaker. NOT input validation, NOT the crash line.
- "resource_leak" — OOMKilled, heap out of memory, exit code 137/134, cannot allocate memory. The fix belongs at the GROWTH site: bound the collection, add eviction/TTL/cap, stream instead of accumulating. NOT guards at the crash line.
- "logic_data" — KeyError, NullPointerException, NoMethodError, index out of range, wrong type, invalid input reaching code. The fix belongs at the exact crash line.
- "lifecycle" — SIGTERM/SIGINT handling, graceful shutdown, startup ordering. The fix belongs in the signal/shutdown handler.
- "concurrency" — data race, deadlock, lock ordering, check-then-act on shared state. The fix belongs in the synchronization protocol; patching one call site keeps the race.

The crash location is where the failure SURFACED. For logic_data it is usually also the bug. For every other class the crash site is usually a VICTIM — the root cause lives in another file or layer (retry policy, growth site, shutdown handler, lock protocol).

## STEP 2 — Hypotheses
Rules:
- Base your hypothesis on the ACTUAL ERROR TRACEBACK and crash vitals, not on unrelated code you see in the file.
- Each hypothesis must be MECHANISM-LEVEL: name the underlying mechanism (what grows, what lacks retry, what races, what signal is mishandled), not just the visible symptom (which line crashed).
- If the error is a missing-key access (e.g. Python "KeyError: 99" at "name = users[user_id]", Java "NullPointerException" at "users.get(user_id)", Go "panic: runtime error: index out of range" at "users[user_id]"), the root cause is "the collection doesn't contain key 99". Do NOT speculate about databases, config, or infrastructure unless the traceback mentions them.
- If the error is a division by zero (e.g. Python "ZeroDivisionError", Java "ArithmeticException", Go "panic: runtime error: integer divide by zero"), the root cause is "no zero check before division". Do NOT mention database connections.
- Be specific: reference the exact variable, function, and line that caused the error.
- Do NOT hallucinate infrastructure that isn't in the traceback. If you see DATABASE_URL in the code but the error is about a dict key, the database is irrelevant.

Each hypothesis must be:
  - Specific (reference the exact line and variable that caused the crash)
  - Mechanism-level (state WHY it fails, not only WHERE it crashes)
  - Based on the traceback (not on code that wasn't involved in the crash)
  - Testable
  - Independent

Respond with a JSON object of shape:
{
  "fault_class": "transient_infra|resource_leak|logic_data|lifecycle|concurrency",
  "hypotheses": [
    {
      "statement": "...",
      "supporting_evidence": ["..."],
      "confidence": 0.0-1.0,
      "source_files": ["src/handler.<ext>"],
      "fix_layer": "which file/layer the fix belongs in and why"
    }
  ],
  "files_to_read": ["app/main.<ext>", "app/config.<ext>"]
}

List 1-6 files in files_to_read — the files most likely to contain the bug OR the mechanism that made the crash site a victim (retry policy, growth site, lock protocol). These will be read in full and passed to the Fix Proposer. Use the file extensions that actually appear in the file tree (e.g. .py, .go, .js, .ts, .java, .rs, .rb).

Do NOT make up evidence — only cite things that appear in the context.`

// Investigator forms root cause hypotheses using a two-round LLM loop:
// search -> reason -> read. Optional vector store enables a "second time,
// already fixed" fast path that skips the loop entirely.
type Investigator struct {
	router      *llm.Router
	k8s         *k8s.Client
	gitHubToken string
	cloner      *repo.Cloner
	vectorStore VectorStore

	allowedRepos  []string
	defaultBranch string
	gitHost       string
}

// SetAllowedRepos sets the GitHub repo allowlist. Empty means allow all.
func (a *Investigator) SetAllowedRepos(repos []string) {
	a.allowedRepos = repos
}

// SetDefaultBranch sets the default branch fallback when neither the
// sentinel.io/git-commit nor sentinel.io/branch annotation is present.
func (a *Investigator) SetDefaultBranch(branch string) {
	if branch != "" {
		a.defaultBranch = branch
	}
}

// SetGitHost propagates the git host to the cloner so GitHub Enterprise
// (or any other git host) is supported for clone URLs.
func (a *Investigator) SetGitHost(host string) {
	if host != "" {
		a.gitHost = host
		a.cloner.WithGitHost(host)
	}
}

// isRepoAllowed returns true if repo is on the allowlist. Empty allowlist
// means allow all.
func (a *Investigator) isRepoAllowed(repo string) bool {
	return IsRepoAllowed(repo, a.allowedRepos)
}

// IsRepoAllowed returns true if repo is on the allowlist. Empty allowlist
// means allow all. Exported so the agent-loop PipelineAdapter can enforce the
// same allowlist as the legacy Investigator without re-implementing the
// normalization rules.
func IsRepoAllowed(repo string, allowedRepos []string) bool {
	if len(allowedRepos) == 0 {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(repo))
	normalized = strings.TrimSuffix(normalized, ".git")
	for _, allowed := range allowedRepos {
		a := strings.ToLower(strings.TrimSpace(allowed))
		a = strings.TrimSuffix(a, ".git")
		if a == normalized {
			return true
		}
	}
	return false
}

// VectorStore is the interface for incident-memory retrieval used by the
// "second time, already fixed" fast path.
type VectorStore interface {
	Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error)
	UsingHashFallback() bool
}

// VectorMetadata is one past incident stored in the vector index.
type VectorMetadata struct {
	IncidentID string
	Service    string
	RootCause  string
	FixType    string
	FixPattern string
	Outcome    string
}

// NewInvestigator creates an Investigator. vectorStore may be nil.
func NewInvestigator(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, vectorStore VectorStore) *Investigator {
	return &Investigator{
		router:        router,
		k8s:           k8sClient,
		gitHubToken:   gitHubToken,
		cloner:        repo.NewCloner(gitHubToken),
		vectorStore:   vectorStore,
		defaultBranch: "main",
		gitHost:       "github.com",
	}
}

// Run executes the investigator agent.
func (a *Investigator) Run(ctx context.Context, state *schemas.AgentState) error {
	cluster := state.Cluster

	// Always fetch fresh pod logs via the K8s API (up to 8KB, retry 3x).
	// Falls back to whatever the detection layer already captured.
	var podLogs string
	if a.k8s != nil {
		podLogs, _ = a.k8s.FetchCrashingPodLogs(ctx, cluster.Service, cluster.Namespace)
	}
	if podLogs != "" {
		logging.Info("fetched pod logs via K8s API", "bytes", len(podLogs))
		fullExplanation := cluster.Summary + "\n\n## Pod Logs (full, from K8s API)\n" + podLogs
		if len(cluster.Anomalies) > 0 {
			cluster.Anomalies[0].Explanation = fullExplanation
		}
		state.Cluster = cluster
	} else {
		podLogs = a.extractPodLogs(cluster)
		logging.Info("K8s API logs unavailable, using detection logs", "bytes", len(podLogs))
	}

	// Runtime ground truth (v1.7.4): env var names, config refs, resource
	// limits from the live pod. Best-effort — a nil context just means the
	// prompts omit the section. Prevents the run-#11 user-api class of
	// confident-wrong fixes that rename env vars the deployment sets.
	if a.k8s != nil && state.RuntimeContext == nil {
		if rc, rerr := a.k8s.FetchPodRuntimeContext(ctx, cluster.Service, cluster.Namespace); rerr != nil {
			logging.Info("runtime context fetch failed — proceeding without it", "error", rerr)
		} else if rc != nil {
			state.RuntimeContext = rc
			logging.Info("fetched pod runtime context",
				"pod", rc.PodName, "env_vars", len(rc.EnvVarNames),
				"configmaps", len(rc.ConfigMapRefs), "secrets", len(rc.SecretRefs))
		}
	}

	// Memory loop: query the vector index for similar past incidents.
	// Hash-embedding fallbacks can produce false high-similarity scores, so
	// the applyKnownFix fast path is gated on UsingHashFallback() == false.
	if a.vectorStore != nil && podLogs != "" {
		similar, scores, err := a.vectorStore.Search(ctx, podLogs, 5)
		if err == nil && len(similar) > 0 {
			logging.Info("found similar incidents", "count", len(similar), "top_score", scores[0])

			if scores[0] > ConfidenceHighMatch && similar[0].Outcome == "success" && !a.vectorStore.UsingHashFallback() {
				logging.Info("high-similarity match — applying known fix from past incident",
					"score", scores[0], "incident_id", similar[0].IncidentID)
				return a.applyKnownFix(ctx, state, similar[0], podLogs)
			} else if scores[0] > ConfidenceHighMatch && a.vectorStore.UsingHashFallback() {
				logging.Info("high-similarity match but hash embeddings in use — skipping applyKnownFix for safety", "score", scores[0])
			}

			if scores[0] > ConfidenceMediumMatch {
				logging.Info("medium-similarity match — using past incident as LLM context",
					"score", scores[0], "incident_id", similar[0].IncidentID)
				state.RetrievedIncidents = []map[string]any{{
					"root_cause":  similar[0].RootCause,
					"fix_pattern": similar[0].FixPattern,
					"fix_type":    similar[0].FixType,
					"outcome":     similar[0].Outcome,
					"similarity":  scores[0],
					"incident_id": similar[0].IncidentID,
				}}
			}
		}
	}

	// Discover the GitHub repo from cluster annotations.
	repoName := discoverRepo(state, a.gitHost)
	if repoName == "" {
		logging.Info("no repo discovered — skipping code fetch")
		return a.runWithoutRepo(ctx, state, podLogs)
	}

	// Enforce the repo allowlist before cloning. Without this, a user with
	// pod-edit permission could annotate their pod with an arbitrary repo and
	// have Sentinel clone it (sending the GitHub PAT to github.com for any
	// repo the PAT can read).
	if !a.isRepoAllowed(repoName) {
		logging.Info("repo not in allowlist — refusing to clone", "repo", repoName)
		return a.runWithoutRepo(ctx, state, podLogs)
	}

	// Resolve which branch/commit to clone.
	branch := a.resolveBranch(ctx, state, repoName)
	logging.Info("cloning repo", "repo", repoName, "branch", branch)

	repoPath, err := a.cloner.Clone(ctx, repoName, branch)
	if err != nil {
		logging.Error("clone failed — falling back to no-repo mode", "err", err)
		return a.runWithoutRepo(ctx, state, podLogs)
	}
	defer a.cloner.Cleanup(repoPath)
	logging.Info("repo cloned", "path", repoPath)

	// Walk the file tree (filtered, capped).
	fileTree, err := repo.WalkTree(repoPath, 500)
	if err != nil {
		logging.Error("walk tree failed", "err", err)
		return a.runWithoutRepo(ctx, state, podLogs)
	}
	logging.Info("file tree walked", "files", len(fileTree))

	// Round 1: ask the LLM what to search for.
	round1Msg := a.buildRound1Message(state, podLogs, fileTree)
	round1Result, err := a.router.FrontierJSON(ctx, investigatorRound1Prompt, round1Msg, 1000)
	if err != nil {
		logging.Error("round 1 LLM failed", "err", err)
		return a.runWithoutRepo(ctx, state, podLogs)
	}

	searchQueries, _ := round1Result["search_queries"].([]any)
	if len(searchQueries) == 0 {
		// The error type and the variable name are right there in the traceback.
		logging.Info("LLM returned no search queries — extracting from pod logs")
		for _, q := range ExtractSearchQueriesUniversal(podLogs) {
			searchQueries = append(searchQueries, q)
		}
		if len(searchQueries) == 0 {
			logging.Info("no search queries from logs either — falling back to no-repo mode")
			return a.runWithoutRepo(ctx, state, podLogs)
		}
	}

	// Deterministic enrichment (v1.7.16): stack-trace identifiers are
	// ALWAYS merged into the query set — in run #29 the LLM's queries alone
	// found the crash file but never the mechanism file (user-api's
	// tokenStore.ts appeared in no result set). Fault-class hints add
	// leak/retry vocabulary when the crash shape indicates that mechanism.
	searchQueries = enrichSearchQueries(searchQueries, podLogs, state.RuntimeContext)
	logging.Info("search queries after enrichment", "count", len(searchQueries))

	// Run the searches locally (instant, no API calls).
	var allResults []repo.SearchResult
	seenPaths := map[string]bool{}
	for i, q := range searchQueries {
		if i >= MaxSearchQueries {
			break
		}
		pattern, _ := q.(string)
		if pattern == "" {
			continue
		}
		results, err := repo.Search(repoPath, pattern, 20)
		if err != nil {
			continue // invalid regex, skip
		}
		for _, r := range results {
			if !seenPaths[r.Path] {
				allResults = append(allResults, r)
				seenPaths[r.Path] = true
			}
		}
	}
	logging.Info("local search complete", "unique_files", len(allResults))

	// Round 2: ask the LLM for hypotheses + files to read.
	round2Msg := a.buildRound2Message(state, podLogs, fileTree, allResults)
	round2Result, err := a.router.FrontierJSON(ctx, investigatorRound2Prompt, round2Msg, 4000)
	if err != nil {
		logging.Error("round 2 LLM failed", "err", err)
		return a.runWithoutRepo(ctx, state, podLogs)
	}
	logging.Info("round 2 LLM returned", "keys", keysOfAny(round2Result))

	// Parse hypotheses.
	rawHyps, _ := round2Result["hypotheses"].([]any)
	faultClass, _ := round2Result["fault_class"].(string)
	if faultClass != "" {
		logging.Info("round 2 fault classification", "fault_class", faultClass)
	}
	logging.Info("round 2 returned hypotheses", "count", len(rawHyps))

	var newHyps []schemas.Hypothesis

	// If the LLM returned zero hypotheses, generate from the error context
	// directly so the analyst has something to falsify.
	if len(rawHyps) == 0 && podLogs != "" {
		logging.Error("LLM returned 0 hypotheses — generating from error context")
		newHyps = GenerateHypothesesUniversal(podLogs, state)
	}

	if len(newHyps) == 0 {
		for i, h := range rawHyps {
			if i >= MaxStackFilesToRead {
				break
			}
			hMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			statement, _ := hMap["statement"].(string)
			confidence, _ := hMap["confidence"].(float64)
			if confidence == 0 {
				confidence = 0.5
			}
			supporting, _ := hMap["supporting_evidence"].([]any)
			supportingStrs := toStringSlice(supporting)
			srcFiles, _ := hMap["source_files"].([]any)
			srcFilesStrs := toStringSlice(srcFiles)
			fixLayer, _ := hMap["fix_layer"].(string)
			newHyps = append(newHyps, schemas.Hypothesis{
				HID:                fmt.Sprintf("H%d", i+1),
				Statement:          statement,
				SupportingEvidence: supportingStrs,
				Confidence:         confidence,
				SourceFiles:        srcFilesStrs,
				FaultClass:         faultClass,
				FixLayer:           fixLayer,
			})
		}
	}

	// Read the files the LLM asked for.
	filesToRead, _ := round2Result["files_to_read"].([]any)
	fetched := map[string]string{}
	for _, f := range filesToRead {
		path, _ := f.(string)
		if path == "" {
			continue
		}
		content, err := repo.ReadFile(repoPath, path)
		if err != nil {
			logging.Info("could not read file", "path", path, "err", err)
			continue
		}
		fetched[path] = content
	}

	// Fallback: if the LLM returned zero files, try files from the stack
	// trace and the search results.
	if len(fetched) == 0 {
		logging.Info("LLM returned 0 files — falling back to stack trace parsing")
		stackFiles := k8s.ParseStackTrace(podLogs)
		for _, r := range allResults {
			stackFiles = append(stackFiles, r.Path)
		}
		for _, path := range stackFiles {
			if _, alreadyRead := fetched[path]; alreadyRead {
				continue
			}
			content, err := repo.ReadFile(repoPath, path)
			if err != nil {
				logging.Error("failed to read stack file", "path", path, "err", err)
				continue
			}
			fetched[path] = content
			if len(fetched) >= MaxFilesToRead {
				break
			}
		}
	}

	logging.Info("fetched files", "count", len(fetched), "paths", keysOf(fetched))

	state.Hypotheses = newHyps
	state.FetchedCode = fetched
	state.SourceFiles = keysOf(fetched)
	// pipeline.Run owns state.Iterations; do not increment here.

	trace := map[string]any{
		"agent":            "investigator",
		"iteration":        state.Iterations,
		"hypothesis_count": len(newHyps),
		"search_queries":   len(searchQueries),
		"search_results":   len(allResults),
		"fetched_files":    keysOf(fetched),
	}
	if faultClass != "" {
		trace["fault_class"] = faultClass
	}
	state.AgentTrace = append(state.AgentTrace, trace)

	return nil
}

// runWithoutRepo is the fallback when we can't clone the repo (private repo,
// network issue, no annotation, allowlist rejection). Forms hypotheses from
// logs alone, no source code.
func (a *Investigator) runWithoutRepo(ctx context.Context, state *schemas.AgentState, podLogs string) error {
	if podLogs == "" {
		podLogs = a.extractPodLogs(state.Cluster)
	}

	sourceFiles := k8s.ParseStackTrace(podLogs)
	state.SourceFiles = sourceFiles
	state.FetchedCode = map[string]string{}

	msg := fmt.Sprintf("# Service: %s\n# Namespace: %s\n\n## Pod Logs\n%s\n\n## Your Task\nProduce up to 5 root cause hypotheses based on the logs above. Be specific.",
		state.Cluster.Service, state.Cluster.Namespace, podLogs)

	result, err := a.router.FrontierJSON(ctx, investigatorRound2Prompt, msg, 2000)
	if err != nil {
		state.Status = schemas.StatusFailed
		s := fmt.Sprintf("investigator_llm_failure: %v", err)
		state.Error = &s
		return nil
	}

	rawHyps, _ := result["hypotheses"].([]any)
	var newHyps []schemas.Hypothesis
	for i, h := range rawHyps {
		if i >= MaxStackFilesToRead {
			break
		}
		hMap, ok := h.(map[string]any)
		if !ok {
			continue
		}
		statement, _ := hMap["statement"].(string)
		confidence, _ := hMap["confidence"].(float64)
		if confidence == 0 {
			confidence = 0.3 // lower confidence without source
		}
		newHyps = append(newHyps, schemas.Hypothesis{
			HID:        fmt.Sprintf("H%d", i+1),
			Statement:  statement,
			Confidence: confidence,
		})
	}
	state.Hypotheses = newHyps
	// pipeline.Run owns state.Iterations; do not increment here.
	return nil
}

// applyKnownFix skips the full investigation and applies a fix from a similar
// past incident. Clones the repo, fetches the file that was fixed last time,
// and creates a hypothesis directly — no LLM calls needed.
func (a *Investigator) applyKnownFix(_ context.Context, state *schemas.AgentState, meta *VectorMetadata, podLogs string) error {
	logging.Info("applying known fix from memory", "fix_pattern", meta.FixPattern, "fix_type", meta.FixType, "incident_id", meta.IncidentID)

	parsed := ParseError(podLogs)
	srcFiles := []string{}
	if parsed != nil && parsed.SourceFile != "" {
		srcFiles = append(srcFiles, parsed.SourceFile)
	}

	state.Hypotheses = []schemas.Hypothesis{{
		HID:       "H1",
		Statement: fmt.Sprintf("Known issue from incident %s: %s", meta.IncidentID, meta.RootCause),
		SupportingEvidence: []string{
			fmt.Sprintf("Memory match (similarity > 0.85) with incident %s", meta.IncidentID),
			fmt.Sprintf("Prior fix: %s", meta.FixPattern),
		},
		Confidence:  0.9, // high confidence — we've fixed this before
		SourceFiles: srcFiles,
	}}
	state.Iterations = 1

	state.RootCause = &schemas.RootCause{
		Statement:  meta.RootCause,
		Confidence: 0.9,
		Category:   "known_pattern",
	}
	state.Status = schemas.StatusRootCaused

	state.AgentTrace = append(state.AgentTrace, map[string]any{
		"agent":       "investigator",
		"action":      "memory_match",
		"incident_id": meta.IncidentID,
		"similarity":  "high",
	})

	logging.Info("root cause set from memory", "root_cause", meta.RootCause)
	return nil
}

// extractPodLogs concatenates the cluster summary + all anomaly explanations
// as a fallback when the K8s API fetch returned nothing.
func (a *Investigator) extractPodLogs(cluster schemas.AnomalyCluster) string {
	allText := cluster.Summary + "\n"
	for _, anomaly := range cluster.Anomalies {
		allText += anomaly.Explanation + "\n"
	}
	return allText
}

// extractCrashLocation formats the crash location from the traceback so the
// LLM focuses on the actual crash, not other similar-looking code in the file.
// Placed at the top of the investigator prompt.
func extractCrashLocation(podLogs string) string {
	parsed := ParseError(podLogs)
	if parsed == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## CRASH LOCATION (from traceback — THIS is the bug, not other code in the file)\n")
	fmt.Fprintf(&sb, "- Error type: %s\n", parsed.ErrorType)
	if parsed.ErrorDetail != "" {
		fmt.Fprintf(&sb, "- Error detail: %s\n", parsed.ErrorDetail)
	}
	if parsed.SourceFile != "" {
		fmt.Fprintf(&sb, "- Source file: %s\n", parsed.SourceFile)
	}
	if parsed.SourceLine != "" {
		fmt.Fprintf(&sb, "- Source line: %s\n", parsed.SourceLine)
	}
	if parsed.RawLine != "" {
		fmt.Fprintf(&sb, "- Crash line: %s\n", parsed.RawLine)
	}
	if parsed.Function != "" {
		fmt.Fprintf(&sb, "- Function: %s\n", parsed.Function)
	}

	sb.WriteString("\nIMPORTANT: the failure surfaced at the location above. If the fault is a logic/data error, the bug is usually right there. But if the evidence shows a transient infrastructure error (network/DNS/timeout), a resource kill (OOMKilled / exit 137 / heap out of memory), or a concurrency fault, this crash site is a VICTIM — form hypotheses about the MECHANISM (missing retry policy, unbounded growth, lock protocol), which often lives in a different file.\n")

	return sb.String()
}

// buildRound1Message builds the round 1 prompt: service info, pod logs,
// file tree, and a "emit search queries" task.
func (a *Investigator) buildRound1Message(state *schemas.AgentState, podLogs string, fileTree []string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
	parts = append(parts, "")
	parts = append(parts, "## Anomaly Cluster Summary")
	parts = append(parts, state.Cluster.Summary)
	parts = append(parts, "")

	if lang := detectLanguageHint(podLogs); lang != "" {
		parts = append(parts, fmt.Sprintf("## Detected Language: %s", lang))
		parts = append(parts, "")
	}
	if crashLoc := extractCrashLocation(podLogs); crashLoc != "" {
		parts = append(parts, crashLoc)
	}
	if rcBlock := renderRuntimeContext(state.RuntimeContext); rcBlock != "" {
		parts = append(parts, rcBlock)
	}
	parts = append(parts, "## Pod Logs")
	if podLogs != "" {
		if len(podLogs) > MaxPodLogsBytes {
			podLogs = podLogs[:MaxPodLogsBytes]
		}
		parts = append(parts, podLogs)
	} else {
		parts = append(parts, "(no logs available — pod may have crashed before writing any)")
	}
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("## Repository File Tree (%d files)", len(fileTree)))
	treeStr := strings.Join(fileTree[:min(300, len(fileTree))], "\n")
	parts = append(parts, treeStr)
	parts = append(parts, "")
	parts = append(parts, "## Your Task")
	parts = append(parts, "Emit 3-8 search queries (regex patterns) to find the source of the bug in the repo above.")
	return strings.Join(parts, "\n")
}

// buildRound2Message builds the round 2 prompt: service info, pod logs, file
// tree, search results, and a "produce hypotheses + files to read" task.
func (a *Investigator) buildRound2Message(state *schemas.AgentState, podLogs string, fileTree []string, searchResults []repo.SearchResult) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
	parts = append(parts, "")

	if lang := detectLanguageHint(podLogs); lang != "" {
		parts = append(parts, fmt.Sprintf("## Detected Language: %s", lang))
		parts = append(parts, "")
	}
	if crashLoc := extractCrashLocation(podLogs); crashLoc != "" {
		parts = append(parts, crashLoc)
	}
	if rcBlock := renderRuntimeContext(state.RuntimeContext); rcBlock != "" {
		parts = append(parts, rcBlock)
	}
	parts = append(parts, "## Pod Logs")
	if podLogs != "" {
		if len(podLogs) > MaxPodLogsBytes {
			podLogs = podLogs[:MaxPodLogsBytes]
		}
		parts = append(parts, podLogs)
	} else {
		parts = append(parts, "(no logs available)")
	}
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("## Repository File Tree (%d files)", len(fileTree)))
	parts = append(parts, strings.Join(fileTree[:min(200, len(fileTree))], "\n"))
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("## Search Results (%d matches)", len(searchResults)))
	for i, r := range searchResults {
		if i >= MaxSearchResultsShown {
			break
		}
		parts = append(parts, fmt.Sprintf("- %s:%d — %s", r.Path, r.Line, r.Context))
	}
	parts = append(parts, "")
	parts = append(parts, "## Your Task")
	parts = append(parts, "Classify the fault mechanism, produce up to 5 mechanism-level root cause hypotheses, and list 1-6 files to read in full (crash-site files AND mechanism files).")
	return strings.Join(parts, "\n")
}

// enrichSearchQueries merges deterministic queries into the LLM's round-1
// query set (v1.7.16): stack-trace identifiers ALWAYS, plus mechanism-specific
// vocabulary when the crash shape indicates a resource leak or a transient
// fault. Deterministic, no LLM — the queries only widen what the local search
// can surface; the round-2 LLM still filters what matters.
func enrichSearchQueries(llmQueries []any, podLogs string, rc *schemas.PodRuntimeContext) []any {
	seen := map[string]bool{}
	var out []any
	add := func(q string) {
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		out = append(out, q)
	}
	for _, q := range llmQueries {
		if s, ok := q.(string); ok {
			add(s)
		}
	}
	for _, q := range ExtractSearchQueriesUniversal(podLogs) {
		add(q)
	}

	lower := strings.ToLower(podLogs)
	oomShape := (rc != nil && rc.OOMKilled) ||
		strings.Contains(lower, "out of memory") ||
		strings.Contains(lower, "oom") ||
		strings.Contains(lower, "cannot allocate")
	if oomShape {
		// Leak-site vocabulary: where does data only grow?
		for _, q := range []string{
			`cache|Cache`,
			`token|session`,
			`evict|expir|ttl|TTL|lru|maxsize|max_size|MAX_|_CAP|capacity`,
			`new Map|\.set\(|\.push\(|\.append\(|\.add\(`,
		} {
			add(q)
		}
	}
	transientShape := strings.Contains(lower, "gaierror") ||
		strings.Contains(lower, "econnrefused") ||
		strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "temporarily unavailable")
	if transientShape {
		// Resilience-layer vocabulary: where is the retry policy?
		for _, q := range []string{
			`retry|retries|max_retries|maxRetries|MAX_RETRIES`,
			`autoretry|retry_backoff|retry_policy|backoff|retry_delay`,
			`acks_late|on_failure|retry_kwargs`,
		} {
			add(q)
		}
	}

	if len(out) > MaxSearchQueries {
		out = out[:MaxSearchQueries]
	}
	return out
}

// detectLanguageHint parses the pod logs for the error type + language and
// returns a human-readable language hint for the LLM prompt. Returns "" if no
// language can be detected.
func detectLanguageHint(podLogs string) string {
	if podLogs == "" {
		return ""
	}
	parsed := ParseError(podLogs)
	if parsed == nil {
		return ""
	}
	if parsed.Language == "" || parsed.Language == "unknown" {
		return ""
	}
	switch parsed.Language {
	case "python":
		return "Python (idiomatic errors: KeyError, ValueError, TypeError, ImportError, ZeroDivisionError, AttributeError)"
	case "go":
		return "Go (idiomatic errors: panic, runtime error: index out of range, nil pointer dereference, goroutine stack)"
	case "java":
		return "Java (idiomatic errors: NullPointerException, ArrayIndexOutOfBoundsException, ClassNotFoundException, IllegalArgumentException)"
	case "javascript":
		return "JavaScript/TypeScript (idiomatic errors: TypeError, ReferenceError, SyntaxError, UnhandledPromiseRejection)"
	case "rust":
		return "Rust (idiomatic errors: panic, panicked at, unwrap on None/Err, thread 'main' panicked)"
	case "ruby":
		return "Ruby (idiomatic errors: NoMethodError, NameError, ArgumentError, TypeError)"
	case "c":
		return "C/C++ (idiomatic errors: SIGSEGV, SIGABRT, Segmentation fault, assertion failed)"
	default:
		return parsed.Language
	}
}

// toStringSlice converts []any to []string, skipping non-string entries.
func toStringSlice(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// keysOfAny returns the keys of m as a slice with no defined order. Used for
// debug logging of LLM JSON responses.
func keysOfAny(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
