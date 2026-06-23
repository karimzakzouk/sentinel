package agents

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "regexp"
        "strings"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/llm"
        "github.com/karimzakzouk/sentinel/v3/internal/repo"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

const investigatorRound1Prompt = `You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

You are given:
- An anomaly cluster describing what's wrong with a service
- Pod logs from the crashing container (may be empty if logs weren't available)
- The file tree of the service's GitHub repository

Your job is to decide what to SEARCH for in the source code to investigate the incident. You'll emit search queries (regex patterns) that will be run against the local clone of the repo.

Think about:
- Error messages or stack traces in the logs → search for the error type (e.g. "KeyError", "DB_PASSWORD", "connection refused")
- Variable names or config keys mentioned in errors → search for those exact strings
- Function or class names in stack traces → search for "def functionName" or "class ClassName"
- Import errors → search for the module name

Respond with a JSON object of shape:
{
  "search_queries": ["DB_PASSWORD", "KeyError", "def main", "import os"]
}

Emit 3-8 search queries. Be specific — exact strings from the error are better than generic terms.`

const investigatorRound2Prompt = `You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

You are given:
- An anomaly cluster describing what's wrong with a service
- Pod logs from the crashing container
- The file tree of the service's GitHub repository
- Search results from running queries against the local repo clone

Your job: produce UP TO 5 root cause hypotheses AND list the files we should read in full to write a fix. Each hypothesis must be:
  - Specific (e.g. NOT "the database is slow" — instead "the connection pool is exhausted because max_connections was reduced")
  - Testable
  - Independent

Respond with a JSON object of shape:
{
  "hypotheses": [
    {
      "statement": "...",
      "supporting_evidence": ["..."],
      "confidence": 0.0-1.0,
      "source_files": ["src/foo.py"]
    }
  ],
  "files_to_read": ["app/main.py", "app/config.py"]
}

List 1-5 files in files_to_read — the files most likely to contain the bug. These will be read in full and passed to the Fix Proposer.

Do NOT make up evidence — only cite things that appear in the context.`

// Investigator forms root cause hypotheses using a two-round agentic loop:
//   Round 1: LLM sees logs + file tree → emits search queries
//   Round 2: LLM sees logs + file tree + search results → emits hypotheses + files to read
//
// This is the same pattern SWE-agent and OpenHands use: search → reason → read.
type Investigator struct {
        router      *llm.Router
        k8s         *k8s.Client
        gitHubToken string
        cloner      *repo.Cloner
        vectorStore VectorStore
}

// VectorStore is the interface for incident memory retrieval.
type VectorStore interface {
        Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error)
}

// VectorMetadata is a past incident stored in the vector index.
type VectorMetadata struct {
        IncidentID string
        Service    string
        RootCause  string
        FixType    string
        FixPattern string
        Outcome    string
}

// NewInvestigator creates an Investigator with optional vector store for memory loop.
func NewInvestigator(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, vectorStore VectorStore) *Investigator {
        return &Investigator{
                router:      router,
                k8s:         k8sClient,
                gitHubToken: gitHubToken,
                cloner:      repo.NewCloner(gitHubToken),
                vectorStore: vectorStore,
        }
}

