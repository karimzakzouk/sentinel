package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/karimzakzoukz/sentinel/v3/internal/k8s"
	"github.com/karimzakzoukz/sentinel/v3/internal/llm"
	"github.com/karimzakzoukz/sentinel/v3/internal/repo"
	"github.com/karimzakzoukz/sentinel/v3/internal/schemas"
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
}

// NewInvestigator creates an Investigator.
func NewInvestigator(router *llm.Router, k8sClient *k8s.Client, gitHubToken string) *Investigator {
	return &Investigator{
		router:      router,
		k8s:         k8sClient,
		gitHubToken: gitHubToken,
		cloner:      repo.NewCloner(gitHubToken),
	}
}

// Run executes the investigator agent.
func (a *Investigator) Run(ctx context.Context, state *schemas.AgentState) error {
	cluster := state.Cluster

	// 1. Discover the GitHub repo from cluster annotations
	repoName := discoverRepo(state)
	if repoName == "" {
		log.Printf("[investigator] no repo discovered — skipping code fetch")
		return a.runWithoutRepo(ctx, state)
	}

	// 2. Clone the repo locally (this is what SWE-agent / OpenHands do)
	log.Printf("[investigator] cloning repo %s", repoName)
	repoPath, err := a.cloner.Clone(ctx, repoName, "main")
	if err != nil {
		log.Printf("[investigator] clone failed: %v — falling back to no-repo mode", err)
		return a.runWithoutRepo(ctx, state)
	}
	defer a.cloner.Cleanup(repoPath)
	log.Printf("[investigator] repo cloned to %s", repoPath)

	// 3. Walk the file tree
	fileTree, err := repo.WalkTree(repoPath, 500)
	if err != nil {
		log.Printf("[investigator] walk tree failed: %v", err)
		return a.runWithoutRepo(ctx, state)
	}
	log.Printf("[investigator] file tree: %d files", len(fileTree))

	// 4. Round 1 — ask LLM what to search for
	podLogs := a.extractPodLogs(cluster)
	round1Msg := a.buildRound1Message(state, podLogs, fileTree)
	round1Result, err := a.router.FrontierJSON(ctx, investigatorRound1Prompt, round1Msg, 1000)
	if err != nil {
		log.Printf("[investigator] round 1 LLM failed: %v", err)
		return a.runWithoutRepo(ctx, state)
	}

	searchQueries, _ := round1Result["search_queries"].([]any)
	if len(searchQueries) == 0 {
		log.Printf("[investigator] LLM returned no search queries — falling back")
		return a.runWithoutRepo(ctx, state)
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
		return a.runWithoutRepo(ctx, state)
	}

	// 7. Parse hypotheses
	rawHyps, _ := round2Result["hypotheses"].([]any)
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
func (a *Investigator) runWithoutRepo(ctx context.Context, state *schemas.AgentState) error {
	podLogs := a.extractPodLogs(state.Cluster)

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

// discoverRepo extracts the GitHub repo from the cluster's annotations.
func discoverRepo(state *schemas.AgentState) string {
	if repo, ok := state.Cluster.Annotations["sentinel.io/repo"]; ok {
		return normalizeRepo(repo)
	}
	for _, anomaly := range state.Cluster.Anomalies {
		if repo, ok := anomaly.Annotations["sentinel.io/repo"]; ok {
			return normalizeRepo(repo)
		}
	}
	return ""
}

// normalizeRepo strips "github.com/" prefix if present.
func normalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	return strings.TrimSuffix(repo, ".git")
}

// uuidNew is a convenience wrapper.
func uuidNew() uuid.UUID { return uuid.New() }

// jsonMarshal helper.
func jsonMarshalString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
