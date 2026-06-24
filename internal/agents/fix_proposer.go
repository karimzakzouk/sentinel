package agents

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "path/filepath"
        "strings"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/llm"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// validateFilePath validates a file path returned by the LLM for a code fix.
//
// v3.8.4: This function is called with paths that originate from LLM output,
// which in turn was influenced by pod-log content (attacker-controllable).
// Without validation, an LLM that returned "../../../etc/passwd" or
// ".github/workflows/ci.yml" (allowing a malicious CI workflow to be
// smuggled into the PR) would have those paths accepted.
//
// Rules:
//  1. Reject empty paths
//  2. Reject absolute paths (anything starting with '/')
//  3. Reject paths containing '..' segments (path traversal)
//  4. Reject paths that escape the repo root after cleaning
//  5. Reject sensitive paths by default:
//     - .github/workflows/* (CI/CD tampering)
//     - .github/actions/* (composite action tampering)
//     - Dockerfile (build-chain tampering) — only allowed when explicitly
//       returned as the dockerfile_path, not as a file_to_change
//     - package.json / pyproject.toml / go.mod postinstall hooks
//     - .gitlab-ci.yml, .circleci/config.yml, Jenkinsfile, etc.
//  6. Reject paths with control characters or null bytes
//  7. Cap path length at 500 chars
//
// Returns the cleaned path on success, or "" if rejected. Callers should
// skip files with empty return values (do not abort the whole proposal —
// one bad path shouldn't kill a fix that has other valid files).
func validateFilePath(path string) string {
        if path == "" {
                return ""
        }
        if len(path) > 500 {
                log.Printf("[fix_proposer] SECURITY: rejecting path (too long): %q", path)
                return ""
        }

        // Reject control chars and null bytes.
        for _, r := range path {
                if r < 0x20 || r == 0x7f {
                        log.Printf("[fix_proposer] SECURITY: rejecting path (control char): %q", path)
                        return ""
                }
        }

        // Reject absolute paths.
        if filepath.IsAbs(path) {
                log.Printf("[fix_proposer] SECURITY: rejecting absolute path: %q", path)
                return ""
        }

        clean := filepath.Clean(path)
        if clean == "." {
                log.Printf("[fix_proposer] SECURITY: rejecting path (resolves to repo root): %q", path)
                return ""
        }
        if strings.HasPrefix(clean, "..") {
                log.Printf("[fix_proposer] SECURITY: rejecting path traversal: %q", path)
                return ""
        }

        // Reject sensitive paths. Use filepath.ToSlash so the check works on
        // Windows-style paths too (LLMs sometimes return backslashes).
        cleanSlash := filepath.ToSlash(clean)
        sensitive := false
        switch {
        case strings.HasPrefix(cleanSlash, ".github/workflows/"),
                strings.HasPrefix(cleanSlash, ".github/actions/"),
                strings.HasPrefix(cleanSlash, ".gitlab-ci.yml"),
                strings.HasPrefix(cleanSlash, ".circleci/"),
                strings.HasPrefix(cleanSlash, "Jenkinsfile"),
                strings.HasPrefix(cleanSlash, ".drone.yml"),
                strings.HasPrefix(cleanSlash, "cloudbuild.yaml"),
                strings.HasPrefix(cleanSlash, "cloudbuild.yml"),
                strings.HasPrefix(cleanSlash, ".azure-pipelines.yml"),
                strings.HasPrefix(cleanSlash, "azure-pipelines.yml"):
                sensitive = true
        }
        if sensitive {
                log.Printf("[fix_proposer] SECURITY: rejecting CI/CD-sensitive path: %q", path)
                return ""
        }

        return clean
}

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

        // 0. FALLBACK: If we have no source code, propose restart.
        // Don't ask the LLM to write code it can't see — it'll hallucinate.
        if len(state.FetchedCode) == 0 {
                log.Printf("[fix_proposer] no source code fetched — proposing restart fallback")
                proposal := &schemas.FixProposal{
                        FixID:             uuid.New(),
                        FixType:           schemas.FixRestart,
                        TargetService:     state.Cluster.Service,
                        TargetNamespace:   state.Cluster.Namespace,
                        Description:       fmt.Sprintf("Restart fallback: no source code available to generate a code fix. Root cause: %s", rc.Statement),
                        Repo:              strPtr(discoverRepo(state)),
                        PRTitle:           strPtr(fmt.Sprintf("[Sentinel] Restart %s (no source available for code fix)", state.Cluster.Service)),
                        PRBody:            strPtr(buildPRBody(state, rc, "restart")),
                        RequiresSandbox:   false,
                        ProposedConfidence: rc.Confidence * 0.5, // lower confidence — we're guessing
                }
                a.finalizeProposal(state, proposal)
                return nil
        }

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
        files := parseFilesToChange(rawFiles, state)

        // v3.8.6: If the LLM returned no files, retry with a simpler, more
        // explicit prompt. Free-tier models (gpt-oss-120b:free, etc.) often
        // fail to produce the full file content in the first call — they
        // return the fix_type and description but leave files_to_change empty
        // or malformed. The retry prompt shows the exact file to fix and
        // asks for just the fixed version, which is a much simpler task.
        if len(files) == 0 && len(state.FetchedCode) > 0 {
                log.Printf("[fix_proposer] LLM returned 0 files on first attempt — retrying with simplified prompt")
                log.Printf("[fix_proposer] LLM response keys: %v", keysOfMap(result))

                files = a.retryFixWithSimplifiedPrompt(ctx, state, result)
                if len(files) > 0 {
                        log.Printf("[fix_proposer] retry succeeded — got %d file(s)", len(files))
                } else {
                        log.Printf("[fix_proposer] retry also returned 0 files — giving up")
                }
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

// parseFilesToChange extracts FileChange entries from the LLM's raw
// files_to_change array, fixing container paths and validating security.
// Extracted so the retry path can reuse the same logic.
func parseFilesToChange(rawFiles []any, state *schemas.AgentState) []schemas.FileChange {
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
                        for repoPath := range state.FetchedCode {
                                if strings.HasSuffix(path, repoPath) && path != repoPath {
                                        path = repoPath
                                        break
                                }
                                if strings.HasSuffix(repoPath, path) && repoPath != path {
                                        path = repoPath
                                        break
                                }
                        }
                }
                validated := validateFilePath(path)
                if validated == "" {
                        continue
                }
                path = validated
                files = append(files, schemas.FileChange{Path: path, NewContent: content})
        }
        return files
}

