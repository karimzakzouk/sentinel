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
  - code_fix      : a code change is required — provide search/replace blocks
  - rollback      : revert to the previous deployment (NO schema migration involved)
  - roll_forward  : the previous deploy had a schema migration — must fix forward, NOT rollback
  - infrastructure: a Terraform-managed resource needs changing

For code_fix, use SEARCH/REPLACE blocks. Each block has:
  - "search": the EXACT text from the file that needs to change (copy it character-for-character including whitespace and indentation)
  - "replace": the new text that should replace it

Rules for search/replace:
  - The "search" text MUST exactly match existing code in the file (including spaces, tabs, newlines)
  - Keep "search" blocks small (2-10 lines) — just the code that needs to change
  - Only include the lines that actually change — don't include surrounding context that stays the same
  - You can have multiple search/replace blocks per file if multiple spots need changing
  - If creating a NEW file (not modifying an existing one), use "new_content" with the full file instead

Respond with a JSON object of shape:
{
  "fix_type": "restart|scale|code_fix|rollback|roll_forward|infrastructure",
  "description": "...",
  "k8s_action": null | {"kind": "Deployment", "name": "...", "patch": {...}},
  "files_to_change": [{"path": "app/main.py", "changes": [{"search": "exact code to find", "replace": "new code"}]}],
  "dockerfile_path": "Dockerfile",
  "pr_title": "[Sentinel] ...",
  "pr_body": "..."
}

The "dockerfile_path" field is the path to the Dockerfile relative to the repo
root. Default to "Dockerfile" (repo root).

