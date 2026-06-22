package agents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/karimzakzoukz/sentinel/v3/internal/k8s"
	"github.com/karimzakzoukz/sentinel/v3/internal/llm"
	"github.com/karimzakzoukz/sentinel/v3/internal/schemas"
)

const investigatorSystemPrompt = `You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

Given:
- An anomaly cluster describing what's wrong with a service
- Recent log lines and trace spans (if available)
- Similar past incidents retrieved from the incident memory
- Source code of files mentioned in stack traces

Your job: produce UP TO 5 root cause hypotheses. Each hypothesis must be:
  - Specific (e.g. NOT "the database is slow" — instead "the connection pool is exhausted because max_connections was reduced in commit abc123")
  - Testable (the Evidence Gatherer must be able to confirm or refute it via Loki/Prometheus/Jaeger queries)
  - Independent (hypotheses should not just be rephrasings of each other)

Rank hypotheses by likelihood given the priors.

Respond with a JSON object of shape:
{
  "hypotheses": [
    {
      "statement": "...",
      "supporting_evidence": ["..."],
      "confidence": 0.0-1.0,
      "source_files": ["src/foo.py"]
    }
  ]
}

Do NOT make up evidence — only cite things that appear in the context.`

// Investigator forms up to 5 root cause hypotheses based on the cluster + context.
type Investigator struct {
	router      *llm.Router
	k8s         *k8s.Client
	gitHubToken string
}

// NewInvestigator creates an Investigator.
func NewInvestigator(router *llm.Router, k8sClient *k8s.Client, gitHubToken string) *Investigator {
	return &Investigator{router: router, k8s: k8sClient, gitHubToken: gitHubToken}
}

// Run executes the investigator agent.
func (a *Investigator) Run(ctx context.Context, state *schemas.AgentState) error {
	cluster := state.Cluster

	// 1. Parse stack traces from logs to find source files
	allText := cluster.Summary + "\n"
	for _, anomaly := range cluster.Anomalies {
		allText += anomaly.Explanation + "\n"
	}
	sourceFiles := k8s.ParseStackTrace(allText)
	state.SourceFiles = uniqueStrings(append(state.SourceFiles, sourceFiles...))

	// 2. Discover the GitHub repo from cluster annotations
	repo := discoverRepo(state)

	// 3. Fetch source code for the files we identified
	fetched := a.fetchFilesForCluster(ctx, cluster, repo)
	state.FetchedCode = fetched

	// 4. Build the LLM prompt
	userMsg := a.buildUserMessage(state, fetched)

	// 5. Call LLM
	result, err := a.router.FrontierJSON(ctx, investigatorSystemPrompt, userMsg, 4000)
	if err != nil {
		state.Status = schemas.StatusFailed
		s := fmt.Sprintf("investigator_llm_failure: %v", err)
		state.Error = &s
		return nil
	}

	// 6. Parse + validate hypotheses
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
			confidence = 0.5
		}
		supporting, _ := hMap["supporting_evidence"].([]any)
		supportingStrs := make([]string, 0, len(supporting))
		for _, s := range supporting {
			if str, ok := s.(string); ok {
				supportingStrs = append(supportingStrs, str)
			}
		}
		srcFiles, _ := hMap["source_files"].([]any)
		srcFilesStrs := make([]string, 0, len(srcFiles))
		for _, s := range srcFiles {
			if str, ok := s.(string); ok {
				srcFilesStrs = append(srcFilesStrs, str)
			}
		}
		newHyps = append(newHyps, schemas.Hypothesis{
			HID:                fmt.Sprintf("H%d", i+1),
			Statement:          statement,
			SupportingEvidence: supportingStrs,
			Confidence:         confidence,
			SourceFiles:        srcFilesStrs,
		})
	}

	state.Hypotheses = newHyps
	state.Iterations++

	trace := map[string]any{
		"agent":            "investigator",
		"iteration":        state.Iterations,
		"hypothesis_count": len(newHyps),
		"fetched_files":    keysOf(fetched),
	}
	state.AgentTrace = append(state.AgentTrace, trace)

	return nil
}

