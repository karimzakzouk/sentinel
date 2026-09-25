package main

import (
	"sync"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/config"
)

// The fix-service budget (SENTINEL_MAX_FIX_SERVICES) gates the FIRST thing
// handleInvestigation does, before any LLM work — these tests pin its
// semantics so a refactor can't silently turn the cost cap into a no-op
// (the pool spent three chart versions as exactly that kind of phantom).
func newBudgetWiring(cap int) *wiring {
	return &wiring{cfg: &config.Config{MaxFixServices: cap}}
}

func TestClaimFixBudgetDisabled(t *testing.T) {
	w := newBudgetWiring(0)
	for i := 0; i < 10; i++ {
		if !w.claimFixBudget("search-api", "auto-detected") {
			t.Fatalf("claim %d denied with budget disabled", i)
		}
	}
}

func TestClaimFixBudgetCapsDistinctServices(t *testing.T) {
	w := newBudgetWiring(2)
	for _, svc := range []string{"search-api", "user-api"} {
		if !w.claimFixBudget(svc, "auto-detected") {
			t.Fatalf("claim for %s denied before cap reached", svc)
		}
	}
	if w.claimFixBudget("shipping-api", "auto-detected") {
		t.Fatal("third distinct service claimed with cap=2")
	}
}

func TestClaimFixBudgetOneAttemptPerService(t *testing.T) {
	w := newBudgetWiring(3)
	if !w.claimFixBudget("search-api", "auto-detected") {
		t.Fatal("first claim denied")
	}
	// Repeat fires (re-detection waves near soak end) must not re-enter
	// the pipeline: the first attempt already spent the tokens.
	if w.claimFixBudget("search-api", "auto-detected") {
		t.Fatal("repeat claim for same service allowed")
	}
	if w.claimFixBudget("search-api", "slo") {
		t.Fatal("repeat claim via different automated trigger allowed")
	}
	// The failed claim must not have consumed another slot.
	if !w.claimFixBudget("user-api", "auto-detected") {
		t.Fatal("repeat claim poisoned an unused slot")
	}
}

func TestClaimFixBudgetOperatorIntentBypasses(t *testing.T) {
	w := newBudgetWiring(1)
	if !w.claimFixBudget("search-api", "auto-detected") {
		t.Fatal("first claim denied")
	}
	for _, trigger := range []string{"manual", "slack", "chaos", "incident-42"} {
		if !w.claimFixBudget("shipping-api", trigger) {
			t.Fatalf("operator-intent trigger %q denied at cap", trigger)
		}
	}
}

func TestClaimFixBudgetAutomatedTriggersCapped(t *testing.T) {
	w := newBudgetWiring(1)
	if !w.claimFixBudget("search-api", "auto-detected") {
		t.Fatal("first claim denied")
	}
	for _, trigger := range []string{"auto-detected", "slo", "alertmanager", ""} {
		if w.claimFixBudget("user-api", trigger) {
			t.Fatalf("automated trigger %q bypassed the cap", trigger)
		}
	}
}

func TestClaimFixBudgetConcurrent(t *testing.T) {
	// The detector fires clusters from multiple goroutines; the claim must
	// be atomic or two goroutines can both "win" the last slot (and the
	// run-#20 token blowup happens through the back door).
	const cap = 3
	w := newBudgetWiring(cap)
	services := []string{"a", "b", "c", "d", "e", "f"}
	var mu sync.Mutex
	granted := 0
	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			if w.claimFixBudget(s, "auto-detected") {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(svc)
	}
	wg.Wait()
	if granted != cap {
		t.Fatalf("granted %d claims under cap=%d, want exactly %d", granted, cap, cap)
	}
}
