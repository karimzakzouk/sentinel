package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestValidateFilePath is a table-driven test of the path-validation rules.
// Rejects: empty, too long, control chars, absolute, traversal, repo root,
// and sensitive paths (CI/CD, Dockerfile, build-chain, env files).
func TestValidateFilePath(t *testing.T) {
	cases := []struct {
		name string
		path string
		ok   bool
	}{
		{"clean", "app/main.py", true},
		{"nested clean", "src/services/auth/handler.go", true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 501), false},
		{"control char", "app/main\x00.py", false},
		{"absolute path", "/etc/passwd", false},
		{"traversal", "../../../etc/passwd", false},
		{"resolves to root", ".", false},
		{"resolves to root via ./", "./", false},
		{"github workflow", ".github/workflows/ci.yml", false},
		{"github actions", ".github/actions/setup/action.yml", false},
		{"gitlab ci", ".gitlab-ci.yml", false},
		{"circle ci", ".circleci/config.yml", false},
		{"jenkinsfile", "Jenkinsfile", false},
		{"drone", ".drone.yml", false},
		{"cloudbuild yaml", "cloudbuild.yaml", false},
		{"cloudbuild yml", "cloudbuild.yml", false},
		{"azure pipelines", "azure-pipelines.yml", false},
		{"dockerfile", "Dockerfile", false},
		{"dockerfile dot", "Dockerfile.prod", false},
		{"docker compose", "docker-compose.yml", false},
		{"package.json", "package.json", false},
		{"package-lock.json", "package-lock.json", false},
		{"pyproject", "pyproject.toml", false},
		{"setup.py", "setup.py", false},
		{"requirements", "requirements.txt", false},
		{"pipfile", "Pipfile", false},
		{"pipfile.lock", "Pipfile.lock", false},
		{"go.mod", "go.mod", false},
		{"go.sum", "go.sum", false},
		{"npmrc", ".npmrc", false},
		{"pypirc", ".pypirc", false},
		{"netrc", ".netrc", false},
		{"env", ".env", false},
		{"env example allowed", ".env.example", true},
		{"shell script allowed", "scripts/deploy.sh", true},
		{"makefile allowed", "Makefile", true},
		{"mk allowed", "build.mk", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateFilePath(c.path)
			if c.ok && got == "" {
				t.Errorf("expected path %q to be accepted, got rejection", c.path)
			}
			if !c.ok && got != "" {
				t.Errorf("expected path %q to be rejected, got %q", c.path, got)
			}
		})
	}
}

