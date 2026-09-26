package agents

// fix_proposer.go: the LLM agent that writes the fix as Aider-style
// SEARCH/REPLACE blocks. Calls the LLM, parses metadata JSON + blocks via
// editblock.go, applies them with layered (exact/whitespace/fuzzy) matching,
// and feeds actionable errors back on failure (Aider's pattern).
//
// Path validation (validateFilePath) is the second layer of defense against
// prompt injection coaxing the LLM into writing to sensitive paths
// (.github/workflows/*, Dockerfile, package.json, etc.) — the path comes from
// the LLM, which was influenced by pod-log content (attacker-controllable).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
	"github.com/karimzakzouk/sentinel/internal/syntaxvalidate"
)

// validateFilePath validates a file path returned by the LLM for a code fix.
// Returns the cleaned path on success, or "" if rejected. Callers should
// skip files with empty return values (do not abort the whole proposal — one
// bad path shouldn't kill a fix that has other valid files).
//
// Rules:
//  1. Reject empty paths
//  2. Reject absolute paths (anything starting with '/')
//  3. Reject paths containing '..' segments (path traversal)
//  4. Reject paths that escape the repo root after cleaning
//  5. Reject sensitive paths by default:
//     - .github/workflows/*, .github/actions/* (CI/CD tampering)
//     - .gitlab-ci.yml, .circleci/, Jenkinsfile, .drone.yml, cloudbuild.*, azure-pipelines.yml
//     - Dockerfile, docker-compose.* (build-chain tampering)
//     - package.json, package-lock.json (postinstall hooks)
//     - pyproject.toml, setup.py, requirements.txt, Pipfile, Pipfile.lock
//     - go.mod, go.sum
//     - .npmrc, .pypirc, .netrc (registry credentials)
//     - .env (but allow .env.example)
//  6. Reject paths with control characters or null bytes
//  7. Cap path length at 500 chars
func validateFilePath(path string) string {
	if path == "" {
		return ""
	}
	if len(path) > 500 {
		logging.Info("rejecting path (too long)", "path", path)
		return ""
	}

	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			logging.Info("rejecting path (control char)", "path", path)
			return ""
		}
	}

	if filepath.IsAbs(path) {
		logging.Info("rejecting absolute path", "path", path)
		return ""
	}

	clean := filepath.Clean(path)
	if clean == "." {
		logging.Info("rejecting path (resolves to repo root)", "path", path)
		return ""
	}
	if strings.HasPrefix(clean, "..") {
		logging.Info("rejecting path traversal", "path", path)
		return ""
	}

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

	case cleanSlash == "Dockerfile" || strings.HasPrefix(cleanSlash, "Dockerfile."),
		strings.HasPrefix(cleanSlash, "docker-compose.yml"),
		strings.HasPrefix(cleanSlash, "docker-compose.yaml"),
		cleanSlash == "package.json" || cleanSlash == "package-lock.json",
		cleanSlash == "pyproject.toml" || cleanSlash == "setup.py" || cleanSlash == "requirements.txt" || cleanSlash == "Pipfile" || cleanSlash == "Pipfile.lock",
		cleanSlash == "go.mod" || cleanSlash == "go.sum",
		cleanSlash == ".npmrc" || cleanSlash == ".pypirc" || cleanSlash == ".netrc",
		cleanSlash == ".env" && cleanSlash != ".env.example":
		sensitive = true
		sensitiveReason = "build-chain-sensitive path"
	}
	if sensitive {
		logging.Info("rejecting sensitive path", "reason", sensitiveReason, "path", path)
		return ""
	}

	return clean
}

