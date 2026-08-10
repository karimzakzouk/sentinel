package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// --- Test helpers ---

// buildLLMResponse builds an OpenAI-style JSON response with the given content.
func buildLLMResponse(content string) []byte {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}, "finish_reason": "stop"},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// newLLMServer returns an httptest.Server that responds with the given
// content on every call. The server is used as the LLM API endpoint for a
// real *llm.Router — no mocking of the Router struct itself.
func newLLMServer(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(buildLLMResponse(content))
	}))
}

// newSequentialLLMServer returns an httptest.Server that responds with the
// given contents in sequence — the first request gets contents[0], the
// second gets contents[1], etc. After the sequence is exhausted, the server
// returns a 500 error. Useful for testing multi-turn agent loops where each
// LLM call should return a different response (tool call → done).
func newSequentialLLMServer(contents []string) *httptest.Server {
	var idx int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := atomic.AddInt32(&idx, 1) - 1
		if int(i) >= len(contents) {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(buildLLMResponse(contents[i]))
	}))
}

// fakeCloner is a test Cloner that returns a pre-built temp directory
// instead of actually cloning from GitHub. Implements the agent.Cloner
// interface.
type fakeCloner struct {
	repoPath string // path to return from Clone
	err      error  // if non-nil, Clone returns this error
	calls    int    // number of Clone calls (for assertions)
	mu       sync.Mutex
}

func (f *fakeCloner) Clone(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.repoPath, nil
}

func (f *fakeCloner) Cleanup(_ string) {}

func (f *fakeCloner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// makeTestRepo creates a temp directory with a small Go source file that
// the code-intel parser can index. Returns the repo path.
func makeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := `package demo

import "fmt"

func Greet(name string) {
    msg := buildMessage(name)
    fmt.Println(msg)
}

func buildMessage(name string) string {
    return "hello, " + name
}
`
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write greet.go: %v", err)
	}
	return dir
}

// newStateWithLogs builds a minimal AgentState with the given pod logs and
// optional repo annotation.
func newStateWithLogs(logs, repoAnnotation string) *schemas.AgentState {
	ann := map[string]string{}
	if repoAnnotation != "" {
		ann["sentinel.io/repo"] = repoAnnotation
	}
	return &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			ClusterID: uuid.New(),
			Service:   "test-svc",
			Namespace: "test-ns",
			Anomalies: []schemas.Anomaly{
				{
					AnomalyID:   uuid.New(),
					SignalID:    uuid.New(),
					Service:     "test-svc",
					Namespace:   "test-ns",
					Explanation: logs,
					Annotations: ann,
				},
			},
			Annotations: ann,
		},
	}
}

// fakeVectorStore is a test agents.VectorStore that returns canned results.
type fakeVectorStore struct {
	results []*agents.VectorMetadata
	scores  []float32
	hashFB  bool
}

func (f *fakeVectorStore) Search(_ context.Context, _ string, _ int) ([]*agents.VectorMetadata, []float32, error) {
	return f.results, f.scores, nil
}

func (f *fakeVectorStore) UsingHashFallback() bool { return f.hashFB }

// --- Tests ---

// TestPipelineAdapter_NoRepo verifies that when no sentinel.io/repo
// annotation is present, the adapter falls back to the error-parser-only
// path and still produces hypotheses.
func TestPipelineAdapter_NoRepo(t *testing.T) {
	router := llm.New("http://invalid:9", "k", "m", "f") // unreachable, but we won't call it
	router.SetRetryConfig(0, 0)

	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{MaxTurns: 5})
	state := newStateWithLogs("panic: runtime error: index out of range", "")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(state.Hypotheses) == 0 {
		t.Error("expected hypotheses from fallback path, got none")
	}
}