// TestNormalizeFilePath verifies that LLM-returned paths (often container
// paths like /app/app/main.py) are normalized to the fetched-code keys.
func TestNormalizeFilePath(t *testing.T) {
	fetched := map[string]string{
		"app/main.py":    "content",
		"src/handler.go": "content",
		"lib/utils.py":   "content",
	}

	cases := []struct {
		name string
		path string
		want string
	}{
		{"exact match", "app/main.py", "app/main.py"},
		{"path is suffix of key (container path)", "/app/app/main.py", "app/main.py"},
		{"strip leading dirs", "foo/bar/lib/utils.py", "lib/utils.py"},
		{"no match returns as-is", "unknown/file.go", "unknown/file.go"},
		{"empty path returns empty", "", ""},
		{"path that doesn't match any key", "src/services/handler.go", "src/services/handler.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeFilePath(c.path, fetched)
			if got != c.want {
				t.Errorf("normalizeFilePath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestIsValidFixType verifies that all canonical FixType values are
// recognized and unknown values are rejected.
func TestIsValidFixType(t *testing.T) {
	valid := []string{"restart", "scale", "code_fix", "rollback", "roll_forward", "infrastructure"}
	for _, ft := range valid {
		if !isValidFixType(ft) {
			t.Errorf("expected %q to be valid", ft)
		}
	}
	invalid := []string{"", "code", "unknown", "CODE_FIX", "deploy"}
	for _, ft := range invalid {
		if isValidFixType(ft) {
			t.Errorf("expected %q to be invalid", ft)
		}
	}
}

// TestExtractMetadataJSON verifies that the metadata JSON preceding the
// SEARCH/REPLACE blocks is extracted correctly.
func TestExtractMetadataJSON(t *testing.T) {
	// Metadata JSON before SEARCH/REPLACE blocks.
	input := `{"fix_type":"code_fix","description":"Add zero check","pr_title":"[Sentinel] Fix ZeroDivisionError"}

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise ValueError("zero")
    result = a / b
>>>>>>> REPLACE`

	m := extractMetadataJSON(input)
	if m["fix_type"] != "code_fix" {
		t.Errorf("fix_type = %v, want code_fix", m["fix_type"])
	}
	if m["description"] != "Add zero check" {
		t.Errorf("description = %v", m["description"])
	}

	// No SEARCH blocks — try the whole output.
	m = extractMetadataJSON(`{"fix_type":"restart"}`)
	if m["fix_type"] != "restart" {
		t.Errorf("fix_type = %v, want restart", m["fix_type"])
	}

	// No JSON at all.
	m = extractMetadataJSON("just prose, no JSON")
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// TestBuildPRBody verifies that the placeholder PR body mentions the root
// cause and does NOT claim sandbox verification was performed.
func TestBuildPRBody(t *testing.T) {
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "Division by zero in /divide endpoint",
			Confidence: 0.85,
			Category:   "bug",
		},
	}
	body := buildPRBody(state, state.RootCause, "code_fix")
	if !strings.Contains(body, "code_fix") {
		t.Errorf("expected fix type in body, got: %s", body)
	}
	if !strings.Contains(body, "Division by zero") {
		t.Errorf("expected root cause in body, got: %s", body)
	}
	if !strings.Contains(body, "0.850") {
		t.Errorf("expected confidence in body, got: %s", body)
	}
	// Placeholder must NOT claim sandbox verification.
	if strings.Contains(body, "tested in a sandbox") {
		t.Errorf("placeholder PR body must not claim sandbox verification, got: %s", body)
	}
	if !strings.Contains(body, "Sandbox verification status will be appended") {
		t.Errorf("expected sandbox-placeholder note, got: %s", body)
	}
}

// TestResolveBaseBranch verifies the PR-target branch resolution priority:
// sentinel.io/branch annotation > configured default.
func TestResolveBaseBranch(t *testing.T) {
	fp := &FixProposer{defaultBranch: "main"}

	// No annotation → default branch.
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api"},
	}
	if got := fp.resolveBaseBranch(state); got != "main" {
		t.Errorf("expected main, got %q", got)
	}

	// Cluster annotation wins.
	state.Cluster.Annotations = map[string]string{"sentinel.io/branch": "release-1.2"}
	if got := fp.resolveBaseBranch(state); got != "release-1.2" {
		t.Errorf("expected release-1.2, got %q", got)
	}

	// Anomaly annotation used if cluster annotation absent.
	state.Cluster.Annotations = nil
	state.Cluster.Anomalies = []schemas.Anomaly{{
		Annotations: map[string]string{"sentinel.io/branch": "release-2.0"},
	}}
	if got := fp.resolveBaseBranch(state); got != "release-2.0" {
		t.Errorf("expected release-2.0, got %q", got)
	}

	// Custom default branch.
	fp.defaultBranch = "develop"
	state.Cluster.Anomalies = nil
	if got := fp.resolveBaseBranch(state); got != "develop" {
		t.Errorf("expected develop, got %q", got)
	}
}

// TestFixProposer_NoRootCause verifies the early-fail path: with no root
// cause set, the proposer transitions to StatusFailed without calling the LLM.
func TestFixProposer_NoRootCause(t *testing.T) {
	fp := NewFixProposer(llm.New("", "", "m", "f"), nil, "")
	state := &schemas.AgentState{}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.Status != schemas.StatusFailed {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusFailed)
	}
	if state.Error == nil || *state.Error != "fix_proposer_no_root_cause" {
		t.Errorf("expected fix_proposer_no_root_cause error, got %v", state.Error)
	}
}

// TestFixProposer_NoFetchedCode_ProposesRestart verifies the fallback path:
// with no source code fetched, the proposer proposes a restart instead of
// asking the LLM to hallucinate a fix.
func TestFixProposer_NoFetchedCode_ProposesRestart(t *testing.T) {
	fp := NewFixProposer(llm.New("", "", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "pod crashed",
			Confidence: 0.6,
		},
		// FetchedCode is nil — triggers restart fallback.
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.FixProposal == nil {
		t.Fatal("expected fix proposal to be set")
	}
	if state.FixProposal.FixType != schemas.FixRestart {
		t.Errorf("fix_type = %q, want %q", state.FixProposal.FixType, schemas.FixRestart)
	}
	if state.FixProposal.RequiresSandbox {
		t.Error("restart should not require sandbox")
	}
	// Restart confidence is half the root-cause confidence (we're guessing).
	if state.FixProposal.ProposedConfidence != 0.3 {
		t.Errorf("confidence = %v, want 0.3", state.FixProposal.ProposedConfidence)
	}
	if state.Status != schemas.StatusFixProposed {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusFixProposed)
	}
}

// TestFixProposer_SearchReplaceBlockSuccess verifies the happy path: the LLM
// returns valid SEARCH/REPLACE blocks that apply cleanly to the fetched code.
func TestFixProposer_SearchReplaceBlockSuccess(t *testing.T) {
	content := `{"fix_type":"code_fix","description":"Add zero check","pr_title":"[Sentinel] Fix ZeroDivisionError"}

` +
		`app/main.py
` +
		`<<<<<<< SEARCH
` +
		`    result = a / b
` +
		`=======
` +
		`    if b == 0:
        raise ValueError("zero")
    result = a / b
` +
		`>>>>>>> REPLACE`
	server := httptest.NewServer(llmJSONResponseFunc(t, content))
	defer server.Close()

	fp := NewFixProposer(llm.New(server.URL, "k", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "division by zero",
			Confidence: 0.8,
		},
		FetchedCode: map[string]string{
			"app/main.py": "def divide(a, b):\n    result = a / b\n    return result\n",
		},
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.FixProposal == nil {
		t.Fatal("expected fix proposal to be set")
	}
	if state.FixProposal.FixType != schemas.FixCodeFix {
		t.Errorf("fix_type = %q, want code_fix", state.FixProposal.FixType)
	}
	if len(state.FixProposal.FilesToChange) != 1 {
		t.Fatalf("expected 1 file change, got %d", len(state.FixProposal.FilesToChange))
	}
	fc := state.FixProposal.FilesToChange[0]
	if fc.Path != "app/main.py" {
		t.Errorf("path = %q, want app/main.py", fc.Path)
	}
	if len(fc.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(fc.Changes))
	}
	if fc.Changes[0].Search != "    result = a / b" {
		t.Errorf("search = %q", fc.Changes[0].Search)
	}
	if !strings.Contains(fc.Changes[0].Replace, "ValueError") {
		t.Errorf("replace = %q, expected ValueError", fc.Changes[0].Replace)
	}
}

// TestFixProposer_RetryLoopOnErrorFeedback verifies the Aider-style retry
// loop: the first LLM response has a SEARCH block that doesn't match, the
// proposer feeds the error back, and the second response succeeds.
func TestFixProposer_RetryLoopOnErrorFeedback(t *testing.T) {
	firstContent := `{"fix_type":"code_fix"}

` +
		`app/main.py
` +
		`<<<<<<< SEARCH
` +
		`    NONEXISTENT CODE
` +
		`=======
` +
		`    replaced
` +
		`>>>>>>> REPLACE`
	secondContent := `{"fix_type":"code_fix"}

` +
		`app/main.py
` +
		`<<<<<<< SEARCH
` +
		`    result = a / b
` +
		`=======
` +
		`    if b == 0:
        raise ValueError("zero")
    result = a / b
` +
		`>>>>>>> REPLACE`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			llmJSONResponse(t, w, firstContent)
			return
		}
		llmJSONResponse(t, w, secondContent)
	}))
	defer server.Close()

	fp := NewFixProposer(llm.New(server.URL, "k", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "division by zero",
			Confidence: 0.8,
		},
		FetchedCode: map[string]string{
			"app/main.py": "def divide(a, b):\n    result = a / b\n    return result\n",
		},
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 LLM calls (retry), got %d", calls)
	}
	if state.FixProposal == nil || len(state.FixProposal.FilesToChange) != 1 {
		t.Fatalf("expected fix proposal with 1 file change after retry, got %+v", state.FixProposal)
	}
}

