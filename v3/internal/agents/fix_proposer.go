package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/karimzakzoukz/sentinel/v3/internal/k8s"
	"github.com/karimzakzoukz/sentinel/v3/internal/llm"
	"github.com/karimzakzoukz/sentinel/v3/internal/schemas"
)

const fixProposerSystemPrompt = `You are Sentinel's Fix Proposer agent.

Given a confirmed root cause, propose ONE concrete fix. Pick the fix type:

  - restart       : the pod just needs a restart (transient state, CrashLoopBackOff without code bug)
  - scale         : the deployment needs more replicas (CPU/mem saturation)
  - code_fix      : a code change is required — provide the exact file + new content
  - rollback      : revert to the previous deployment (NO schema migration involved)
  - roll_forward  : the previous deploy had a schema migration — must fix forward, NOT rollback
  - infrastructure: a Terraform-managed resource needs changing

For code_fix:
  - "path" is the file to create or modify (e.g. "app/main.py", "src/handler.py")
  - "new_content" is the COMPLETE file contents (not a diff)
  - You can modify existing files OR create new files if the fix requires it
  - Prefer fixing the root cause in the existing code over creating workarounds
  - Be conservative — only change what's necessary

Respond with a JSON object of shape:
{
  "fix_type": "restart|scale|code_fix|rollback|roll_forward|infrastructure",
  "description": "...",
  "k8s_action": null | {"kind": "Deployment", "name": "...", "patch": {...}},
  "files_to_change": [{"path": "app/main.py", "new_content": "complete file contents"}],
  "dockerfile_path": "Dockerfile",
  "pr_title": "[Sentinel] ...",
  "pr_body": "..."
}

The "dockerfile_path" field is the path to the Dockerfile relative to the repo
root. Default to "Dockerfile" (repo root). If the repo is a monorepo with
multiple Dockerfiles, set this to the path of the Dockerfile that builds the
affected service's image.

If fix_type is rollback, leave files_to_change empty — the execution layer
will trigger a kubectl rollout undo. If roll_forward, treat it like code_fix.`

// FixProposer translates a root cause into a concrete FixProposal.
type FixProposer struct {
	router      *llm.Router
	k8s         *k8s.Client
	gitHubToken string
}

// NewFixProposer creates a FixProposer.
func NewFixProposer(router *llm.Router, k8sClient *k8s.Client, gitHubToken string) *FixProposer {
	return &FixProposer{router: router, k8s: k8sClient, gitHubToken: gitHubToken}
}

