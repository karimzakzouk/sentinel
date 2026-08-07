package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestExtractCrashLocation verifies that the crash-location block is
// formatted from the traceback and points the LLM at the actual crash.
func TestExtractCrashLocation(t *testing.T) {
	logs := `Traceback (most recent call last):
  File "app/main.py", line 70, in get_config
    value = config.get(key).upper()
AttributeError: 'NoneType' object has no attribute 'upper'
`
	out := extractCrashLocation(logs)
	if !strings.Contains(out, "AttributeError") {
		t.Errorf("expected error type in output, got: %s", out)
	}
	if !strings.Contains(out, "app/main.py") {
		t.Errorf("expected source file in output, got: %s", out)
	}
	if !strings.Contains(out, "70") {
		t.Errorf("expected source line in output, got: %s", out)
	}
	// The IMPORTANT note must be present so the LLM focuses on the crash.
	if !strings.Contains(out, "IMPORTANT") {
		t.Errorf("expected IMPORTANT note in output, got: %s", out)
	}
}

// TestExtractCrashLocation_FunctionOnErrorLine verifies that when the
// function name appears on the error-type line itself (Go-style:
// "panic: foo in main.bar()"), it is included in the crash location.
func TestExtractCrashLocation_FunctionOnErrorLine(t *testing.T) {
	logs := `panic: runtime error: index out of range
goroutine 1 [running]:
main.get_config()
        /app/main.go:70 +0x100`
	out := extractCrashLocation(logs)
	if !strings.Contains(out, "panic") {
		t.Errorf("expected error type 'panic' in output, got: %s", out)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("expected source file in output, got: %s", out)
	}
}

// TestExtractCrashLocation_NoError verifies that empty logs return "".
func TestExtractCrashLocation_NoError(t *testing.T) {
	if got := extractCrashLocation("just some logs"); got != "" {
		t.Errorf("expected empty crash location for non-error logs, got: %s", got)
	}
}

// TestDetectLanguageHint verifies the language hint maps the parser's
// language code to a human-readable string with idiomatic error examples.
func TestDetectLanguageHint(t *testing.T) {
	cases := []struct {
		name string
		logs string
		want string
	}{
		{"python KeyError", `Traceback (most recent call last):
  File "x.py", line 1
KeyError: 'foo'`, "Python"},
		{"go panic", `panic: runtime error: index out of range
goroutine 1 [running]:
main.foo()
        /app/main.go:10`, "Go"},
		{"java NPE", `Exception in thread "main" java.lang.NullPointerException
        at com.acme.Foo.bar(Foo.java:10)`, "Java"},
		{"javascript TypeError", `/app/index.js:10
TypeError: Cannot read property 'x' of undefined`, "JavaScript/TypeScript"},
		{"rust panic", `thread 'main' panicked at src/main.rs:10:
called Option::unwrap() on a None value`, "Rust"},
		{"ruby NoMethodError", `app.rb:10:in ` + "`foo'" + `: undefined method 'bar' (NoMethodError)`, "Ruby"},
		{"c sigsegv", `Segmentation fault (core dumped)
SIGSEGV`, "C/C++"},
		{"empty", "", ""},
		{"unparseable", "no error here", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectLanguageHint(c.logs)
			if c.want == "" {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("expected %q prefix, got %q", c.want, got)
			}
		})
	}
}

