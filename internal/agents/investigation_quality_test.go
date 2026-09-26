package agents

// investigation_quality_test.go (v1.7.16): pins the "mechanism over
// symptom" behavior added after run #29's 1/9 postmortem — five of six
// fixes were symptom-level patches at crash sites that were merely
// victims of the real mechanism (missing retry policy, unbounded
// growth, lock protocol). These tests pin:
//
//   - crash vitals rendering (restarts, OOMKilled, exit codes)
//   - the OOM interpretation guidance that redirects the LLM from the
//     crash line to the growth site
//   - deterministic search-query enrichment (leak + retry vocabulary)
//   - fault-class / fix-layer flow into the fix proposer's prompt

import (
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// oomCrashVitals reproduces the user-api crash shape from runs #26-#29:
// OOMKilled under a 256Mi limit, restart loop, JS heap exhaustion line.
func oomCrashVitals() *schemas.PodRuntimeContext {
	return &schemas.PodRuntimeContext{
		PodName:                 "user-api-86cc7c8bf7-7bqvf",
		ContainerName:           "user-api",
		EnvVarNames:             []string{"USER_DATABASE_URL"},
		MemoryLimit:             "256Mi",
		RestartCount:            5,
		LastTerminationReason:   "OOMKilled",
		LastTerminationExitCode: 137,
		WaitingReason:           "CrashLoopBackOff",
		OOMKilled:               true,
	}
}

func TestRenderRuntimeContextCrashVitals(t *testing.T) {
	s := renderRuntimeContext(oomCrashVitals())
	mustContain := []string{
		"restarts: 5",
		"RECURRING",
		"reason=OOMKilled",
		"exit_code=137",
		"CrashLoopBackOff",
	}
	for _, sub := range mustContain {
		if !strings.Contains(s, sub) {
			t.Errorf("crash vitals missing %q:\n%s", sub, s)
		}
	}
}

func TestRenderRuntimeContextOOMGuidance(t *testing.T) {
	s := renderRuntimeContext(oomCrashVitals())
	mustContain := []string{
		"OOM EVIDENCE",
		"VICTIM",
		"bounding the growth will",
	}
	for _, sub := range mustContain {
		if !strings.Contains(s, sub) {
			t.Errorf("OOM guidance missing %q:\n%s", sub, s)
		}
	}
}

func TestRenderRuntimeContextNoOOMGuidanceWithoutKill(t *testing.T) {
	rc := run11UserAPIContext() // no vitals set
	s := renderRuntimeContext(rc)
	if strings.Contains(s, "OOM EVIDENCE") {
		t.Errorf("OOM guidance must not render without an OOM kill:\n%s", s)
	}
	if strings.Contains(s, "restarts:") {
		t.Errorf("restart line must not render when there are no restarts:\n%s", s)
	}
}

func TestRenderRuntimeContextSingleRestartNoRecurringNote(t *testing.T) {
	rc := run11UserAPIContext()
	rc.RestartCount = 1
	s := renderRuntimeContext(rc)
	if !strings.Contains(s, "restarts: 1") {
		t.Errorf("expected 'restarts: 1', got:\n%s", s)
	}
	if strings.Contains(s, "RECURRING") {
		t.Errorf("single restart is not yet a pattern — RECURRING note should be absent:\n%s", s)
	}
}

func TestEnrichSearchQueriesOOMShape(t *testing.T) {
	logs := "<--- Last few GCs --->\nFATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory\n"
	rc := &schemas.PodRuntimeContext{OOMKilled: true}
	got := enrichSearchQueries([]any{"db.ts"}, logs, rc)
	joined := strings.Join(toStringSlice(got), "||")
	for _, want := range []string{"db.ts", "cache|Cache", "token|session", "evict"} {
		if !strings.Contains(joined, want) {
			t.Errorf("OOM enrichment missing %q in %v", want, got)
		}
	}
}

func TestEnrichSearchQueriesTransientShape(t *testing.T) {
	logs := "Traceback (most recent call last):\n  File \"app/tasks.py\", line 42, in send_email\nsocket.gaierror: [Errno -2] Name or service not known\n"
	got := enrichSearchQueries([]any{"email_client"}, logs, nil)
	joined := strings.Join(toStringSlice(got), "||")
	for _, want := range []string{"email_client", "retry|retries|max_retries|maxRetries|MAX_RETRIES", "autoretry|retry_backoff"} {
		if !strings.Contains(joined, want) {
			t.Errorf("transient enrichment missing %q in %v", want, got)
		}
	}
}

func TestEnrichSearchQueriesDedupAndCap(t *testing.T) {
	logs := "KeyError: 99\n"
	llm := []any{"KeyError", "users", "config", "main", "x", "y", "z", "w"}
	got := enrichSearchQueries(llm, logs, nil)
	if len(got) > MaxSearchQueries {
		t.Errorf("enrichment exceeded cap: %d > %d", len(got), MaxSearchQueries)
	}
	seen := map[string]int{}
	for _, q := range got {
		s, _ := q.(string)
		seen[s]++
		if seen[s] > 1 {
			t.Errorf("duplicate query %q in %v", s, got)
		}
	}
	// Stack identifiers from the logs are always merged in.
	if seen["KeyError"] != 1 {
		// "KeyError" was in the LLM set AND in the log extraction — dedup means exactly one occurrence.
		t.Errorf("expected KeyError query present exactly once, got %v", got)
	}
}

func TestEnrichSearchQueriesPlainLogicErrorAddsNoHints(t *testing.T) {
	logs := `Traceback (most recent call last):
  File "app/main.py", line 70, in get_config
AttributeError: 'NoneType' object has no attribute 'upper'
`
	got := enrichSearchQueries([]any{"main.py"}, logs, nil)
	joined := strings.Join(toStringSlice(got), "||")
	for _, notWant := range []string{"cache|Cache", "token|session", "retry|retries"} {
		if strings.Contains(joined, notWant) {
			t.Errorf("logic_data fault must not add mechanism hints, found %q in %v", notWant, got)
		}
	}
}

func TestFixProposerMessageIncludesFaultClassAndFixLayer(t *testing.T) {
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "notification-worker", Namespace: "marketly"},
		RootCause: &schemas.RootCause{
			Statement:  "send_email task permanently fails on transient DNS errors because max_retries=0 and gaierror is not in the retry-exception tuple",
			Confidence: 0.75,
			FaultClass: "transient_infra",
			FixLayer:   "app/tasks.py retry policy — gaierror is transient; needs autoretry_for + retry_backoff, not validation",
		},
		FetchedCode: map[string]string{"app/tasks.py": "def send_email(): pass"},
	}

	a := &FixProposer{}
	msg := a.buildUserMessage(state)
	for _, sub := range []string{
		"- fault class: transient_infra",
		"- fix layer: app/tasks.py retry policy",
	} {
		if !strings.Contains(msg, sub) {
			t.Errorf("fix proposer message missing %q:\n%s", sub, msg)
		}
	}
}

func TestRound2PromptPinsFaultClassification(t *testing.T) {
	mustContain := []string{
		`"fault_class"`,
		`"transient_infra"`,
		`"resource_leak"`,
		`"logic_data"`,
		`"lifecycle"`,
		`"concurrency"`,
		"fix_layer",
		"MECHANISM-LEVEL",
		"a VICTIM",
	}
	for _, sub := range mustContain {
		if !strings.Contains(investigatorRound2Prompt, sub) {
			t.Errorf("round-2 prompt missing %q", sub)
		}
	}
}

func TestCrashLocationFooterDistinguishesVictimSites(t *testing.T) {
	logs := `Traceback (most recent call last):
  File "app/main.py", line 70, in get_config
AttributeError: 'NoneType' object has no attribute 'upper'
`
	out := extractCrashLocation(logs)
	if !strings.Contains(out, "IMPORTANT") {
		t.Errorf("expected IMPORTANT note, got:\n%s", out)
	}
	if !strings.Contains(out, "VICTIM") {
		t.Errorf("expected victim-site guidance in footer, got:\n%s", out)
	}
}