const fixProposerSystemPrompt = `You are Sentinel's Fix Proposer agent. You write production code fixes for runtime errors.

Given a confirmed root cause and the source code, propose ONE fix using SEARCH/REPLACE blocks.

## Fix Quality Requirements
- Fix the ROOT CAUSE, not just the symptom. If a collection access crashes (dict/key lookup, array index, map get), add a proper check — don't just wrap it in try/catch.
- Fix the LAYER the fault mechanism demands, not just the crash site (the root cause's fault_class and fix_layer are stated below when available):
  - Transient network faults (DNS gaierror, ECONNREFUSED/ECONNRESET, timeouts, throttling): add retry-with-backoff in the task/job layer — e.g. Celery autoretry_for=(socket.gaierror, ...), retry_backoff=True, max_retries>=3; JS/TS a retry wrapper with exponential backoff; Java Spring @Retryable. Do NOT add input/config validation at the call site for a transient fault.
  - OOM / memory kills (OOMKilled, heap out of memory, exit 137): bound the growing structure at its GROWTH site — cap the collection, add eviction/TTL/LRU. Guards at the crash line do NOT stop an OOM.
  - Concurrency faults (race, deadlock, lock ordering): fix the protocol — consistent lock ordering, atomic check-then-act — not one call site.
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

internal/handler/handler.go
<<<<<<< SEARCH
        sku := r.URL.Path[len("/products/"):]
=======
        if len(r.URL.Path) <= len("/products/") {
                http.Error(w, "sku required", http.StatusBadRequest)
                return
        }
        sku := r.URL.Path[len("/products/"):]
>>>>>>> REPLACE

Rules:
- CRITICAL: The SEARCH section MUST be COPIED VERBATIM from the source code provided above.
  Do NOT use placeholder text. Copy the ACTUAL code from the source file shown in this prompt.
  The SEARCH block is a literal substring match — if even ONE character differs, it will fail.
- Keep blocks small (2-10 lines) — only the code that needs to change
- Multiple blocks per file are OK if multiple spots need fixing
- The file path goes on the line BEFORE <<<<<<< SEARCH
- Do NOT wrap the SEARCH/REPLACE blocks in JSON or code fences

## Example Output

{"fix_type":"code_fix","description":"Add length check before path slice","pr_title":"[Sentinel] Fix index out of range in handler","pr_body":"Added a length check before slicing the URL path."}

internal/handler/handler.go
<<<<<<< SEARCH
        sku := r.URL.Path[len("/products/"):]
=======
        if len(r.URL.Path) <= len("/products/") {
                http.Error(w, "sku required", http.StatusBadRequest)
                return
        }
        sku := r.URL.Path[len("/products/"):]
>>>>>>> REPLACE`