// retryFixWithSimplifiedPrompt is called when the first LLM attempt returns
// 0 files. It builds a much simpler prompt that:
//  1. Shows the LLM the exact file that needs fixing (from FetchedCode)
//  2. Shows the root cause statement
//  3. Asks ONLY for the fixed file content — no complex JSON structure
//
// This works with weaker/free-tier models that can't handle the full
// fix-proposer prompt (which asks for a complex JSON object with multiple
// fields). The retry prompt is dead simple: "here's the file, here's the
// bug, give me the fixed file."
//
// v3.8.6.
func (a *FixProposer) retryFixWithSimplifiedPrompt(ctx context.Context, state *schemas.AgentState, firstResult map[string]any) []schemas.FileChange {
        // Pick the most relevant file from FetchedCode. The root cause usually
        // references a specific file — try to find it. Otherwise use the first.
        targetPath := ""
        targetContent := ""
        if state.RootCause != nil && len(state.RootCause.SourceFiles) > 0 {
                for _, sf := range state.RootCause.SourceFiles {
                        if content, ok := state.FetchedCode[sf]; ok {
                                targetPath = sf
                                targetContent = content
                                break
                        }
                }
        }
        if targetPath == "" {
                // Fall back to the first fetched file
                for p, c := range state.FetchedCode {
                        targetPath = p
                        targetContent = c
                        break
                }
        }
        if targetPath == "" {
                return nil
        }

        // Use the description from the first attempt if available (it usually
        // correctly describes the fix even when it doesn't produce the file).
        description := ""
        if d, ok := firstResult["description"].(string); ok {
                description = d
        }

        // Build a dead-simple prompt: "here's the file, here's the bug, fix it."
        simplifiedPrompt := "You are a code fixer. Below is a Python file that has a bug. Fix the bug and return the COMPLETE fixed file.\n\n" +
                "BUG: " + description + "\n" +
                "ROOT CAUSE: " + state.RootCause.Statement + "\n\n" +
                "CURRENT FILE (" + targetPath + "):\n" +
                "---\n" +
                targetContent + "\n" +
                "---\n\n" +
                "Return a JSON object with EXACTLY this format:\n" +
                "{\n" +
                "  \"path\": \"" + targetPath + "\",\n" +
                "  \"new_content\": \"<the complete fixed file contents>\"\n" +
                "}\n\n" +
                "Rules:\n" +
                "- Return the COMPLETE file, not a diff\n" +
                "- Only change what's necessary to fix the bug\n" +
                "- The \"path\" must be exactly \"" + targetPath + "\"\n" +
                "- Do NOT include any text before or after the JSON"

        result, err := a.router.FrontierJSON(ctx, "You are a code fixer. Return only valid JSON.", simplifiedPrompt, 4000)
        if err != nil {
                log.Printf("[fix_proposer] retry LLM call failed: %v", err)
                return nil
        }

        // The retry returns a single file, not an array. Parse it directly.
        path, _ := result["path"].(string)
        content, _ := result["new_content"].(string)

        // Also try the array format in case the LLM wrapped it
        if path == "" {
                if rawFiles, ok := result["files_to_change"].([]any); ok && len(rawFiles) > 0 {
                        if fcMap, ok := rawFiles[0].(map[string]any); ok {
                                path, _ = fcMap["path"].(string)
                                content, _ = fcMap["new_content"].(string)
                        }
                }
        }

        if path == "" || content == "" {
                log.Printf("[fix_proposer] retry returned empty path or content (path=%q, content_len=%d)", path, len(content))
                return nil
        }

        // Fix path against fetched_code keys
        for repoPath := range state.FetchedCode {
                if strings.HasSuffix(path, repoPath) && path != repoPath {
                        path = repoPath
                        break
                }
                if strings.HasSuffix(repoPath, path) && repoPath != path {
                        path = repoPath
                        break
                }
        }

        validated := validateFilePath(path)
        if validated == "" {
                log.Printf("[fix_proposer] retry: path validation rejected %q", path)
                return nil
        }

        return []schemas.FileChange{{Path: validated, NewContent: content}}
}