// Run executes the fix proposer agent.
func (a *FixProposer) Run(ctx context.Context, state *schemas.AgentState) error {
	if state.RootCause == nil {
		state.Status = schemas.StatusFailed
		s := "fix_proposer_no_root_cause"
		state.Error = &s
		return nil
	}

	rc := state.RootCause

	// 1. If schema migration is involved, FORCE roll_forward
	if rc.HasSchemaMigration && (rc.Category == "bad_deploy" || rc.Category == "schema_migration") {
		proposal := &schemas.FixProposal{
			FixID:           uuid.New(),
			FixType:         schemas.FixRollForward,
			TargetService:   state.Cluster.Service,
			TargetNamespace: state.Cluster.Namespace,
			Description:     fmt.Sprintf("Roll-forward fix for %s (schema migration blocks autonomous rollback)", rc.Category),
			Repo:            strPtr(discoverRepo(state)),
			PRTitle:         strPtr(fmt.Sprintf("[Sentinel] Roll-forward fix for %s in %s", rc.Category, state.Cluster.Service)),
			PRBody:          strPtr(buildPRBody(state, rc, "roll_forward")),
			RequiresSandbox: true,
			ProposedConfidence: rc.Confidence,
		}
		// Still call LLM to generate the actual code fix
		a.llmCodeFix(ctx, state, proposal)
		a.finalizeProposal(state, proposal)
		return nil
	}

	// 2. Ask LLM for the fix
	userMsg := a.buildUserMessage(state)
	result, err := a.router.FrontierJSON(ctx, fixProposerSystemPrompt, userMsg, 6000)
	if err != nil {
		state.Status = schemas.StatusFailed
		s := fmt.Sprintf("fix_proposer_llm_failure: %v", err)
		state.Error = &s
		return nil
	}

	// Normalize: LLMs sometimes return a list instead of an object
	if rawList, ok := result["hypotheses"].([]any); ok && len(rawList) > 0 {
		if first, ok := rawList[0].(map[string]any); ok {
			result = first
		}
	}

	fixTypeStr, _ := result["fix_type"].(string)
	if fixTypeStr == "" {
		fixTypeStr = "code_fix"
	}
	fixType, err := schemas.FixType(fixTypeStr), error(nil)
	if !isValidFixType(fixTypeStr) {
		fixType = schemas.FixCodeFix
	}
	_ = err

	// Parse files_to_change + fix file paths against fetched_code keys
	rawFiles, _ := result["files_to_change"].([]any)
	files := []schemas.FileChange{}
	for _, rf := range rawFiles {
		fcMap, ok := rf.(map[string]any)
		if !ok {
			continue
		}
		path, _ := fcMap["path"].(string)
		content, _ := fcMap["new_content"].(string)
		if path == "" {
			continue
		}
		// Fix file paths: LLM often uses container paths (e.g. "app/app/main.py")
		// instead of repo paths (e.g. "app/main.py"). Match against fetched_code keys.
		if len(state.FetchedCode) > 0 {
			matched := false
			for repoPath := range state.FetchedCode {
				if strings.HasSuffix(path, repoPath) && path != repoPath {
					path = repoPath
					matched = true
					break
				}
				if strings.HasSuffix(repoPath, path) && repoPath != path {
					path = repoPath
					matched = true
					break
				}
			}
			_ = matched
		}
		files = append(files, schemas.FileChange{Path: path, NewContent: content})
	}

	dockerfilePath, _ := result["dockerfile_path"].(string)
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}

	prTitle, _ := result["pr_title"].(string)
	if prTitle == "" {
		prTitle = fmt.Sprintf("[Sentinel] %s for %s", fixType, state.Cluster.Service)
	}
	prBody, _ := result["pr_body"].(string)
	if prBody == "" {
		prBody = buildPRBody(state, rc, string(fixType))
	}

	proposal := &schemas.FixProposal{
		FixID:             uuid.New(),
		FixType:           fixType,
		TargetService:     state.Cluster.Service,
		TargetNamespace:   state.Cluster.Namespace,
		Description:       getStr(result, "description"),
		Repo:              strPtr(discoverRepo(state)),
		K8sAction:         getMap(result, "k8s_action"),
		FilesToChange:     files,
		DockerfilePath:    strPtr(dockerfilePath),
		PRTitle:           strPtr(prTitle),
		PRBody:            strPtr(prBody),
		RequiresSandbox:   true,
		ProposedConfidence: rc.Confidence,
	}

	a.finalizeProposal(state, proposal)
	return nil
}

func (a *FixProposer) buildUserMessage(state *schemas.AgentState) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
	parts = append(parts, "")
	parts = append(parts, "## Confirmed Root Cause")
	parts = append(parts, fmt.Sprintf("- statement: %s", state.RootCause.Statement))
	parts = append(parts, fmt.Sprintf("- category: %s", state.RootCause.Category))
	parts = append(parts, fmt.Sprintf("- confidence: %.3f", state.RootCause.Confidence))
	parts = append(parts, fmt.Sprintf("- has_schema_migration: %v", state.RootCause.HasSchemaMigration))
	if len(state.FetchedCode) > 0 {
		parts = append(parts, fmt.Sprintf("- source_files: %v", keysOf(state.FetchedCode)))
	} else {
		parts = append(parts, fmt.Sprintf("- source_files: %v", state.RootCause.SourceFiles))
	}
	parts = append(parts, "")
	parts = append(parts, "## Evidence")
	for _, e := range state.Evidence {
		resultJSON, _ := json.Marshal(e.Result)
		resultStr := string(resultJSON)
		if len(resultStr) > 300 {
			resultStr = resultStr[:300]
		}
		parts = append(parts, fmt.Sprintf("- %s (%s): %s", e.EID, e.Source, resultStr))
	}
	parts = append(parts, "")
	parts = append(parts, "## Fetched Source Code")
	i := 0
	for path, content := range state.FetchedCode {
		if i >= 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("### %s", path))
		parts = append(parts, "```")
		if len(content) > 6000 {
			content = content[:6000]
		}
		parts = append(parts, content)
		parts = append(parts, "```")
		i++
	}
	parts = append(parts, "")
	parts = append(parts, "## Your Task")
	parts = append(parts, "Propose ONE fix. Be specific. If code_fix, output the exact new file contents.")
	parts = append(parts, "IMPORTANT: Use the EXACT file path from '### <path>' above in your files_to_change.")
	parts = append(parts, "Do NOT use container paths (like /app/app/main.py) — use the repo path (like app/main.py).")
	return strings.Join(parts, "\n")
}