// TestFixProposer_AllAttemptsFail verifies that after maxRetries+1 failed
// attempts, the proposer transitions to StatusFailed with a clear error.
func TestFixProposer_AllAttemptsFail(t *testing.T) {
	content := `{"fix_type":"code_fix"}

` +
		`app/main.py
` +
		`<<<<<<< SEARCH
` +
		`    NONEXISTENT
` +
		`=======
` +
		`    replaced
` +
		`>>>>>>> REPLACE`
	server := httptest.NewServer(llmJSONResponseFunc(t, content))
	defer server.Close()

	fp := NewFixProposer(llm.New(server.URL, "k", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "division by zero",
			Confidence: 0.8,
		},
		FetchedCode: map[string]string{"app/main.py": "def divide(a, b):\n    return a / b\n"},
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.Status != schemas.StatusFailed {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusFailed)
	}
	if state.Error == nil || !strings.Contains(*state.Error, "fix_proposer_no_files_after") {
		t.Errorf("expected fix_proposer_no_files_after error, got %v", state.Error)
	}
}

// TestFixProposer_WholeFileReplacement verifies the fallback path where the
// LLM returns the entire fixed file instead of SEARCH/REPLACE blocks.
func TestFixProposer_WholeFileReplacement(t *testing.T) {
	content := `{"fix_type":"code_fix"}

` +
		`app/main.py
` +
		"```python\n" +
		`def divide(a, b):
    if b == 0:
        raise ValueError("zero")
    return a / b
` +
		"```"
	server := httptest.NewServer(llmJSONResponseFunc(t, content))
	defer server.Close()

	fp := NewFixProposer(llm.New(server.URL, "k", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "division by zero",
			Confidence: 0.8,
		},
		FetchedCode: map[string]string{"app/main.py": "def divide(a, b):\n    return a / b\n"},
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.FixProposal == nil {
		t.Fatal("expected fix proposal")
	}
	if len(state.FixProposal.FilesToChange) != 1 {
		t.Fatalf("expected 1 file change, got %d", len(state.FixProposal.FilesToChange))
	}
	fc := state.FixProposal.FilesToChange[0]
	if fc.NewContent == "" {
		t.Error("expected NewContent to be set for whole-file replacement")
	}
	if len(fc.Changes) != 0 {
		t.Errorf("expected 0 search/replace changes for whole-file replacement, got %d", len(fc.Changes))
	}
}

// TestFixProposer_SchemaMigrationForcesRollForward verifies that a root cause
// with HasSchemaMigration=true and category=bad_deploy forces roll_forward.
func TestFixProposer_SchemaMigrationForcesRollForward(t *testing.T) {
	fp := NewFixProposer(llm.New("http://invalid:9", "k", "m", "f"), nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:          "schema migration broke prod",
			Confidence:         0.7,
			Category:           "bad_deploy",
			HasSchemaMigration: true,
		},
		FetchedCode: map[string]string{"app/main.py": "content"},
	}
	if err := fp.Run(context.Background(), state); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if state.FixProposal == nil {
		t.Fatal("expected fix proposal")
	}
	if state.FixProposal.FixType != schemas.FixRollForward {
		t.Errorf("fix_type = %q, want %q", state.FixProposal.FixType, schemas.FixRollForward)
	}
	if !state.FixProposal.RequiresSandbox {
		t.Error("schema-migration roll_forward should require sandbox")
	}
}