// TestToStringSlice verifies that non-string entries are skipped.
func TestToStringSlice(t *testing.T) {
	in := []any{"a", 42, "b", nil, "c", true}
	got := toStringSlice(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestKeysOfAny verifies that all keys are returned.
func TestKeysOfAny(t *testing.T) {
	m := map[string]any{"a": 1, "b": 2, "c": 3}
	got := keysOfAny(m)
	if len(got) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, k := range got {
		seen[k] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("missing key %q in %v", want, got)
		}
	}
}

// TestIsRepoAllowed verifies the allowlist logic: empty allowlist means allow
// all, non-empty means exact match (case-insensitive, .git suffix stripped).
func TestIsRepoAllowed(t *testing.T) {
	a := &Investigator{}

	// Empty allowlist → allow all.
	if !a.isRepoAllowed("anything/goes") {
		t.Error("empty allowlist should allow all repos")
	}

	a.allowedRepos = []string{"Org/Repo", "another/repo.git"}
	cases := []struct {
		repo string
		want bool
	}{
		{"org/repo", true},        // case-insensitive
		{"ORG/REPO", true},        // case-insensitive
		{"org/repo.git", true},    // .git suffix stripped
		{"another/repo", true},    // .git suffix in allowlist stripped
		{"org/other", false},      // not in allowlist
		{"", false},               // empty repo
		{"org/repo/extra", false}, // partial match doesn't count
	}
	for _, c := range cases {
		if got := a.isRepoAllowed(c.repo); got != c.want {
			t.Errorf("isRepoAllowed(%q) = %v, want %v", c.repo, got, c.want)
		}
	}
}

// TestExtractPodLogs verifies that the cluster summary + anomaly explanations
// are concatenated.
func TestExtractPodLogs(t *testing.T) {
	a := &Investigator{}
	cluster := schemas.AnomalyCluster{
		Summary: "api is crashing",
		Anomalies: []schemas.Anomaly{
			{Explanation: "CrashLoopBackOff: TypeError at app/main.py:10"},
			{Explanation: "Pod has restarted 5 times"},
		},
	}
	logs := a.extractPodLogs(cluster)
	if !strings.Contains(logs, "api is crashing") {
		t.Errorf("expected summary in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "CrashLoopBackOff") {
		t.Errorf("expected first anomaly in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "restarted 5 times") {
		t.Errorf("expected second anomaly in logs, got: %s", logs)
	}
}

// fakeVectorStore is a test double for VectorStore.
type fakeVectorStore struct {
	results      []*VectorMetadata
	scores       []float32
	err          error
	hashFallback bool
}

func (f *fakeVectorStore) Search(_ context.Context, _ string, _ int) ([]*VectorMetadata, []float32, error) {
	return f.results, f.scores, f.err
}

func (f *fakeVectorStore) UsingHashFallback() bool { return f.hashFallback }

// TestApplyKnownFix verifies the memory-loop fast path: a high-similarity
// past incident is applied directly, skipping the LLM-driven investigation.
func TestApplyKnownFix(t *testing.T) {
	a := &Investigator{}
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
	}
	meta := &VectorMetadata{
		IncidentID: "inc-123",
		RootCause:  "missing key 'foo' in config",
		FixType:    "code_fix",
		FixPattern: "use config.get(key)",
		Outcome:    "success",
	}
	podLogs := `Traceback (most recent call last):
  File "app/main.py", line 70, in get_config
    value = config['foo'].upper()
KeyError: 'foo'`

	if err := a.applyKnownFix(context.Background(), state, meta, podLogs); err != nil {
		t.Fatalf("applyKnownFix failed: %v", err)
	}
	if state.RootCause == nil {
		t.Fatal("expected root cause to be set from memory")
	}
	if state.RootCause.Statement != meta.RootCause {
		t.Errorf("root cause = %q, want %q", state.RootCause.Statement, meta.RootCause)
	}
	if state.RootCause.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", state.RootCause.Confidence)
	}
	if state.Status != schemas.StatusRootCaused {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusRootCaused)
	}
	if len(state.Hypotheses) != 1 || state.Hypotheses[0].HID != "H1" {
		t.Errorf("expected 1 hypothesis H1, got %v", state.Hypotheses)
	}
	if len(state.Hypotheses[0].SourceFiles) != 1 || state.Hypotheses[0].SourceFiles[0] != "app/main.py" {
		t.Errorf("expected source file app/main.py from parsed error, got %v", state.Hypotheses[0].SourceFiles)
	}
	if state.Iterations != 1 {
		t.Errorf("Iterations = %v, want 1", state.Iterations)
	}
}

// TestRun_NoRepoRunsRunsRunWithoutRepo verifies the runWithoutRepo path:
// when no repo annotation is set, the investigator calls the LLM directly
// with just the pod logs.
func TestRun_NoRepoAnnotation_FallsBackToRunWithoutRepo(t *testing.T) {
	// Mock LLM: returns 2 hypotheses in JSON.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"hypotheses\":[{\"statement\":\"H1 stmt\",\"confidence\":0.7},{\"statement\":\"H2 stmt\",\"confidence\":0.3}]}"}}]}`)
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	inv := NewInvestigator(router, nil, "", nil)

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Summary:   "CrashLoopBackOff",
			Anomalies: []schemas.Anomaly{{
				Explanation: `Traceback (most recent call last):
  File "app/main.py", line 70
KeyError: 'foo'`,
			}},
		},
	}

	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("investigator Run failed: %v", err)
	}
	if len(state.Hypotheses) != 2 {
		t.Fatalf("expected 2 hypotheses, got %d", len(state.Hypotheses))
	}
	if state.Hypotheses[0].HID != "H1" || state.Hypotheses[0].Statement != "H1 stmt" {
		t.Errorf("H1 mismatch: %+v", state.Hypotheses[0])
	}
	if state.Hypotheses[0].Confidence != 0.7 {
		t.Errorf("H1 confidence = %v, want 0.7", state.Hypotheses[0].Confidence)
	}
	// CRITICAL: the v1 fix — runWithoutRepo must NOT increment state.Iterations.
	if state.Iterations != 0 {
		t.Errorf("state.Iterations = %d, want 0 (pipeline.Run owns iteration increment)", state.Iterations)
	}
}

// TestRun_RunWithoutRepo_LLMFailure verifies that an LLM failure transitions
// state to StatusFailed with an explanatory error message (non-fatal return).
func TestRun_RunWithoutRepo_LLMFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	}))
	defer server.Close()

	router := llm.New(server.URL, "bad-key", "m", "f")
	router.SetRetryConfig(0, 0) // don't retry — fail fast
	inv := NewInvestigator(router, nil, "", nil)

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Anomalies: []schemas.Anomaly{{Explanation: "KeyError: foo"}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run should not return an error for LLM failure: %v", err)
	}
	if state.Status != schemas.StatusFailed {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusFailed)
	}
	if state.Error == nil || !strings.Contains(*state.Error, "investigator_llm_failure") {
		t.Errorf("expected investigator_llm_failure in state.Error, got %v", state.Error)
	}
}