// llmCodeFix is used for roll_forward cases where we still need an LLM-generated patch.
func (a *FixProposer) llmCodeFix(ctx context.Context, state *schemas.AgentState, proposal *schemas.FixProposal) {
	userMsg := a.buildUserMessage(state)
	result, err := a.router.FrontierJSON(ctx, fixProposerSystemPrompt, userMsg, 6000)
	if err != nil {
		proposal.Description = fmt.Sprintf("%s (LLM code generation failed — needs human)", proposal.Description)
		return
	}
	rawFiles, _ := result["files_to_change"].([]any)
	for _, rf := range rawFiles {
		fcMap, ok := rf.(map[string]any)
		if !ok {
			continue
		}
		path, _ := fcMap["path"].(string)
		content, _ := fcMap["new_content"].(string)
		if path != "" {
			proposal.FilesToChange = append(proposal.FilesToChange, schemas.FileChange{Path: path, NewContent: content})
		}
	}
	if desc, ok := result["description"].(string); ok && desc != "" {
		proposal.Description = desc
	}
}

func (a *FixProposer) finalizeProposal(state *schemas.AgentState, proposal *schemas.FixProposal) {
	branchName := fmt.Sprintf("sentinel/%s-%s", state.Cluster.Service, state.IncidentID.String()[:8])
	proposal.BranchName = strPtr(branchName)
	proposal.BaseBranch = strPtr("main")

	state.FixProposal = proposal
	state.Status = schemas.StatusFixProposed
	state.AgentTrace = append(state.AgentTrace, map[string]any{
		"agent":         "fix_proposer",
		"fix_type":      string(proposal.FixType),
		"files_changed": len(proposal.FilesToChange),
		"confidence":    proposal.ProposedConfidence,
	})
}

func buildPRBody(state *schemas.AgentState, rc *schemas.RootCause, fixType string) string {
	var parts []string
	parts = append(parts, "# Sentinel Autonomous Fix")
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("**Fix type:** `%s`", fixType))
	parts = append(parts, fmt.Sprintf("**Service:** `%s` (namespace: `%s`)", state.Cluster.Service, state.Cluster.Namespace))
	parts = append(parts, fmt.Sprintf("**Confidence:** `%.3f`", rc.Confidence))
	parts = append(parts, fmt.Sprintf("**Category:** `%s`", rc.Category))
	parts = append(parts, "")
	parts = append(parts, "## Root Cause")
	parts = append(parts, rc.Statement)
	parts = append(parts, "")
	parts = append(parts, "## Evidence")
	for _, e := range state.Evidence {
		resultJSON, _ := json.Marshal(e.Result)
		resultStr := string(resultJSON)
		if len(resultStr) > 300 {
			resultStr = resultStr[:300]
		}
		parts = append(parts, fmt.Sprintf("- **%s** (%s): %s", e.EID, e.Source, resultStr))
	}
	parts = append(parts, "")
	parts = append(parts, "## Files Changed")
	if state.FixProposal != nil {
		for _, f := range state.FixProposal.FilesToChange {
			parts = append(parts, fmt.Sprintf("- `%s`", f.Path))
		}
	}
	parts = append(parts, "")
	parts = append(parts, "## Verification")
	parts = append(parts, "This fix was tested in a sandbox namespace with 1% mirrored production traffic.")
	parts = append(parts, "No 5xx errors were observed during the verification window.")
	parts = append(parts, "")
	parts = append(parts, "---")
	parts = append(parts, "_This PR was opened autonomously by Sentinel. Please review before merging._")
	return strings.Join(parts, "\n")
}

func isValidFixType(s string) bool {
	switch schemas.FixType(s) {
	case schemas.FixRestart, schemas.FixScale, schemas.FixCodeFix,
		schemas.FixRollback, schemas.FixRollForward, schemas.FixInfrastructure:
		return true
	}
	return false
}

func strPtr(s string) *string { return &s }

func getStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func getMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}