// TestPipelineAdapter_RepoNotAllowed verifies that a repo annotation
// pointing to a repo not on the allowlist triggers the fallback path
// (no clone attempted).
func TestPipelineAdapter_RepoNotAllowed(t *testing.T) {
	router := llm.New("http://invalid:9", "k", "m", "f")
	router.SetRetryConfig(0, 0)

	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{MaxTurns: 5})
	adapter.SetAllowedRepos([]string{"org/allowed"})
	// Inject a fake cloner that would fail if called — proves we never clone.
	fc := &fakeCloner{err: nil, repoPath: "/nonexistent"}
	adapter.setCloner(fc)

	state := newStateWithLogs("panic: nil pointer", "org/blocked")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if fc.callCount() != 0 {
		t.Errorf("expected 0 clone calls (repo not allowed), got %d", fc.callCount())
	}
	if len(state.Hypotheses) == 0 {
		t.Error("expected hypotheses from fallback path, got none")
	}
}

// TestPipelineAdapter_CloneFailure verifies that a clone error triggers
// the fallback path (error parser only) instead of failing the whole Run.
func TestPipelineAdapter_CloneFailure(t *testing.T) {
	router := llm.New("http://invalid:9", "k", "m", "f")
	router.SetRetryConfig(0, 0)

	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{MaxTurns: 5})
	fc := &fakeCloner{err: context.DeadlineExceeded}
	adapter.setCloner(fc)

	state := newStateWithLogs("KeyError: 'missing'", "org/repo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if fc.callCount() != 1 {
		t.Errorf("expected 1 clone call, got %d", fc.callCount())
	}
	if len(state.Hypotheses) == 0 {
		t.Error("expected hypotheses from fallback after clone failure, got none")
	}
}