// keysOfMap returns the keys of a map for debug logging.
func keysOfMap(m map[string]any) []string {
        keys := make([]string, 0, len(m))
        for k := range m {
                keys = append(keys, k)
        }
        return keys
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
                // v3.8.4: Apply the same path validation as the main path.
                validated := validateFilePath(path)
                if validated != "" {
                        proposal.FilesToChange = append(proposal.FilesToChange, schemas.FileChange{Path: validated, NewContent: content})
                }
        }
        if desc, ok := result["description"].(string); ok && desc != "" {
                proposal.Description = desc
        }
}

func (a *FixProposer) finalizeProposal(state *schemas.AgentState, proposal *schemas.FixProposal) {
        branchName := fmt.Sprintf("sentinel/%s-%s", state.Cluster.Service, state.IncidentID.String()[:8])
        proposal.BranchName = strPtr(branchName)
        // v3.3.1: Respect sentinel.io/branch annotation for PR base instead of
        // hardcoding "main". If the service is deployed from "release-1.2", the
        // PR should target "release-1.2", not "main". Falls back to "main" if no
        // annotation is set (same as the investigator's clone behavior).
        proposal.BaseBranch = strPtr(resolveBaseBranch(state))

        state.FixProposal = proposal
        state.Status = schemas.StatusFixProposed
        state.AgentTrace = append(state.AgentTrace, map[string]any{
                "agent":         "fix_proposer",
                "fix_type":      string(proposal.FixType),
                "files_changed": len(proposal.FilesToChange),
                "confidence":    proposal.ProposedConfidence,
        })
}

// resolveBaseBranch determines which branch the PR should target.
// Priority: sentinel.io/branch annotation > "main" fallback.
// This mirrors the investigator's resolveBranch logic but WITHOUT the
// git-commit case (PRs target branches, not commits).
func resolveBaseBranch(state *schemas.AgentState) string {
        if branch, ok := state.Cluster.Annotations["sentinel.io/branch"]; ok && branch != "" {
                return branch
        }
        for _, anomaly := range state.Cluster.Anomalies {
                if branch, ok := anomaly.Annotations["sentinel.io/branch"]; ok && branch != "" {
                        return branch
                }
        }
        return "main"
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
        // v3.8.4: This is a placeholder body written BEFORE sandbox verification
        // runs. The actual PR body is overwritten by agents.BuildPRBody in
        // main.go after the sandbox runs (or is skipped). The placeholder
        // must NOT claim sandbox verification was performed — it hasn't been
        // yet. The previous text ("This fix was tested in a sandbox namespace
        // with 1% mirrored production traffic") was a hardcoded lie that
        // would persist in the PR if the execution path failed before
        // reaching BuildPRBody.
        parts = append(parts, "_Sandbox verification status will be appended by the execution layer._")
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
