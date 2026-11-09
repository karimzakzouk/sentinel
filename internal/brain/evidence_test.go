package brain

import (
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

// A logged exception's first type is its whole masked message, hundreds of
// characters for a Redis client's timeout; the account names the root cause
// and stays one readable line.
func TestDescribeLogsStaysReadable(t *testing.T) {
	long := "Error status code 'FailedPrecondition' with detail 'Can't access cart storage. " + strings.Repeat("inst: <num>, qu: <num>, ", 30)
	ev := watcher.LogEvidence{Reason: watcher.ReasonSample, Pod: "cart-1", Container: "server", Summary: logs.Summary{Lines: 113,
		Templates: []logs.TemplateCount{{Template: long, Count: 48, Error: true}},
		Exceptions: []logs.ExceptionCount{{Count: 3,
			Exception: logs.Exception{Types: []string{long, "StackExchange.Redis.RedisConnectionException"}}}}}}
	got := describeLogs(ev)
	if !strings.Contains(got, "StackExchange.Redis.RedisConnectionException ×3") || strings.Count(got, "inst:") > 10 || len(got) > 400 {
		t.Fatalf("describeLogs = %q (%d bytes)", got, len(got))
	}
}

// Signals in an incident's logs become facts its next assessment weighs:
// errors about names that do not resolve make a DNS failure.
func TestSignalsInLogsTellTheClass(t *testing.T) {
	b, _ := newBrain(t)
	inc := &incident{primary: web, facts: map[string]int{}, symptomFacts: map[string]map[string]int{}}
	ev := watcher.LogEvidence{Workload: web, Summary: logs.Summary{Templates: []logs.TemplateCount{
		{Template: "dial tcp: lookup payments on <ip>: no such host", Count: 7, Error: true},
		{Template: "GET / 200", Count: 50},
	}}}
	b.noteSignals(inc, web.String(), ev)
	if inc.facts["dns.failure"] != 7 || len(inc.facts) != 1 {
		t.Fatalf("facts = %v", inc.facts)
	}
	other := model.ObjectRef{Kind: "Deployment", Namespace: "shop", Name: "front"}
	ev.Workload = other
	b.noteSignals(inc, other.String(), ev)
	if inc.symptomFacts[other.String()]["dns.failure"] != 7 {
		t.Fatalf("symptom facts = %v", inc.symptomFacts)
	}
}
