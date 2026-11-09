package brain

import (
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
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