// FixProposer translates a root cause into a concrete FixProposal. Calls the
// LLM, parses SEARCH/REPLACE blocks, applies them with layered matching, and
// feeds actionable errors back on failure (Aider's pattern).
type FixProposer struct {
	router      *llm.Router
	k8s         *k8s.Client
	gitHubToken string

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

// SetGitHost sets the git host for repo normalization.
func (a *FixProposer) SetGitHost(host string) {
	if host != "" {
		a.gitHost = host
	}
}

// SetDefaultBranch sets the default branch fallback for PR targeting.
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

	// Fallback: if we have no source code, propose restart. Don't ask the
	// LLM to write code it can't see — it'll hallucinate.
	if len(state.FetchedCode) == 0 {
		logging.Info("no source code fetched — proposing restart fallback")
		proposal := &schemas.FixProposal{
			FixID:              uuid.New(),
			FixType:            schemas.FixRestart,
			TargetService:      state.Cluster.Service,
			TargetNamespace:    state.Cluster.Namespace,
			Description:        fmt.Sprintf("Restart fallback: no source code available to generate a code fix. Root cause: %s", rc.Statement),
			Repo:               strPtr(discoverRepo(state, a.gitHost)),
			PRTitle:            strPtr(fmt.Sprintf("[Sentinel] Restart %s (no source available for code fix)", state.Cluster.Service)),
			PRBody:             strPtr(buildPRBody(state, rc, "restart")),
			RequiresSandbox:    false,
			ProposedConfidence: rc.Confidence * 0.5, // lower confidence — we're guessing
		}
		a.finalizeProposal(state, proposal)
		return nil
	}

	// Schema migrations block autonomous rollback — force roll_forward.
	if rc.HasSchemaMigration && (rc.Category == "bad_deploy" || rc.Category == "schema_migration") {
		proposal := &schemas.FixProposal{
			FixID:              uuid.New(),
			FixType:            schemas.FixRollForward,
			TargetService:      state.Cluster.Service,
			TargetNamespace:    state.Cluster.Namespace,
			Description:        fmt.Sprintf("Roll-forward fix for %s (schema migration blocks autonomous rollback)", rc.Category),
			Repo:               strPtr(discoverRepo(state, a.gitHost)),
			PRTitle:            strPtr(fmt.Sprintf("[Sentinel] Roll-forward fix for %s in %s", rc.Category, state.Cluster.Service)),
			PRBody:             strPtr(buildPRBody(state, rc, "roll_forward")),
			RequiresSandbox:    true,
			ProposedConfidence: rc.Confidence,
		}
		// Still call the LLM to generate the actual code fix.
		a.llmCodeFix(ctx, state, proposal)
		a.finalizeProposal(state, proposal)
		return nil
	}

	// Ask the LLM for the fix — with adaptive retry loop. The LLM outputs
	// metadata JSON (small) + SEARCH/REPLACE blocks (plain text). We parse
	// the blocks, apply them with layered matching (exact → fuzzy), and feed
	// actionable errors back on failure (Aider's pattern).
	userMsg := a.buildUserMessage(state)
	var files []schemas.FileChange
	var fixType schemas.FixType
	var dockerfilePath, prTitle, prBody, description string
	var lastError string

	maxRetries := 5
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var retryHint string
		if attempt > 0 {
			retryHint = fmt.Sprintf("\n\n## PREVIOUS ATTEMPT FAILED\n%s\n\nPlease try again. Fix the issue above and return a corrected response.", lastError)
			userMsg = a.buildUserMessage(state) + retryHint
		}

		// Per-attempt timeout: large reasoning models can take 3-5 minutes
		// to generate code; the per-call cap is 300s, clipped by ctx.
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 300*time.Second)
		logging.Info("fix proposer attempt", "attempt", attempt+1, "max", maxRetries+1)
		rawOutput, llmErr := a.router.FrontierText(attemptCtx, fixProposerSystemPrompt, userMsg, 8000)
		attemptCancel()
		if llmErr != nil {
			lastError = fmt.Sprintf("LLM call failed: %v", llmErr)
			logging.Error("attempt LLM error", "attempt", attempt+1, "err", llmErr)
			continue
		}

		logging.Info("got LLM output", "attempt", attempt+1, "bytes", len(rawOutput))

		// Parse metadata JSON from the first line(s). The LLM outputs
		// {"fix_type":"code_fix",...} before the SEARCH/REPLACE blocks.
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

		// Parse SEARCH/REPLACE blocks from the output.
		blocks := ParseEditBlocks(rawOutput)

		if len(blocks) > 0 {
			logging.Info("parsed SEARCH/REPLACE blocks", "attempt", attempt+1, "count", len(blocks))

			// Group blocks by file path.
			blocksByFile := make(map[string][]EditBlock)
			for _, b := range blocks {
				path := b.FilePath
				if path == "" {
					// Use the first fetched file as default.
					for p := range state.FetchedCode {
						path = p
						break
					}
				}
				path = normalizeFilePath(path, state.FetchedCode)
				blocksByFile[path] = append(blocksByFile[path], EditBlock{
					FilePath: path,
					Search:   b.Search,
					Replace:  b.Replace,
				})
			}

			// Apply blocks to each file.
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

				// Syntax-validate the patched content BEFORE accepting it.
				// This catches broken code that the LLM generated (missing
				// brackets, invalid syntax, etc.) and feeds the error back
				// on the next retry. Based on the Aider/SWE-agent pattern
				// of validate-then-retry.
				if syntaxErr := syntaxvalidate.Validate(filePath, editResult.NewContent); syntaxErr != nil {
					lastError = fmt.Sprintf("syntax validation failed for %s: %v", filePath, syntaxErr)
					logging.Error("fix proposer: syntax validation failed", "attempt", attempt+1, "path", filePath, "err", syntaxErr)
					allApplied = false
					break
				}

				// Build FileChange from the result — use Changes (search/replace)
				// so the GitHub executor can apply them to the branch.
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
				logging.Info("applied blocks to file", "attempt", attempt+1, "blocks", len(fileBlocks), "path", filePath)
			}

			if allApplied && len(files) > 0 {
				logging.Info("fix attempt succeeded", "attempt", attempt+1, "files", len(files), "blocks", len(blocks))
				break
			}
			logging.Error("fix attempt failed", "attempt", attempt+1, "err", lastError)
			continue
		}

		// No SEARCH/REPLACE blocks found — try whole-file replacement fallback.
		if filePath, content := ParseWholeFileReplacement(rawOutput); filePath != "" && content != "" {
			filePath = normalizeFilePath(filePath, state.FetchedCode)
			logging.Info("using whole-file replacement", "attempt", attempt+1, "path", filePath, "bytes", len(content))
			files = []schemas.FileChange{{Path: filePath, NewContent: content}}
			break
		}

		lastError = "no SEARCH/REPLACE blocks or whole-file replacement found in LLM output"
		logging.Error("fix attempt failed", "attempt", attempt+1, "err", lastError)
	}

	if len(files) == 0 {
		logging.Error("all fix attempts failed", "attempts", maxRetries+1)
		state.Status = schemas.StatusFailed
		s := fmt.Sprintf("fix_proposer_no_files_after_%d_attempts: %s", maxRetries+1, lastError)
		state.Error = &s
		return nil
	}

	if prBody == "" {
		prBody = buildPRBody(state, rc, string(fixType))
	}

	proposal := &schemas.FixProposal{
		FixID:              uuid.New(),
		FixType:            fixType,
		TargetService:      state.Cluster.Service,
		TargetNamespace:    state.Cluster.Namespace,
		Description:        description,
		Repo:               strPtr(discoverRepo(state, a.gitHost)),
		FilesToChange:      files,
		DockerfilePath:     strPtr(dockerfilePath),
		PRTitle:            strPtr(prTitle),
		PRBody:             strPtr(prBody),
		RequiresSandbox:    true,
		ProposedConfidence: rc.Confidence,
	}

	a.finalizeProposal(state, proposal)
	return nil
}