// TestFixProposer_SandboxFeedbackIncludedInMessage verifies that sandbox
// feedback is placed at the top of the user message so the LLM sees it first.
func TestFixProposer_SandboxFeedbackIncludedInMessage(t *testing.T) {
	fp := NewFixProposer(llm.New("", "", "m", "f"), nil, "")
	feedback := "previous fix crashed at line 70"
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "division by zero",
			Confidence: 0.8,
			Category:   "bug",
		},
		FetchedCode:     map[string]string{"app/main.py": "content"},
		SandboxFeedback: &feedback,
	}
	msg := fp.buildUserMessage(state)
	if !strings.Contains(msg, "Previous Fix Failed Sandbox Verification") {
		t.Errorf("expected sandbox feedback header in message, got: %s", msg)
	}
	if !strings.Contains(msg, feedback) {
		t.Errorf("expected sandbox feedback content in message, got: %s", msg)
	}
	// The feedback should appear before the root cause section.
	fbIdx := strings.Index(msg, feedback)
	rcIdx := strings.Index(msg, "## Confirmed Root Cause")
	if fbIdx < 0 || rcIdx < 0 || fbIdx > rcIdx {
		t.Errorf("expected sandbox feedback before root cause section (fb=%d, rc=%d)", fbIdx, rcIdx)
	}
}

// TestFixProposer_FinalizeProposal verifies that finalizeProposal sets the
// branch name and base branch correctly.
func TestFixProposer_FinalizeProposal(t *testing.T) {
	fp := &FixProposer{defaultBranch: "main"}
	state := &schemas.AgentState{
		Cluster:    schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		IncidentID: uuid.NameSpaceDNS, // any UUID for predictable prefix
	}
	proposal := &schemas.FixProposal{FixType: schemas.FixCodeFix}
	fp.finalizeProposal(state, proposal)

	if proposal.BranchName == nil || !strings.HasPrefix(*proposal.BranchName, "sentinel/api-") {
		t.Errorf("expected branch name sentinel/api-<8hex>, got %v", proposal.BranchName)
	}
	if proposal.BaseBranch == nil || *proposal.BaseBranch != "main" {
		t.Errorf("expected base branch main, got %v", proposal.BaseBranch)
	}
	if state.FixProposal != proposal {
		t.Error("expected proposal to be attached to state")
	}
	if state.Status != schemas.StatusFixProposed {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusFixProposed)
	}
}