func (a *Investigator) buildUserMessage(state *schemas.AgentState, fetched map[string]string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
	parts = append(parts, fmt.Sprintf("# Severity: %s", state.Cluster.Severity))
	parts = append(parts, "")
	parts = append(parts, "## Anomaly Cluster Summary")
	parts = append(parts, state.Cluster.Summary)
	parts = append(parts, "")
	parts = append(parts, "## Anomalies")
	for i, anomaly := range state.Cluster.Anomalies {
		if i >= 8 {
			break
		}
		parts = append(parts, fmt.Sprintf("- [%s] score=%.2f: %s", anomaly.Detector, anomaly.Score, anomaly.Explanation))
		parts = append(parts, fmt.Sprintf("  dimensions: %v", anomaly.Dimensions))
	}
	parts = append(parts, "")
	if len(state.RetrievedIncidents) > 0 {
		parts = append(parts, "## Similar Past Incidents (from memory)")
		for i, s := range state.RetrievedIncidents {
			if i >= 5 {
				break
			}
			score, _ := s["semantic_score"].(float64)
			summary, _ := s["summary"].(string)
			rootCause, _ := s["root_cause"].(string)
			fixPattern, _ := s["fix_pattern"].(string)
			parts = append(parts, fmt.Sprintf("- score=%.2f: %s", score, summary))
			parts = append(parts, fmt.Sprintf("  root_cause: %s", rootCause))
			parts = append(parts, fmt.Sprintf("  fix: %s", fixPattern))
		}
		parts = append(parts, "")
	}
	if len(fetched) > 0 {
		parts = append(parts, "## Source Code (extracted from stack traces)")
		i := 0
		for path, content := range fetched {
			if i >= 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("### %s", path))
			parts = append(parts, "```")
			// Cap per-file size
			if len(content) > 4000 {
				content = content[:4000]
			}
			parts = append(parts, content)
			parts = append(parts, "```")
			i++
		}
		parts = append(parts, "")
	}
	parts = append(parts, "## Your Task")
	parts = append(parts, "Produce up to 5 root cause hypotheses. Be specific and testable.")
	return strings.Join(parts, "\n")
}

// fetchFilesForCluster fetches source files from GitHub by parsing stack
// traces from the cluster's anomalies + summary.
func (a *Investigator) fetchFilesForCluster(ctx context.Context, cluster schemas.AnomalyCluster, repo string) map[string]string {
	if repo == "" || a.gitHubToken == "" {
		return map[string]string{}
	}

	allText := cluster.Summary + "\n"
	for _, anomaly := range cluster.Anomalies {
		allText += anomaly.Explanation + "\n"
	}

	candidates := k8s.ParseStackTrace(allText)
	if len(candidates) == 0 {
		return map[string]string{}
	}

	out := map[string]string{}
	for i, path := range candidates {
		if i >= 10 {
			break
		}
		repoPath, content, err := fetchFileFromRepo(ctx, a.gitHubToken, repo, path)
		if err != nil || content == "" {
			continue
		}
		out[repoPath] = content
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

// fetchFileFromRepo fetches a single file from GitHub via the API.
// Tries progressively shorter paths to handle Docker WORKDIR prefixes.
// Returns (repo_path, content, error) where repo_path is the path that
// actually worked in the repo.
func fetchFileFromRepo(ctx context.Context, token, repo, path string) (string, string, error) {
	// Build list of paths to try: original, then strip one dir at a time
	pathsToTry := []string{path}
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		shorter := strings.Join(parts[i:], "/")
		if shorter != "" && shorter != pathsToTry[len(pathsToTry)-1] {
			pathsToTry = append(pathsToTry, shorter)
		}
	}

	for _, tryPath := range pathsToTry {
		content, err := githubGetFile(ctx, token, repo, tryPath)
		if err != nil || content == "" {
			continue
		}
		return tryPath, content, nil
	}
	return "", "", fmt.Errorf("file %s not found in %s", path, repo)
}

// githubGetFile calls the GitHub API to get a file's contents.
func githubGetFile(ctx context.Context, token, repo, path string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=HEAD", repo, path)
	body, err := githubAPIGet(ctx, token, url)
	if err != nil {
		return "", err
	}

	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Type == "dir" || resp.Content == "" {
		return "", fmt.Errorf("not a file")
	}
	if resp.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(resp.Content, "\n", ""))
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	return resp.Content, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// uuidNew is a convenience wrapper for agents that need fresh UUIDs.
func uuidNew() uuid.UUID {
	return uuid.New()
}