// TestRun_RunWithoutRepo_LLMReturnsZeroHypotheses verifies that an empty
// hypotheses list from the LLM leaves state.Hypotheses empty (no panic).
func TestRun_RunWithoutRepo_LLMReturnsZeroHypotheses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"hypotheses\":[]}"}}]}`)
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	inv := NewInvestigator(router, nil, "", nil)

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Anomalies: []schemas.Anomaly{{Explanation: "KeyError: foo"}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(state.Hypotheses) != 0 {
		t.Errorf("expected 0 hypotheses, got %d", len(state.Hypotheses))
	}
}

// TestRun_RepoNotAllowedFallsBackToRunWithoutRepo verifies the allowlist
// rejection path: a repo not in the allowlist falls back to runWithoutRepo.
func TestRun_RepoNotAllowedFallsBackToRunWithoutRepo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"hypotheses\":[{\"statement\":\"fallback\",\"confidence\":0.4}]}"}}]}`)
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	inv := NewInvestigator(router, nil, "", nil)
	inv.SetAllowedRepos([]string{"allowed/repo-only"})

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Annotations: map[string]string{
				"sentinel.io/repo": "attacker/controlled-repo",
			},
			Anomalies: []schemas.Anomaly{{Explanation: "KeyError: foo"}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Should have fallen back to runWithoutRepo and produced a hypothesis.
	if len(state.Hypotheses) != 1 {
		t.Errorf("expected 1 hypothesis from fallback path, got %d", len(state.Hypotheses))
	}
}

// TestRun_MemoryLoop_HighMatch_AppliesKnownFix verifies that the memory-loop
// fast path is taken when vector store returns a high-similarity successful
// past incident with real (non-hash) embeddings.
func TestRun_MemoryLoop_HighMatch_AppliesKnownFix(t *testing.T) {
	store := &fakeVectorStore{
		results: []*VectorMetadata{{
			IncidentID: "inc-abc",
			RootCause:  "missing env var",
			FixType:    "code_fix",
			FixPattern: "use os.getenv",
			Outcome:    "success",
		}},
		scores:       []float32{0.9},
		hashFallback: false,
	}
	// Router is unused — applyKnownFix doesn't call the LLM.
	inv := NewInvestigator(llm.New("", "", "m", "f"), nil, "", store)

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Anomalies: []schemas.Anomaly{{
				Explanation: `Traceback (most recent call last):
  File "app/main.py", line 10
KeyError: 'DATABASE_URL'`,
			}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if state.RootCause == nil || state.RootCause.Statement != "missing env var" {
		t.Errorf("expected root cause from memory, got %+v", state.RootCause)
	}
	if state.Status != schemas.StatusRootCaused {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusRootCaused)
	}
}

// TestRun_MemoryLoop_HighMatch_HashFallback_SkipsApplyKnownFix verifies that
// a high-similarity match with hash-embedding fallback does NOT take the
// applyKnownFix fast path (hash embeddings can produce false high-similarity).
func TestRun_MemoryLoop_HighMatch_HashFallback_SkipsApplyKnownFix(t *testing.T) {
	store := &fakeVectorStore{
		results: []*VectorMetadata{{
			IncidentID: "inc-abc",
			RootCause:  "missing env var",
			FixType:    "code_fix",
			FixPattern: "use os.getenv",
			Outcome:    "success",
		}},
		scores:       []float32{0.9},
		hashFallback: true,
	}
	// Router returns no hypotheses — confirms the LLM path was taken (not the
	// memory fast path, which would skip the LLM).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"hypotheses\":[]}"}}]}`)
	}))
	defer server.Close()

	inv := NewInvestigator(llm.New(server.URL, "k", "m", "f"), nil, "", store)

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Anomalies: []schemas.Anomaly{{Explanation: "KeyError: 'DATABASE_URL'"}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Memory fast path NOT taken — root cause should NOT be set.
	if state.RootCause != nil {
		t.Errorf("expected root cause to NOT be set with hash fallback, got %+v", state.RootCause)
	}
}

// TestRun_VectorStoreError verifies that a vector-store error is swallowed
// (logged) and the investigation continues via the LLM path.
func TestRun_VectorStoreError(t *testing.T) {
	store := &fakeVectorStore{err: errors.New("vector store offline")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"hypotheses\":[{\"statement\":\"LLM-derived\",\"confidence\":0.5}]}"}}]}`)
	}))
	defer server.Close()

	inv := NewInvestigator(llm.New(server.URL, "k", "m", "f"), nil, "", store)
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Anomalies: []schemas.Anomaly{{Explanation: "KeyError: 'foo'"}},
		},
	}
	if err := inv.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(state.Hypotheses) != 1 {
		t.Errorf("expected 1 hypothesis from LLM path, got %d", len(state.Hypotheses))
	}
}
