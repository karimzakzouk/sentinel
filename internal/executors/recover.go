package executors

import "github.com/karimzakzouk/sentinel/internal/logging"

// agentsRecover is a deferred panic-recovery helper for sandbox goroutines.
// It mirrors agents.recoverPanic but lives here so we don't take an import
// dependency on the agents package just for one helper.
func agentsRecover(name string) {
	if r := recover(); r != nil {
		logging.Error("executor panic recovered", "component", name, "panic", r)
	}
}
