package agents

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "path/filepath"
        "strings"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/internal/k8s"
        "github.com/karimzakzouk/sentinel/internal/llm"
        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// validateFilePath validates a file path returned by the LLM for a code fix.
// which in turn was influenced by pod-log content (attacker-controllable).
// Without validation, an LLM that returned "../../../etc/passwd" or
// ".github/workflows/ci.yml" (allowing a malicious CI workflow to be
// smuggled into the PR) would have those paths accepted.
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
        sensitiveReason := ""
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
                sensitiveReason = "CI/CD-sensitive path"

        // always claimed these were blocked, but the switch missed them).
        // These let an LLM (potentially prompt-injected via pod logs) add
        // backdoor RUN commands or postinstall hooks.

        case cleanSlash == "Dockerfile" || strings.HasPrefix(cleanSlash, "Dockerfile."),
                strings.HasPrefix(cleanSlash, "docker-compose.yml"),
                strings.HasPrefix(cleanSlash, "docker-compose.yaml"),
                cleanSlash == "package.json" || cleanSlash == "package-lock.json",
                cleanSlash == "pyproject.toml" || cleanSlash == "setup.py" || cleanSlash == "requirements.txt" || cleanSlash == "Pipfile" || cleanSlash == "Pipfile.lock",
                cleanSlash == "go.mod" || cleanSlash == "go.sum",
                cleanSlash == ".npmrc" || cleanSlash == ".pypirc" || cleanSlash == ".netrc",
                cleanSlash == "Makefile" || strings.HasSuffix(cleanSlash, ".mk"),
                strings.HasSuffix(cleanSlash, ".sh"),
                strings.HasPrefix(cleanSlash, ".env") && cleanSlash != ".env.example":
                sensitive = true
                sensitiveReason = "build-chain-sensitive path"
        }
        if sensitive {
                log.Printf("[fix_proposer] SECURITY: rejecting %s: %q", sensitiveReason, path)
                return ""
        }

        return clean
}

const fixProposerSystemPrompt = `You are Sentinel's Fix Proposer agent. You write production code fixes for runtime errors.

Given a confirmed root cause and the source code, propose ONE fix using SEARCH/REPLACE blocks.

## Fix Quality Requirements
- Fix the ROOT CAUSE, not just the symptom. If a collection access crashes (dict/key lookup, array index, map get), add a proper check — don't just wrap it in try/catch.
- Use proper error handling for the framework + language in use. For web APIs (FastAPI/Flask in Python, Express/Koa in JS/TS, net/http or chi/gin in Go, Spring in Java, Actix in Rust, etc.):
  - Bad input (missing key, invalid type, out of range) → return appropriate HTTP error (400, 404, 422)
  - Use the framework's error response mechanism (e.g. raise HTTPException in FastAPI, abort() in Flask, c.JSON(status, err) in gin, ResponseEntity.status(status).body(err) in Spring)
  - NEVER return error states with HTTP 200 — callers should know something went wrong from the status code
- For non-web code (CLIs, workers, daemons), use the language's idiomatic error pattern (Python: raise ValueError; Go: fmt.Errorf + return; JS: throw new Error; Java: throw new IllegalArgumentException; Rust: Result::Err).
- Minimal changes. Only modify what's broken. Don't refactor surrounding code.
- The fix must be syntactically valid in the target language (the detected language is included in the context).

## CRITICAL: Replacement Completeness
After your search/replace block is applied, the resulting code must be complete and correct.
Before submitting, mentally apply your replacement to the search text and verify:
- Every variable used after the replaced block is assigned on ALL code paths (not just inside an if/try block)
- Control flow is complete — if you add a guard clause (if/raise, if/return, if/throw), the code AFTER the guard must still execute correctly when the guard condition is FALSE
- Indentation of your replacement matches the surrounding code — the replaced text must fit seamlessly at the same nesting level
- The function still returns/exits correctly on all paths — no code becomes unreachable, no variables become undefined
- If you add an early return/raise/throw, move any code that should still execute ABOVE or AFTER the guard, not inside it

## Root Cause Description
- Be precise. State WHAT is broken and WHERE.
- Do NOT speculate about infrastructure, databases, or configuration unless the error traceback explicitly references them.
- If the error is a missing-key access (e.g. "KeyError: 99", "NullPointerException", "panic: index out of range") at "users[user_id]", the root cause is "the collection doesn't user_id 99" — not "the database doesn't have the user".

## Output Format

Output TWO sections:

### Section 1: Metadata (JSON, small)
Output a JSON object with the fix metadata. Keep it short.
{"fix_type":"code_fix","description":"Brief description","pr_title":"[Sentinel] Brief title","pr_body":"Short summary"}

### Section 2: Code Fix (SEARCH/REPLACE blocks)

Use this exact format for each file you want to change:

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400, detail="Divisor cannot be zero")
    result = a / b
>>>>>>> REPLACE

Rules:
- The SEARCH section MUST match the file exactly (whitespace, indentation, comments — everything)
- Copy the search text character-for-character from the source code provided above
- Keep blocks small (2-10 lines) — only the code that needs to change
- Multiple blocks per file are OK if multiple spots need fixing
- The file path goes on the line BEFORE <<<<<<< SEARCH
- Do NOT wrap the SEARCH/REPLACE blocks in JSON or code fences

## Example Output

{"fix_type":"code_fix","description":"Add zero check before division","pr_title":"[Sentinel] Fix ZeroDivisionError in /divide","pr_body":"Added a guard clause to return 400 when b=0."}

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400, detail="Divisor cannot be zero")
    result = a / b
>>>>>>> REPLACE`