// buildUserMessage constructs the prompt for the LLM. Puts sandbox feedback
// (if any) at the top so the LLM sees it first, then the root cause, evidence,
// and fetched source code.
func (a *FixProposer) buildUserMessage(state *schemas.AgentState) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, fmt.Sprintf("# Namespace: %s", state.Cluster.Namespace))
	parts = append(parts, "")

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

	if lang := a.detectLanguageHint(state); lang != "" {
		parts = append(parts, fmt.Sprintf("## Detected Language: %s", lang))
		parts = append(parts, "")
	}
	// Runtime ground truth (v1.7.4): the env var names the deployment
	// actually sets, so fixes never rename env vars to canonical names the
	// environment doesn't provide (run-#11 user-api harmful-fix class).
	if rcBlock := renderRuntimeContext(state.RuntimeContext); rcBlock != "" {
		parts = append(parts, rcBlock)
	}
	parts = append(parts, "## Confirmed Root Cause")
	if state.RootCause == nil {
		return ""
	}
	parts = append(parts, fmt.Sprintf("- statement: %s", state.RootCause.Statement))
	parts = append(parts, fmt.Sprintf("- category: %s", state.RootCause.Category))
	if state.RootCause.FaultClass != "" {
		parts = append(parts, fmt.Sprintf("- fault class: %s", state.RootCause.FaultClass))
	}
	if state.RootCause.FixLayer != "" {
		parts = append(parts, fmt.Sprintf("- fix layer: %s", state.RootCause.FixLayer))
	}
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
		if len(resultStr) > MaxFixResultChars {
			resultStr = resultStr[:MaxFixResultChars]
		}
		parts = append(parts, fmt.Sprintf("- %s (%s): %s", e.EID, e.Source, resultStr))
	}
	parts = append(parts, "")
	parts = append(parts, "## Fetched Source Code")
	i := 0
	for path, content := range state.FetchedCode {
		if i >= 6 {
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
	if rules := runtimeContextGroundingRules(state.RuntimeContext); rules != "" {
		parts = append(parts, rules)
	}
	return strings.Join(parts, "\n")
}

// detectLanguageHint concatenates anomaly explanations (which contain the
// original pod logs) and runs ParseError to detect the language. Returns ""
// if no language can be detected.
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

// llmCodeFix is used for roll_forward cases where we still need an
// LLM-generated patch in addition to the roll_forward action.
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

// finalizeProposal sets the branch name + base branch, attaches the proposal
// to state, and appends an agent-trace entry.
func (a *FixProposer) finalizeProposal(state *schemas.AgentState, proposal *schemas.FixProposal) {
	branchName := fmt.Sprintf("sentinel/%s-%s", state.Cluster.Service, state.IncidentID.String()[:8])
	proposal.BranchName = strPtr(branchName)
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

// resolveBaseBranch determines which branch the PR should target. Priority:
// sentinel.io/branch annotation > configured default branch. Mirrors the
// investigator's resolveBranch but WITHOUT the git-commit case (PRs target
// branches, not commits).
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
		db = "main"
	}
	return db
}