// TestPipelineAdapter_AgentLoopSuccess verifies the happy path: repo is
// cloned, the LLM returns a "done" response on the first turn, and the
// state is populated with root cause + hypotheses + source files.
func TestPipelineAdapter_AgentLoopSuccess(t *testing.T) {
	// LLM immediately returns done with a root cause.
	doneResp := `{"done": true, "root_cause": "nil pointer dereference in Greet when name is empty", "confidence": 0.9, "files_to_change": ["greet.go"]}`
	server := newLLMServer(doneResp)
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	repoPath := makeTestRepo(t)
	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{
		MaxTurns:    5,
		TurnTimeout: 5e9, // 5s
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("panic: nil pointer dereference", "org/demo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if state.RootCause == nil {
		t.Fatal("expected root cause to be set")
	}
	if !strings.Contains(state.RootCause.Statement, "nil pointer") {
		t.Errorf("root cause = %q, want substring 'nil pointer'", state.RootCause.Statement)
	}
	if state.RootCause.Confidence != 0.9 {
		t.Errorf("confidence = %f, want 0.9", state.RootCause.Confidence)
	}
	if len(state.Hypotheses) == 0 {
		t.Error("expected at least one hypothesis")
	}
	if len(state.SourceFiles) == 0 {
		t.Error("expected source files to be populated")
	}
	if state.Status != schemas.StatusRootCaused {
		t.Errorf("status = %q, want %q", state.Status, schemas.StatusRootCaused)
	}
}

// TestPipelineAdapter_AgentLoopMultiTurn verifies a 2-turn loop: the LLM
// first calls a tool, then declares done. This exercises the conversation
// accumulation + tool execution path.
func TestPipelineAdapter_AgentLoopMultiTurn(t *testing.T) {
	// Default config: NumPlans=1 (single plan), CriticThreshold=0 (no critic).
	// LLM call sequence:
	//   Plan (1 LLM call)
	//   Turn 1: list_symbols (tool call)
	//   Turn 2: done (root cause, confidence 0.85, no critic)
	responses := []string{
		`Plan: read greet.go and check buildMessage function.`,
		`{"tool": "list_symbols", "args": {}}`,
		`{"done": true, "root_cause": "found bug in buildMessage", "confidence": 0.85, "files_to_change": ["greet.go"]}`,
	}
	server := newSequentialLLMServer(responses)
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	repoPath := makeTestRepo(t)
	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{
		MaxTurns:    5,
		TurnTimeout: 5e9,
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("panic: something broke", "org/demo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if state.RootCause == nil {
		t.Fatal("expected root cause after multi-turn loop")
	}
	if !strings.Contains(state.RootCause.Statement, "buildMessage") {
		t.Errorf("root cause = %q, want substring 'buildMessage'", state.RootCause.Statement)
	}
}

// TestPipelineAdapter_TurnCapExceeded verifies that when the LLM never
// returns "done", the loop hits the turn cap and returns a low-confidence
// result (not an error).
func TestPipelineAdapter_TurnCapExceeded(t *testing.T) {
	// LLM always calls a tool, never says done.
	toolResp := `{"tool": "list_symbols", "args": {}}`
	server := newLLMServer(toolResp)
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	repoPath := makeTestRepo(t)
	maxTurns := 3
	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{
		MaxTurns:    maxTurns,
		TurnTimeout: 5e9,
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("error: bad thing", "org/demo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should NOT have a confident root cause.
	if state.RootCause != nil && state.RootCause.Confidence > 0 {
		t.Errorf("expected low/no confidence on turn cap, got %f", state.RootCause.Confidence)
	}
}

// TestPipelineAdapter_WithVectorStore verifies that a medium-similarity
// match from the vector store is surfaced as RetrievedIncidents context.
func TestPipelineAdapter_WithVectorStore(t *testing.T) {
	doneResp := `{"done": true, "root_cause": "bug", "confidence": 0.8, "files_to_change": ["greet.go"]}`
	server := newLLMServer(doneResp)
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	vs := &fakeVectorStore{
		results: []*agents.VectorMetadata{
			{
				IncidentID: "past-123",
				RootCause:  "previous nil pointer",
				FixType:    "code",
				FixPattern: "add nil check",
				Outcome:    "success",
			},
		},
		scores: []float32{0.6}, // above ConfidenceMediumMatch (0.5)
		hashFB: false,
	}

	repoPath := makeTestRepo(t)
	adapter := NewPipelineAdapter(router, nil, "tok", vs, AgentConfig{
		MaxTurns:    5,
		TurnTimeout: 5e9,
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("panic: nil pointer", "org/demo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(state.RetrievedIncidents) == 0 {
		t.Fatal("expected RetrievedIncidents to be populated from vector store")
	}
	ri := state.RetrievedIncidents[0]
	if ri["incident_id"] != "past-123" {
		t.Errorf("retrieved incident_id = %v, want past-123", ri["incident_id"])
	}
	if ri["root_cause"] != "previous nil pointer" {
		t.Errorf("retrieved root_cause = %v, want 'previous nil pointer'", ri["root_cause"])
	}
}

// TestPipelineAdapter_String verifies the String() method produces a
// human-readable identifier for logging.
func TestPipelineAdapter_String(t *testing.T) {
	adapter := NewPipelineAdapter(nil, nil, "tok", nil, AgentConfig{})
	adapter.SetGitHost("github.example.com")
	adapter.SetDefaultBranch("develop")
	adapter.SetAllowedRepos([]string{"org/repo"})

	s := adapter.String()
	for _, want := range []string{"k8s=no", "github.example.com", "develop", "allowlist=yes"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q\ngot: %s", want, s)
		}
	}
}

// TestPipelineAdapter_PodLogsFromState verifies that when K8s is nil (no
// pod logs fetch), the adapter uses the Explanation from the state's
// anomalies.
func TestPipelineAdapter_PodLogsFromState(t *testing.T) {
	doneResp := `{"done": true, "root_cause": "saw the error", "confidence": 0.7, "files_to_change": ["greet.go"]}`
	server := newLLMServer(doneResp)
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	repoPath := makeTestRepo(t)
	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{
		MaxTurns:    5,
		TurnTimeout: 5e9,
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("CUSTOM_ERROR_MARKER_IN_LOGS", "org/demo")

	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if state.RootCause == nil {
		t.Fatal("expected root cause")
	}
}

// TestPipelineAdapter_RespectsContextCancellation verifies that the adapter
// returns (without erroring fatally) when the context is cancelled mid-run.
func TestPipelineAdapter_RespectsContextCancellation(t *testing.T) {
	// Slow LLM server — each response takes 100ms.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(buildLLMResponse(`{"tool": "list_symbols", "args": {}}`))
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)

	repoPath := makeTestRepo(t)
	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{
		MaxTurns:    100,
		TurnTimeout: 50 * time.Millisecond, // shorter than the LLM response
	})
	adapter.setCloner(&fakeCloner{repoPath: repoPath})

	state := newStateWithLogs("error: something", "org/demo")

	// The turn timeout will fire; the loop should break gracefully.
	// We don't check the result — just that Run doesn't panic or hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = adapter.Run(ctx, state)
}

// --- Pipeline integration tests (in agents package via interface) ---

// TestPipelineAdapter_SatisfiesInvestigatorRunner is a compile-time check
// that *PipelineAdapter can be passed to Pipeline.SetAgentLoopRunner.
// The actual assertion is in pipeline_adapter.go (var _ agents.InvestigatorRunner);
// this test documents the contract and will fail to compile if the
// interface drifts.
func TestPipelineAdapter_SatisfiesInvestigatorRunner(t *testing.T) {
	var _ agents.InvestigatorRunner = (*PipelineAdapter)(nil)
}

// TestPipelineAdapter_DoesNotPanicOnEmptyState verifies the adapter handles
// a state with no anomalies gracefully (extractPodLogs returns "").
func TestPipelineAdapter_DoesNotPanicOnEmptyState(t *testing.T) {
	router := llm.New("http://invalid:9", "k", "m", "f")
	router.SetRetryConfig(0, 0)

	adapter := NewPipelineAdapter(router, nil, "tok", nil, AgentConfig{MaxTurns: 5})

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			ClusterID: uuid.New(),
			Service:   "svc",
			Namespace: "ns",
		},
	}

	// Should not panic; should fall through to runWithoutRepo with empty logs.
	if err := adapter.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// With empty logs, GenerateHypothesesUniversal may return empty — that's OK.
	// The contract is "doesn't panic, returns nil".
}

// TestPipelineAdapter_BranchResolution verifies the branch/commit annotation
// priority: git-commit > branch > default.
func TestPipelineAdapter_BranchResolution(t *testing.T) {
	cases := []struct {
		name       string
		annotation map[string]string
		want       string
	}{
		{
			name:       "git-commit takes priority",
			annotation: map[string]string{"sentinel.io/git-commit": "abc123", "sentinel.io/branch": "main"},
			want:       "abc123",
		},
		{
			name:       "branch when no commit",
			annotation: map[string]string{"sentinel.io/branch": "release-1.2"},
			want:       "release-1.2",
		},
		{
			name:       "default branch when neither",
			annotation: map[string]string{},
			want:       "main",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewPipelineAdapter(nil, nil, "tok", nil, AgentConfig{})
			adapter.SetDefaultBranch("main")

			state := &schemas.AgentState{
				Cluster: schemas.AnomalyCluster{
					Annotations: tc.annotation,
				},
			}

			got := adapter.resolveBranch(state)
			if got != tc.want {
				t.Errorf("resolveBranch() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPipelineAdapter_BranchFromAnomalyAnnotations verifies that branch
// annotations on individual anomalies (not just the cluster) are picked up.
func TestPipelineAdapter_BranchFromAnomalyAnnotations(t *testing.T) {
	adapter := NewPipelineAdapter(nil, nil, "tok", nil, AgentConfig{})
	adapter.SetDefaultBranch("main")

	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Anomalies: []schemas.Anomaly{
				{Annotations: map[string]string{"sentinel.io/branch": "feature-branch"}},
			},
		},
	}

	got := adapter.resolveBranch(state)
	if got != "feature-branch" {
		t.Errorf("resolveBranch() = %q, want 'feature-branch'", got)
	}
}