// FixProposer translates a root cause into a concrete FixProposal.

type FixProposer struct {
        router      *llm.Router
        k8s         *k8s.Client
        gitHubToken string

        // "github.com" in branch_resolver.normalizeRepo. Set via SetGitHost.

        gitHost       string
        defaultBranch string
}

// NewFixProposer creates a FixProposer.

func NewFixProposer(router *llm.Router, k8sClient *k8s.Client, gitHubToken string) *FixProposer {
        return &FixProposer{
                router:        router,
                k8s:           k8sClient,
                gitHubToken:   gitHubToken,
                gitHost:       "github.com",
                defaultBranch: "main",
        }
}

// SetGitHost sets the git host for repo normalization. v3.11.0: Replaces
// the hardcoded "github.com" in normalizeRepo.

func (a *FixProposer) SetGitHost(host string) {
        if host != "" {
                a.gitHost = host
        }
}

// SetDefaultBranch sets the default branch fallback. v3.11.0: Replaces the
// hardcoded "main" in resolveBaseBranch.

func (a *FixProposer) SetDefaultBranch(branch string) {
        if branch != "" {
                a.defaultBranch = branch
        }
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
                        Repo:              strPtr(discoverRepo(state, a.gitHost)),
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
                        Repo:            strPtr(discoverRepo(state, a.gitHost)),
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

        // 2. Ask LLM for the fix — with adaptive retry loop.
        // The LLM outputs metadata JSON (small) + SEARCH/REPLACE blocks (plain text).
        // We parse the blocks, apply them with layered matching (exact → fuzzy),
        // and feed actionable errors back on failure (Aider's pattern).

        userMsg := a.buildUserMessage(state)
        var files []schemas.FileChange
        var fixType schemas.FixType
        var dockerfilePath, prTitle, prBody, description string
        var lastError string

        maxRetries := 3
        for attempt := 0; attempt <= maxRetries; attempt++ {
                var retryHint string
                if attempt > 0 {
                        retryHint = fmt.Sprintf("\n\n## PREVIOUS ATTEMPT FAILED\n%s\n\nPlease try again. Fix the issue above and return a corrected response.", lastError)
                        userMsg = a.buildUserMessage(state) + retryHint
                }

                attemptCtx, attemptCancel := context.WithTimeout(ctx, 120*time.Second)
                log.Printf("[fix_proposer] attempt %d/%d (SEARCH/REPLACE format)", attempt+1, maxRetries+1)
                rawOutput, llmErr := a.router.FrontierText(attemptCtx, fixProposerSystemPrompt, userMsg, 8000)
                attemptCancel()
                if llmErr != nil {
                        lastError = fmt.Sprintf("LLM call failed: %v", llmErr)
                        log.Printf("[fix_proposer] attempt %d LLM error: %v", attempt+1, llmErr)
                        continue
                }

                log.Printf("[fix_proposer] attempt %d: got %d chars of output", attempt+1, len(rawOutput))

                // Parse metadata JSON from the first line(s)
                // The LLM outputs {"fix_type":"code_fix",...} before the SEARCH/REPLACE blocks

                metadata := extractMetadataJSON(rawOutput)
                fixTypeStr, _ := metadata["fix_type"].(string)
                if fixTypeStr == "" {
                        fixTypeStr = "code_fix"
                }
                fixType = schemas.FixType(fixTypeStr)
                if !isValidFixType(fixTypeStr) {
                        fixType = schemas.FixCodeFix
                }
                dockerfilePath, _ = metadata["dockerfile_path"].(string)
                if dockerfilePath == "" {
                        dockerfilePath = "Dockerfile"
                }
                prTitle, _ = metadata["pr_title"].(string)
                if prTitle == "" {
                        prTitle = fmt.Sprintf("[Sentinel] %s for %s", fixType, state.Cluster.Service)
                }
                prBody, _ = metadata["pr_body"].(string)
                if prBody == "" {
                        prBody = buildPRBody(state, rc, string(fixType))
                }
                description, _ = metadata["description"].(string)
                if description == "" {
                        description = fmt.Sprintf("Fix %s in %s", fixType, state.Cluster.Service)
                }

                // Parse SEARCH/REPLACE blocks from the output

                blocks := ParseEditBlocks(rawOutput)

                if len(blocks) > 0 {
                        // We have SEARCH/REPLACE blocks — apply them to the source code

                        log.Printf("[fix_proposer] attempt %d: parsed %d SEARCH/REPLACE block(s)", attempt+1, len(blocks))

                        // Group blocks by file path

                        blocksByFile := make(map[string][]EditBlock)
                        for _, b := range blocks {
                                path := b.FilePath
                                if path == "" {
                                        // Use the first fetched file as default

                                        for p := range state.FetchedCode {
                                                path = p
                                                break
                                        }
                                }
                                // Normalize path against fetched code keys

                                path = normalizeFilePath(path, state.FetchedCode)
                                blocksByFile[path] = append(blocksByFile[path], EditBlock{
                                        FilePath: path,
                                        Search:   b.Search,
                                        Replace:  b.Replace,
                                })
                        }

                        // Apply blocks to each file

                        allApplied := true
                        files = []schemas.FileChange{}
                        for filePath, fileBlocks := range blocksByFile {
                                originalContent, ok := state.FetchedCode[filePath]
                                if !ok {
                                        lastError = fmt.Sprintf("file %s not found in fetched source code. Available files: %v", filePath, keysOf(state.FetchedCode))
                                        allApplied = false
                                        break
                                }

                                editResult := ApplyEditBlocks(originalContent, fileBlocks)
                                if !editResult.Success {
                                        lastError = editResult.FailedReason
                                        allApplied = false
                                        break
                                }

                                // Build FileChange from the result — use Changes (search/replace)
                                // so the GitHub executor can apply them to the branch

                                var changes []schemas.SearchReplace
                                for _, b := range fileBlocks {
                                        changes = append(changes, schemas.SearchReplace{
                                                Search:  strings.TrimRight(b.Search, "\n"),
                                                Replace: strings.TrimRight(b.Replace, "\n"),
                                        })
                                }
                                files = append(files, schemas.FileChange{
                                        Path:    filePath,
                                        Changes: changes,
                                })
                                log.Printf("[fix_proposer] applied %d block(s) to %s", len(fileBlocks), filePath)
                        }

                        if allApplied && len(files) > 0 {
                                log.Printf("[fix_proposer] attempt %d succeeded — %d file(s), %d block(s) applied", attempt+1, len(files), len(blocks))
                                break
                        }
                        log.Printf("[fix_proposer] attempt %d failed: %s", attempt+1, lastError)
                        continue
                }

                // No SEARCH/REPLACE blocks found — try whole-file replacement fallback

                if filePath, content := ParseWholeFileReplacement(rawOutput); filePath != "" && content != "" {
                        filePath = normalizeFilePath(filePath, state.FetchedCode)
                        log.Printf("[fix_proposer] attempt %d: using whole-file replacement for %s (%d chars)", attempt+1, filePath, len(content))
                        files = []schemas.FileChange{{Path: filePath, NewContent: content}}
                        break
                }

                lastError = "no SEARCH/REPLACE blocks or whole-file replacement found in LLM output"
                log.Printf("[fix_proposer] attempt %d failed: %s", attempt+1, lastError)
        }

        if len(files) == 0 {
                log.Printf("[fix_proposer] all %d attempts failed — no fix proposed", maxRetries+1)
                state.Status = schemas.StatusFailed
                s := fmt.Sprintf("fix_proposer_no_files_after_%d_attempts: %s", maxRetries+1, lastError)
                state.Error = &s
                return nil
        }

        // Build the proposal from the successful attempt

        if prBody == "" {
                prBody = buildPRBody(state, rc, string(fixType))
        }

        proposal := &schemas.FixProposal{
                FixID:             uuid.New(),
                FixType:           fixType,
                TargetService:     state.Cluster.Service,
                TargetNamespace:   state.Cluster.Namespace,
                Description:       description,
                Repo:              strPtr(discoverRepo(state, a.gitHost)),
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

        // TOP of the message so the LLM sees it first. This implements the
        // "generate → verify → feed error back → retry" loop used by Aider,
        // OpenHands, and SWE-agent. The LLM gets the exact runtime error from the
        // sandbox and can fix the specific bug.

        if state.SandboxFeedback != nil && *state.SandboxFeedback != "" {
                parts = append(parts, "## ⚠️ Previous Fix Failed Sandbox Verification")
                parts = append(parts, "")
                parts = append(parts, "Your previous fix was deployed to a sandbox and tested. It FAILED:")
                parts = append(parts, "")
                parts = append(parts, *state.SandboxFeedback)
                parts = append(parts, "")
                parts = append(parts, "Fix the issue described above. The error is real — your previous fix introduced or did not resolve a runtime bug.")
                parts = append(parts, "Generate a NEW, corrected search/replace block that addresses both the original error AND the sandbox failure.")
                parts = append(parts, "")
        }

        // The FixProposer doesn't have direct access to podLogs, but the
        // anomaly explanations contain the original error logs. We concatenate
        // them and run the parser to detect the language.

        if lang := a.detectLanguageHint(state); lang != "" {
                parts = append(parts, fmt.Sprintf("## Detected Language: %s", lang))
                parts = append(parts, "")
        }
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

// detectLanguageHint concatenates the anomaly explanations (which contain the
// original pod logs) and runs ParseError to detect the language. v3.11.0:.
// Returns "" if no language can be detected.

func (a *FixProposer) detectLanguageHint(state *schemas.AgentState) string {
        var logs string
        for _, anomaly := range state.Cluster.Anomalies {
                logs += anomaly.Explanation + "\n"
        }
        if logs == "" {
                return ""
        }
        parsed := ParseError(logs)
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

        // hardcoding "main". If the service is deployed from "release-1.2", the
        // PR should target "release-1.2", not "main". Falls back to "main" if no
        // annotation is set (same as the investigator's clone behavior).

        proposal.BaseBranch = strPtr(a.resolveBaseBranch(state))

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
// Priority: sentinel.io/branch annotation > configured default branch.
// This mirrors the investigator's resolveBranch logic but WITHOUT the
// git-commit case (PRs target branches, not commits).
// so it can use the FixProposer's configurable defaultBranch.

func (a *FixProposer) resolveBaseBranch(state *schemas.AgentState) string {
        if branch, ok := state.Cluster.Annotations["sentinel.io/branch"]; ok && branch != "" {
                return branch
        }
        for _, anomaly := range state.Cluster.Anomalies {
                if branch, ok := anomaly.Annotations["sentinel.io/branch"]; ok && branch != "" {
                        return branch
                }
        }
        db := a.defaultBranch
        if db == "" {
                db = "main" // safety net
        }
        return db
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

// extractMetadataJSON extracts the small JSON metadata object from the LLM output.
// The LLM outputs {"fix_type":"code_fix",...} before the SEARCH/REPLACE blocks.
//   - markdown fences (```json...```)
//   - bare JSON objects ({...})
//   - prose around JSON
//   - truncated JSON repair

func extractMetadataJSON(rawOutput string) map[string]any {
        // The metadata JSON is the FIRST JSON object in the output (before the SEARCH/REPLACE blocks)
        // If the output starts with SEARCH/REPLACE blocks (no JSON), return empty map

        if strings.Contains(rawOutput, "<<<<<<< SEARCH") {
                // Find the JSON before the first SEARCH block

                idx := strings.Index(rawOutput, "<<<<<<< SEARCH")
                if idx > 0 {
                        beforeBlocks := rawOutput[:idx]
                        return llm.ExtractJSON(beforeBlocks)
                }
        }
        // No SEARCH blocks — try the whole output

        return llm.ExtractJSON(rawOutput)
}

// normalizeFilePath matches the LLM-provided file path against the keys in
// FetchedCode. LLMs often use container paths (e.g. /app/app/main.py) or
// add/remove leading directories. This function finds the best match.

func normalizeFilePath(path string, fetchedCode map[string]string) string {
        // Exact match

        if _, ok := fetchedCode[path]; ok {
                return path
        }
        // Try: path is a suffix of a fetched key (LLM used /app/app/main.py, key is app/main.py)

        for key := range fetchedCode {
                if strings.HasSuffix(key, path) || strings.HasSuffix(path, key) {
                        return key
                }
        }
        // Try: strip leading directories from path

        parts := strings.Split(path, "/")
        for i := 0; i < len(parts); i++ {
                shortPath := strings.Join(parts[i:], "/")
                if _, ok := fetchedCode[shortPath]; ok {
                        return shortPath
                }
        }
        // No match — return as-is (the apply step will fail with a clear error)

        return path
}