// Run executes the investigator agent.
// v3.2.4: ALWAYS fetch full pod logs via K8s API at the start.
// v3.3.0: Memory loop — query HNSW for similar past incidents first.
//         If similarity > 0.85 → skip investigation, apply known fix directly.
func (a *Investigator) Run(ctx context.Context, state *schemas.AgentState) error {
        cluster := state.Cluster

        // 0. ALWAYS fetch full pod logs via K8s API (8KB, retry 3x).
        podLogs, _ := a.k8s.FetchCrashingPodLogs(ctx, cluster.Service, cluster.Namespace)
        if podLogs != "" {
                log.Printf("[investigator] fetched %d chars of pod logs via K8s API", len(podLogs))
                fullExplanation := cluster.Summary + "\n\n## Pod Logs (full, from K8s API)\n" + podLogs
                if len(cluster.Anomalies) > 0 {
                        cluster.Anomalies[0].Explanation = fullExplanation
                }
                state.Cluster = cluster
        } else {
                podLogs = a.extractPodLogs(cluster)
                log.Printf("[investigator] K8s API logs unavailable, using %d chars from detection", len(podLogs))
        }

        // 0.5 MEMORY LOOP — query HNSW for similar past incidents.
        // If we've seen this exact pattern before AND it was successfully fixed,
        // skip the full investigation and apply the known fix directly.
        if a.vectorStore != nil && podLogs != "" {
                similar, scores, err := a.vectorStore.Search(ctx, podLogs, 5)
                if err == nil && len(similar) > 0 {
                        log.Printf("[memory] found %d similar incidents (top score: %.3f)", len(similar), scores[0])

                        // High similarity + prior success → skip investigation, apply known fix
                        if scores[0] > 0.85 && similar[0].Outcome == "success" {
                                log.Printf("[memory] HIGH MATCH (score=%.3f) — applying known fix from incident %s", scores[0], similar[0].IncidentID)
                                return a.applyKnownFix(ctx, state, similar[0], podLogs)
                        }

                        // Medium similarity → use as context for the LLM
                        if scores[0] > 0.5 {
                                log.Printf("[memory] MEDIUM MATCH (score=%.3f) — using incident %s as context", scores[0], similar[0].IncidentID)
                                state.RetrievedIncidents = []map[string]any{{
                                        "root_cause":   similar[0].RootCause,
                                        "fix_pattern":  similar[0].FixPattern,
                                        "fix_type":     similar[0].FixType,
                                        "outcome":      similar[0].Outcome,
                                        "similarity":   scores[0],
                                        "incident_id":  similar[0].IncidentID,
                                }}
                        }
                }
        }

        // 1. Discover the GitHub repo from cluster annotations
        repoName := discoverRepo(state)
        if repoName == "" {
                log.Printf("[investigator] no repo discovered — skipping code fetch")
                return a.runWithoutRepo(ctx, state, podLogs)
        }

        // 2. Resolve which branch/commit to clone.
        branch := a.resolveBranch(ctx, state, repoName)
        log.Printf("[investigator] cloning repo %s@%s", repoName, branch)

        // 3. Clone the repo locally
        repoPath, err := a.cloner.Clone(ctx, repoName, branch)
        if err != nil {
                log.Printf("[investigator] clone failed: %v — falling back to no-repo mode", err)
                return a.runWithoutRepo(ctx, state, podLogs)
        }
        defer a.cloner.Cleanup(repoPath)
        log.Printf("[investigator] repo cloned to %s", repoPath)

        // 4. Walk the file tree
        fileTree, err := repo.WalkTree(repoPath, 500)
        if err != nil {
                log.Printf("[investigator] walk tree failed: %v", err)
                return a.runWithoutRepo(ctx, state, podLogs)
        }
        log.Printf("[investigator] file tree: %d files", len(fileTree))

        // 4. Round 1 — ask LLM what to search for
        // podLogs already fetched at the top of Run() via K8s API
        round1Msg := a.buildRound1Message(state, podLogs, fileTree)
        round1Result, err := a.router.FrontierJSON(ctx, investigatorRound1Prompt, round1Msg, 1000)
        if err != nil {
                log.Printf("[investigator] round 1 LLM failed: %v", err)
                return a.runWithoutRepo(ctx, state, podLogs)
        }

        searchQueries, _ := round1Result["search_queries"].([]any)
        if len(searchQueries) == 0 {
                // v3.2.3: Don't give up — extract search queries from the pod logs.
                // The error type (KeyError, ZeroDivisionError) and the variable name
                // (DATABASE_URLL) are right there in the traceback.
                log.Printf("[investigator] LLM returned no search queries — extracting from pod logs")
                fallbackQueries := ExtractSearchQueriesUniversal(podLogs)
                for _, q := range fallbackQueries {
                        searchQueries = append(searchQueries, q)
                }
                if len(searchQueries) == 0 {
                        log.Printf("[investigator] no search queries from logs either — falling back")
                        return a.runWithoutRepo(ctx, state, podLogs)
                }
        }

        // 5. Run the searches locally (instant, no API calls)
        var allResults []repo.SearchResult
        seenPaths := map[string]bool{}
        for i, q := range searchQueries {
                if i >= 8 {
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
        log.Printf("[investigator] search found %d unique files", len(allResults))

        // 6. Round 2 — ask LLM for hypotheses + files to read
        round2Msg := a.buildRound2Message(state, podLogs, fileTree, allResults)
        round2Result, err := a.router.FrontierJSON(ctx, investigatorRound2Prompt, round2Msg, 4000)
        if err != nil {
                log.Printf("[investigator] round 2 LLM failed: %v", err)
                return a.runWithoutRepo(ctx, state, podLogs)
        }

        // Debug: log what the LLM returned
        log.Printf("[investigator] round 2 LLM returned keys: %v", keysOfAny(round2Result))

        // 7. Parse hypotheses
        rawHyps, _ := round2Result["hypotheses"].([]any)
        log.Printf("[investigator] round 2 returned %d hypotheses", len(rawHyps))

        var newHyps []schemas.Hypothesis

        // v3.2.5: FALLBACK — if LLM returned 0 hypotheses but we have pod logs,
        // generate hypotheses from the error context directly.
        if len(rawHyps) == 0 && podLogs != "" {
                log.Printf("[investigator] LLM returned 0 hypotheses — generating from error context")
                newHyps = GenerateHypothesesUniversal(podLogs, state)
        }

        // Parse LLM hypotheses if we got them
        if len(newHyps) == 0 {
                for i, h := range rawHyps {
                        if i >= 5 {
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
                        newHyps = append(newHyps, schemas.Hypothesis{
                                HID:                fmt.Sprintf("H%d", i+1),
                                Statement:          statement,
                                SupportingEvidence: supportingStrs,
                                Confidence:         confidence,
                                SourceFiles:        srcFilesStrs,
                        })
                }
        }

        // 8. Read the files the LLM asked for
        filesToRead, _ := round2Result["files_to_read"].([]any)
        fetched := map[string]string{}
        for _, f := range filesToRead {
                path, _ := f.(string)
                if path == "" {
                        continue
                }
                content, err := repo.ReadFile(repoPath, path)
                if err != nil {
                        log.Printf("[investigator] could not read %s: %v", path, err)
                        continue
                }
                fetched[path] = content
        }

        // 9. FALLBACK: If the LLM returned 0 files, try files from the stack trace
        if len(fetched) == 0 {
                log.Printf("[investigator] LLM returned 0 files — falling back to stack trace parsing")
                stackFiles := k8s.ParseStackTrace(podLogs)
                // Also try files from search results
                for _, r := range allResults {
                        stackFiles = append(stackFiles, r.Path)
                }
                for _, path := range stackFiles {
                        if _, alreadyRead := fetched[path]; alreadyRead {
                                continue
                        }
                        content, err := repo.ReadFile(repoPath, path)
                        if err != nil {
                                continue
                        }
                        fetched[path] = content
                        if len(fetched) >= 5 {
                                break
                        }
                }
        }

        log.Printf("[investigator] fetched %d files: %v", len(fetched), keysOf(fetched))

        state.Hypotheses = newHyps
        state.FetchedCode = fetched
        state.SourceFiles = keysOf(fetched)
        state.Iterations++

        trace := map[string]any{
                "agent":            "investigator",
                "iteration":        state.Iterations,
                "hypothesis_count": len(newHyps),
                "search_queries":   len(searchQueries),
                "search_results":   len(allResults),
                "fetched_files":    keysOf(fetched),
        }
        state.AgentTrace = append(state.AgentTrace, trace)

        return nil
}

// runWithoutRepo is the fallback when we can't clone the repo (private repo,
// network issue, etc.). Forms hypotheses from logs alone, no source code.
func (a *Investigator) runWithoutRepo(ctx context.Context, state *schemas.AgentState, podLogs string) error {
        // If no podLogs passed, try to extract from cluster
        if podLogs == "" {
                podLogs = a.extractPodLogs(state.Cluster)
        }

        // Parse stack traces from logs for source file hints
        sourceFiles := k8s.ParseStackTrace(podLogs)
        state.SourceFiles = sourceFiles
        state.FetchedCode = map[string]string{}

        // Build a simple prompt with just the logs
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
                if i >= 5 {
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
        state.Iterations++
        return nil
}

// applyKnownFix skips the full investigation and applies a fix from a similar
// past incident. This is the "second time, already fixed" feature.
// It clones the repo, fetches the file that was fixed last time, and creates
// a FixProposal directly — no LLM calls needed.
func (a *Investigator) applyKnownFix(ctx context.Context, state *schemas.AgentState, meta *VectorMetadata, podLogs string) error {
        log.Printf("[memory] applying known fix: %s (type=%s)", meta.FixPattern, meta.FixType)

        // Parse the error from the current logs to get the source file
        parsed := ParseError(podLogs)
        srcFiles := []string{}
        if parsed != nil && parsed.SourceFile != "" {
                srcFiles = append(srcFiles, parsed.SourceFile)
        }

        // Create a hypothesis directly from the memory match
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

        // Set the root cause directly
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

        log.Printf("[memory] root cause set from memory: %s", meta.RootCause)
        return nil
}

func (a *Investigator) extractPodLogs(cluster schemas.AnomalyCluster) string {
        allText := cluster.Summary + "\n"
        for _, anomaly := range cluster.Anomalies {
                allText += anomaly.Explanation + "\n"
        }
        return allText
}

func (a *Investigator) buildRound1Message(state *schemas.AgentState, podLogs string, fileTree []string) string {
        var parts []string
        parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
        parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
        parts = append(parts, "")
        parts = append(parts, "## Anomaly Cluster Summary")
        parts = append(parts, state.Cluster.Summary)
        parts = append(parts, "")
        parts = append(parts, "## Pod Logs")
        if podLogs != "" {
                // Cap logs to 4KB for the LLM
                if len(podLogs) > 4096 {
                        podLogs = podLogs[:4096]
                }
                parts = append(parts, podLogs)
        } else {
                parts = append(parts, "(no logs available — pod may have crashed before writing any)")
        }
        parts = append(parts, "")
        parts = append(parts, fmt.Sprintf("## Repository File Tree (%d files)", len(fileTree)))
        // Cap tree at 300 files for the prompt
        treeStr := strings.Join(fileTree[:min(300, len(fileTree))], "\n")
        parts = append(parts, treeStr)
        parts = append(parts, "")
        parts = append(parts, "## Your Task")
        parts = append(parts, "Emit 3-8 search queries (regex patterns) to find the source of the bug in the repo above.")
        return strings.Join(parts, "\n")
}

func (a *Investigator) buildRound2Message(state *schemas.AgentState, podLogs string, fileTree []string, searchResults []repo.SearchResult) string {
        var parts []string
        parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
        parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
        parts = append(parts, "")
        parts = append(parts, "## Pod Logs")
        if podLogs != "" {
                if len(podLogs) > 4096 {
                        podLogs = podLogs[:4096]
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
        // Cap at 50 results for the prompt
        for i, r := range searchResults {
                if i >= 50 {
                        break
                }
                parts = append(parts, fmt.Sprintf("- %s:%d — %s", r.Path, r.Line, r.Context))
        }
        parts = append(parts, "")
        parts = append(parts, "## Your Task")
        parts = append(parts, "Produce up to 5 root cause hypotheses AND list 1-5 files to read in full. Be specific.")
        return strings.Join(parts, "\n")
}

func toStringSlice(in []any) []string {
        out := make([]string, 0, len(in))
        for _, v := range in {
                if s, ok := v.(string); ok {
                        out = append(out, s)
                }
        }
        return out
}

// uuidNew is a convenience wrapper.
func uuidNew() uuid.UUID { return uuid.New() }

// jsonMarshal helper.
func jsonMarshalString(v any) string {
        b, _ := json.Marshal(v)
        return string(b)
}

// extractSearchQueriesFromLogs pulls search terms from the pod logs traceback.
// When the LLM can't figure out what to search for, we extract:
//   - The error type (KeyError, ZeroDivisionError, etc.)
//   - The variable/key that caused the error (DATABASE_URLL, etc.)
//   - The source file from the stack trace (app/main.py)
func extractSearchQueriesFromLogs(logs string) []string {
        var queries []string

        // Extract error type + key: "KeyError: 'DATABASE_URLL'"
        errorRe := regexp.MustCompile(`(\w+Error|w+Exception):\s*['"]?([^'"]+)['"]?`)
        if m := errorRe.FindStringSubmatch(logs); len(m) >= 2 {
                queries = append(queries, m[1]) // e.g. "KeyError"
                if len(m) >= 3 && m[2] != "" {
                        queries = append(queries, m[2]) // e.g. "DATABASE_URLL"
                }
        }

        // Extract source files from the traceback: 'File "/app/app/main.py", line 11'
        fileRe := regexp.MustCompile(`File "([^"]+\.py)", line`)
        for _, m := range fileRe.FindAllStringSubmatch(logs, -1) {
                if len(m) >= 2 {
                        // Strip container paths — just use the repo-relative path
                        path := m[1]
                        if idx := strings.Index(path, "/app/"); idx >= 0 {
                                path = path[idx+5:] // strip "/app/" prefix
                        }
                        queries = append(queries, path)
                }
        }

        // Also extract any os.environ key references
        envRe := regexp.MustCompile(`os\.environ\[["']([^"']+)["']\]`)
        if m := envRe.FindStringSubmatch(logs); len(m) >= 2 {
                queries = append(queries, m[1])
        }

        // Dedup
        seen := map[string]bool{}
        var unique []string
        for _, q := range queries {
                if !seen[q] {
                        seen[q] = true
                        unique = append(unique, q)
                }
        }

        log.Printf("[investigator] extracted %d search queries from logs: %v", len(unique), unique)
        return unique
}

// generateHypothesesFromLogs creates hypotheses directly from the pod logs
// when the LLM fails to return any. This is the deterministic fallback that
// ensures the pipeline always has at least one hypothesis to work with.
//
// It parses the traceback to extract:
//   - The error type (KeyError, ValueError, ZeroDivisionError, etc.)
//   - The error detail (the key name, the variable, etc.)
//   - The source file + line from the stack trace
//   - The actual code line that caused the error
func generateHypothesesFromLogs(logs string, state *schemas.AgentState) []schemas.Hypothesis {
        var hyps []schemas.Hypothesis

        // Extract error type + detail: "KeyError: 'DATABASE_URLL'"
        errorRe := regexp.MustCompile(`(\w+Error|\w+Exception):\s*['"]?([^'"\n]+)?['"]?`)
        m := errorRe.FindStringSubmatch(logs)
        if m == nil {
                // Try Go panic: "panic: ..."
                panicRe := regexp.MustCompile(`panic:\s*(.+)`)
                if pm := panicRe.FindStringSubmatch(logs); pm != nil {
                        hyps = append(hyps, schemas.Hypothesis{
                                HID:        "H1",
                                Statement:  fmt.Sprintf("Go panic: %s", pm[1]),
                                Confidence: 0.7,
                        })
                        return hyps
                }
                return nil
        }

        errorType := m[1]
        errorDetail := ""
        if len(m) >= 3 {
                errorDetail = strings.TrimSpace(m[2])
        }

        // Extract source file from traceback: File "/app/app/main.py", line 11
        fileRe := regexp.MustCompile(`File "([^"]+)", line (\d+), in`)
        fm := fileRe.FindStringSubmatch(logs)
        sourceFile := ""
        sourceLine := ""
        if len(fm) >= 3 {
                sourceFile = fm[1]
                sourceLine = fm[2]
                // Strip container paths
                if idx := strings.Index(sourceFile, "/app/"); idx >= 0 {
                        sourceFile = sourceFile[idx+5:]
                }
        }

        // Build the hypothesis statement
        statement := fmt.Sprintf("The application crashes with %s", errorType)
        if errorDetail != "" {
                statement += fmt.Sprintf(" on '%s'", errorDetail)
        }
        if sourceFile != "" {
                statement += fmt.Sprintf(" at %s:%s", sourceFile, sourceLine)
        }
        if errorType == "KeyError" && errorDetail != "" {
                statement += fmt.Sprintf(". The environment variable or key '%s' is missing or misspelled.", errorDetail)
        } else if errorType == "ZeroDivisionError" {
                statement += ". A division by zero occurs, likely because a variable is not checked for zero before division."
        } else if errorType == "ImportError" || errorType == "ModuleNotFoundError" {
                statement += ". A required module is missing or the import path is incorrect."
        } else if errorType == "FileNotFoundError" {
                statement += ". A required file is missing."
        }

        srcFiles := []string{}
        if sourceFile != "" {
                srcFiles = append(srcFiles, sourceFile)
        }

        hyps = append(hyps, schemas.Hypothesis{
                HID:                "H1",
                Statement:          statement,
                SupportingEvidence: []string{fmt.Sprintf("Pod logs show: %s: %s", errorType, errorDetail)},
                Confidence:         0.7,
                SourceFiles:        srcFiles,
        })

        // Add a second hypothesis for less likely causes
        if errorType == "KeyError" {
                hyps = append(hyps, schemas.Hypothesis{
                        HID:        "H2",
                        Statement:  fmt.Sprintf("The environment variable '%s' is not set in the deployment configuration", errorDetail),
                        Confidence: 0.3,
                })
        }

        log.Printf("[investigator] generated %d hypotheses from error context", len(hyps))
        return hyps
}

// keysOfAny returns the keys of a map[string]any for debug logging.
func keysOfAny(m map[string]any) []string {
        keys := make([]string, 0, len(m))
        for k := range m {
                keys = append(keys, k)
        }
        return keys
}