// buildPRBody is the placeholder PR body written before sandbox verification.
// The actual PR body is overwritten by agents.BuildPRBody after the sandbox
// runs. The placeholder must NOT claim sandbox verification was performed —
// it hasn't been yet.
func buildPRBody(state *schemas.AgentState, rc *schemas.RootCause, fixType string) string {
	var parts []string
	parts = append(parts, "# Sentinel Autonomous Fix")
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("**Fix type:** `%s`", fixType))
	parts = append(parts, fmt.Sprintf("**Service:** `%s` (namespace: `%s`)", state.Cluster.Service, state.Cluster.Namespace))
	parts = append(parts, fmt.Sprintf("**Confidence:** `%.3f`", rc.Confidence))
	parts = append(parts, fmt.Sprintf("**Category:** `%s`", rc.Category))
	if rc.FaultClass != "" {
		parts = append(parts, fmt.Sprintf("**Fault class:** `%s`", rc.FaultClass))
	}
	if rc.FixLayer != "" {
		parts = append(parts, fmt.Sprintf("**Fix layer:** %s", rc.FixLayer))
	}
	parts = append(parts, "")
	parts = append(parts, "## Root Cause")
	parts = append(parts, rc.Statement)
	parts = append(parts, "")
	parts = append(parts, "## Evidence")
	for _, e := range state.Evidence {
		resultJSON, _ := json.Marshal(e.Result)
		resultStr := string(resultJSON)
		if len(resultStr) > MaxFixResultChars {
			resultStr = resultStr[:MaxFixResultChars]
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
	parts = append(parts, "_Sandbox verification status will be appended by the execution layer._")
	parts = append(parts, "")
	parts = append(parts, "---")
	parts = append(parts, "_This PR was opened autonomously by Sentinel. Please review before merging._")
	return strings.Join(parts, "\n")
}

// isValidFixType returns true if s is a recognized FixType.
func isValidFixType(s string) bool {
	switch schemas.FixType(s) {
	case schemas.FixRestart, schemas.FixScale, schemas.FixCodeFix,
		schemas.FixRollback, schemas.FixRollForward, schemas.FixInfrastructure:
		return true
	}
	return false
}

// strPtr returns a pointer to s. Convenience for optional string fields.
func strPtr(s string) *string { return &s }

// extractMetadataJSON extracts the small JSON metadata object from the LLM
// output. The LLM outputs {"fix_type":"code_fix",...} before the SEARCH/REPLACE
// blocks. If the output starts with SEARCH/REPLACE blocks (no JSON), returns
// an empty map.
func extractMetadataJSON(rawOutput string) map[string]any {
	if strings.Contains(rawOutput, "<<<<<<< SEARCH") {
		idx := strings.Index(rawOutput, "<<<<<<< SEARCH")
		if idx > 0 {
			beforeBlocks := rawOutput[:idx]
			return llm.ExtractJSON(beforeBlocks)
		}
	}
	return llm.ExtractJSON(rawOutput)
}

// normalizeFilePath matches the LLM-provided file path against the keys in
// FetchedCode. LLMs often use container paths (e.g. /app/app/main.py) or
// add/remove leading directories. Returns the matching fetched-code key, or
// the original path if no match.
func normalizeFilePath(path string, fetchedCode map[string]string) string {
	if path == "" {
		return ""
	}
	// Exact match.
	if _, ok := fetchedCode[path]; ok {
		return path
	}
	// Path is a suffix of a fetched key (LLM used /app/app/main.py, key is app/main.py).
	for key := range fetchedCode {
		if strings.HasSuffix(key, path) || strings.HasSuffix(path, key) {
			return key
		}
	}
	// Strip leading directories from path.
	parts := strings.Split(path, "/")
	for i := 0; i < len(parts); i++ {
		shortPath := strings.Join(parts[i:], "/")
		if _, ok := fetchedCode[shortPath]; ok {
			return shortPath
		}
	}
	// No match — return as-is (the apply step will fail with a clear error).
	return path
}