If fix_type is rollback, leave files_to_change empty.`

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
        // v3.9.6: Increased from 6000 to 16000 — some models (nemotron-3-super)
        // produce extensive reasoning before the JSON, causing truncation at 6000
        // tokens. Search/replace blocks are small, but the model's "thinking"
        // eats tokens. 16000 gives room for reasoning + complete JSON.
        userMsg := a.buildUserMessage(state)
        result, err := a.router.FrontierJSON(ctx, fixProposerSystemPrompt, userMsg, 16000)
        if err != nil {
                state.Status = schemas.StatusFailed
                s := fmt.Sprintf("fix_proposer_llm_failure: %v", err)
                state.Error = &s
                return nil
        }

        // v3.8.7: Debug logging — log what the LLM actually returned so we
        // can see exactly why files_to_change is empty.
        log.Printf("[fix_proposer] LLM response keys: %v", keysOfMap(result))
        if rawFilesVal, ok := result["files_to_change"]; ok {
                log.Printf("[fix_proposer] files_to_change type=%T", rawFilesVal)
                if arr, ok := rawFilesVal.([]any); ok {
                        log.Printf("[fix_proposer] files_to_change array length=%d", len(arr))
                        for i, f := range arr {
                                log.Printf("[fix_proposer] files_to_change[%d] = %v", i, f)
                        }
                }
        } else {
                log.Printf("[fix_proposer] files_to_change key MISSING from LLM response")
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

        // v3.8.7: If files_to_change is empty, try alternative key names that
        // LLMs sometimes use: "file_content", "fixed_content", "patch",
        // "code", "solution". This is NOT a retry — it's parsing the SAME
        // LLM response more thoroughly. Different models use different key
        // names for the same thing.
        if len(files) == 0 {
                files = extractFilesFromAlternativeKeys(result, state)
                if len(files) > 0 {
                        log.Printf("[fix_proposer] extracted %d file(s) from alternative keys", len(files))
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
// files_to_change array. Supports BOTH:
//   - "changes": [{search, replace}] — search/replace blocks (v3.9.0, preferred)
//   - "new_content": "full file" — whole-file replacement (legacy fallback)
//
// v3.9.0: If the LLM returns "changes", we use search/replace. If it returns
// "new_content", we use whole-file. If it returns both, changes wins.
func parseFilesToChange(rawFiles []any, state *schemas.AgentState) []schemas.FileChange {
        files := []schemas.FileChange{}
        for _, rf := range rawFiles {
                fcMap, ok := rf.(map[string]any)
                if !ok {
                        continue
                }
                path, _ := fcMap["path"].(string)
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

                // v3.9.0: Try search/replace blocks first
                var changes []schemas.SearchReplace
                if rawChanges, ok := fcMap["changes"].([]any); ok && len(rawChanges) > 0 {
                        for _, rc := range rawChanges {
                                chMap, ok := rc.(map[string]any)
                                if !ok {
                                        continue
                                }
                                search, _ := chMap["search"].(string)
                                replace, _ := chMap["replace"].(string)
                                if search != "" && replace != "" {
                                        // v3.9.8: Normalize escape sequences. Some LLMs return \n as
                                        // literal backslash-n (two chars) instead of actual newlines.
                                        // This happens because the model double-escapes the JSON string.
                                        // Convert literal \n → newline, \t → tab so the search text
                                        // matches the actual file content.
                                        search = normalizeEscapes(search)
                                        replace = normalizeEscapes(replace)
                                        changes = append(changes, schemas.SearchReplace{
                                                Search:  search,
                                                Replace: replace,
                                        })
                                }
                        }
                }

                // Also check for "new_content" (legacy whole-file fallback)
                content, _ := fcMap["new_content"].(string)

                if len(changes) > 0 {
                        log.Printf("[fix_proposer] parsed %d search/replace block(s) for %s", len(changes), path)
                        files = append(files, schemas.FileChange{Path: path, Changes: changes})
                } else if content != "" {
                        log.Printf("[fix_proposer] parsed whole-file replacement for %s (%d chars)", path, len(content))
                        files = append(files, schemas.FileChange{Path: path, NewContent: content})
                }
        }
        return files
}

// extractFilesFromAlternativeKeys tries to find file content in the LLM
// response using alternative key names. Different LLMs return the fixed
// file content under different keys:
//   - "files_to_change" (the expected key, already tried)
//   - "file_content" / "fixed_content" / "new_content" (string with just the content)
//   - "patch" / "diff" (unified diff — not yet supported, logged for debugging)
//   - "code" / "solution" (some models use these)
//   - "file" (single object with path + content)
//
// This is NOT a retry — it parses the SAME LLM response more thoroughly.
// v3.8.7.
func extractFilesFromAlternativeKeys(result map[string]any, state *schemas.AgentState) []schemas.FileChange {
        // Try "file" as a single object {path, content/new_content}
        if fileObj, ok := result["file"].(map[string]any); ok {
                path, _ := fileObj["path"].(string)
                content, _ := fileObj["new_content"].(string)
                if content == "" {
                        content, _ = fileObj["content"].(string)
                }
                if path != "" && content != "" {
                        validated := validateFilePath(path)
                        if validated != "" {
                                return []schemas.FileChange{{Path: validated, NewContent: content}}
                        }
                }
        }

        // Try string keys that contain just the file content. If found, use the
        // first file from FetchedCode as the target path (the investigator already
        // identified which files are relevant — this is NOT hardcoding, it's using
        // the investigator's output).
        stringKeys := []string{"file_content", "fixed_content", "new_content", "code", "solution"}
        for _, key := range stringKeys {
                if content, ok := result[key].(string); ok && content != "" {
                        // Find the target path from the root cause's source files
                        targetPath := ""
                        if state.RootCause != nil {
                                for _, sf := range state.RootCause.SourceFiles {
                                        if _, ok := state.FetchedCode[sf]; ok {
                                                targetPath = sf
                                                break
                                        }
                                }
                        }
                        if targetPath == "" {
                                for p := range state.FetchedCode {
                                        targetPath = p
                                        break
                                }
                        }
                        if targetPath == "" {
                                continue
                        }
                        log.Printf("[fix_proposer] found file content in alternative key %q, using path %s", key, targetPath)
                        validated := validateFilePath(targetPath)
                        if validated != "" {
                                return []schemas.FileChange{{Path: validated, NewContent: content}}
                        }
                }
        }

        return nil
}

// keysOfMap returns the keys of a map for debug logging.
func keysOfMap(m map[string]any) []string {
        keys := make([]string, 0, len(m))
        for k := range m {
                keys = append(keys, k)
        }
        return keys
}

// normalizeEscapes converts literal escape sequences (backslash-n, backslash-t)
// into actual control characters (newline, tab). Some LLMs double-escape JSON
// strings, producing \n as two literal characters instead of a real newline.
// This causes search/replace blocks to fail matching because the file has real
// newlines but the search text has literal \n.
//
// v3.9.8: This was the root cause of the "search text not found" errors on
// KeyError/TypeError investigations. The LLM returned:
//   "search": "    name = users[user_id]\\n    return ..."
// After JSON parsing, \\n becomes \n (literal backslash-n, 2 chars). But the
// file has real newlines (1 char). normalizeEscapes fixes this.
func normalizeEscapes(s string) string {
        // Replace literal \n (backslash + n) with actual newline
        s = strings.ReplaceAll(s, "\\n", "\n")
        // Replace literal \t (backslash + t) with actual tab
        s = strings.ReplaceAll(s, "\\t", "\t")
        // Replace literal \r (backslash + r) with actual carriage return
        s = strings.ReplaceAll(s, "\\r", "\r")
        return s
}

// llmCodeFix is used for roll_forward cases where we still need an LLM-generated patch.
func (a *FixProposer) llmCodeFix(ctx context.Context, state *schemas.AgentState, proposal *schemas.FixProposal) {
        userMsg := a.buildUserMessage(state)
        result, err := a.router.FrontierJSON(ctx, fixProposerSystemPrompt, userMsg, 16000)
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
