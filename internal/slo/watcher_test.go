package slo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeQuerier returns configurable long/short burn rates. When err is set,
// every call returns that error.
type fakeQuerier struct {
	mu    sync.Mutex
	long  float64
	short float64
	err   error
	calls int
}

func (f *fakeQuerier) QueryBurnRate(_ context.Context, _ SLO, _, _ time.Duration) (float64, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.long, f.short, f.err
}

func (f *fakeQuerier) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeQuerier) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestWatcher returns a Watcher with a single SLO registered and the
// fake querier wired in. Only the named rule is installed so each test can
// assert against a single fired incident (or none).
func newTestWatcher(long, short float64, ruleName string) (*Watcher, *fakeQuerier) {
	q := &fakeQuerier{long: long, short: short}
	w := NewWatcher(q)
	w.RegisterSLO(SLO{
		Service:   "api",
		Namespace: "prod",
		Target:    0.999,
		Window:    30 * 24 * time.Hour,
		Query:     `sum(rate(http_requests_total{service="{{service}}",namespace="{{namespace}}",status!~"5.."}[{{window}}])) / sum(rate(http_requests_total{service="{{service}}",namespace="{{namespace}}"}[{{window}}]))`,
	})

	all := DefaultRules()
	for _, r := range all {
		if r.Name == ruleName {
			w.SetRules([]AlertRule{r})
			break
		}
	}
	if ruleName == "" {
		w.SetRules(all)
	}
	return w, q
}

// runOneTick calls Tick once and returns the incidents captured by
// OnIncident.
func runOneTick(w *Watcher) []Incident {
	var incidents []Incident
	w.OnIncident = func(i Incident) { incidents = append(incidents, i) }
	w.Tick(context.Background())
	return incidents
}

// TestSLO_BurnRate14x verifies the fast-burn page rule fires at 14.4x.
func TestSLO_BurnRate14x(t *testing.T) {
	w, _ := newTestWatcher(15.0, 15.0, "fast-burn-page")
	incidents := runOneTick(w)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	inc := incidents[0]
	if inc.RuleName != "fast-burn-page" {
		t.Errorf("expected fast-burn-page rule, got %q", inc.RuleName)
	}
	if inc.Severity != "page" {
		t.Errorf("expected page severity, got %q", inc.Severity)
	}
	if inc.Threshold != 14.4 {
		t.Errorf("expected threshold 14.4, got %f", inc.Threshold)
	}
}

// TestSLO_BurnRate6x verifies the medium-burn page rule fires at 6x.
func TestSLO_BurnRate6x(t *testing.T) {
	w, _ := newTestWatcher(7.0, 7.0, "medium-burn-page")
	incidents := runOneTick(w)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].RuleName != "medium-burn-page" {
		t.Errorf("expected medium-burn-page rule, got %q", incidents[0].RuleName)
	}
	if incidents[0].Severity != "page" {
		t.Errorf("expected page severity, got %q", incidents[0].Severity)
	}
}

// TestSLO_BurnRate3x verifies the slow-burn ticket rule fires at 3x.
func TestSLO_BurnRate3x(t *testing.T) {
	w, _ := newTestWatcher(4.0, 4.0, "slow-burn-ticket")
	incidents := runOneTick(w)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].RuleName != "slow-burn-ticket" {
		t.Errorf("expected slow-burn-ticket rule, got %q", incidents[0].RuleName)
	}
	if incidents[0].Severity != "ticket" {
		t.Errorf("expected ticket severity, got %q", incidents[0].Severity)
	}
}

// TestSLO_BurnRate1x verifies the always-on ticket rule fires at 1x.
func TestSLO_BurnRate1x(t *testing.T) {
	w, _ := newTestWatcher(2.0, 2.0, "always-on-ticket")
	incidents := runOneTick(w)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].RuleName != "always-on-ticket" {
		t.Errorf("expected always-on-ticket rule, got %q", incidents[0].RuleName)
	}
	if incidents[0].Severity != "ticket" {
		t.Errorf("expected ticket severity, got %q", incidents[0].Severity)
	}
}

// TestSLO_BelowThresholdNoFire verifies no incident fires when both windows
// are below the rule's threshold.
func TestSLO_BelowThresholdNoFire(t *testing.T) {
	w, _ := newTestWatcher(0.5, 0.5, "always-on-ticket")
	incidents := runOneTick(w)
	if len(incidents) != 0 {
		t.Errorf("expected no incident at 0.5x burn rate, got %d", len(incidents))
	}
}

// TestSLO_MultiWindowShortLowNoFire verifies that a low short-window burn
// rate suppresses firing even when the long window is high. This is the
// multi-window rule that prevents flapping on transient spikes.
func TestSLO_MultiWindowShortLowNoFire(t *testing.T) {
	w, _ := newTestWatcher(20.0, 0.5, "fast-burn-page")
	incidents := runOneTick(w)
	if len(incidents) != 0 {
		t.Errorf("expected no incident when short window is low, got %d", len(incidents))
	}
}

// TestSLO_FiresSyntheticIncident verifies the OnIncident callback fires with
// a fully-populated Incident struct suitable for feeding into the existing
// investigation pipeline. At burn rate 15x, all four default rules fire
// (14.4 / 6 / 3 / 1 are all <= 15) — we verify the highest-severity one
// (fast-burn-page) is among them and carries the expected fields.
func TestSLO_FiresSyntheticIncident(t *testing.T) {
	w, _ := newTestWatcher(15.0, 15.0, "") // install all default rules

	var incidents []Incident
	w.OnIncident = func(i Incident) { incidents = append(incidents, i) }
	w.Tick(context.Background())

	if len(incidents) != 4 {
		t.Fatalf("expected 4 incidents (all rules fire at 15x), got %d", len(incidents))
	}

	var inc *Incident
	for i := range incidents {
		if incidents[i].RuleName == "fast-burn-page" {
			inc = &incidents[i]
			break
		}
	}
	if inc == nil {
		t.Fatal("fast-burn-page rule did not fire")
	}
	if inc.AlertName != "SLOBurnRateHigh" {
		t.Errorf("expected alert name SLOBurnRateHigh, got %q", inc.AlertName)
	}
	if inc.Service != "api" || inc.Namespace != "prod" {
		t.Errorf("wrong service/namespace: %+v", inc)
	}
	if inc.SLOTarget != 0.999 {
		t.Errorf("expected SLOTarget=0.999, got %f", inc.SLOTarget)
	}
	if inc.LongBurn != 15.0 || inc.ShortBurn != 15.0 {
		t.Errorf("wrong burn rates: long=%f short=%f", inc.LongBurn, inc.ShortBurn)
	}
	if inc.FiredAt.IsZero() {
		t.Error("expected non-zero FiredAt")
	}
	if inc.Reason == "" {
		t.Error("expected non-empty Reason")
	}
}

// TestSLO_Cooldown verifies that a second tick within the rule's cooldown
// does NOT fire a second batch of incidents.
func TestSLO_Cooldown(t *testing.T) {
	w, _ := newTestWatcher(15.0, 15.0, "") // all default rules
	var incidents []Incident
	w.OnIncident = func(i Incident) { incidents = append(incidents, i) }

	// First tick fires all 4 rules.
	w.Tick(context.Background())
	if len(incidents) != 4 {
		t.Fatalf("expected 4 incidents after first tick, got %d", len(incidents))
	}

	// Second tick within the cooldowns — should NOT fire anything.
	w.Tick(context.Background())
	if len(incidents) != 4 {
		t.Errorf("expected cooldown to suppress second batch, got %d total", len(incidents))
	}
}

// TestSLO_QuerierErrorDoesNotFire verifies that a querier error is logged
// but does not produce an incident.
func TestSLO_QuerierErrorDoesNotFire(t *testing.T) {
	w, q := newTestWatcher(15.0, 15.0, "fast-burn-page")
	q.setErr(errors.New("prometheus unavailable"))

	fired := false
	w.OnIncident = func(_ Incident) { fired = true }
	w.Tick(context.Background())

	if fired {
		t.Error("expected no incident when querier returns an error")
	}
}

// TestSLO_SetRules verifies that SetRules replaces the default rule set.
func TestSLO_SetRules(t *testing.T) {
	w, q := newTestWatcher(15.0, 15.0, "fast-burn-page")
	custom := []AlertRule{
		{Name: "test-rule", LongWindow: 1 * time.Hour, ShortWindow: 5 * time.Minute, Threshold: 100, Severity: "page", Cooldown: 1 * time.Minute},
	}
	w.SetRules(custom)

	fired := false
	w.OnIncident = func(_ Incident) { fired = true }
	w.Tick(context.Background())

	if fired {
		t.Error("custom rule with threshold 100 should NOT fire at burn rate 15")
	}
	if q.callsCount() == 0 {
		t.Error("expected querier to be called")
	}
}

// TestSLO_DefaultRules verifies the canonical Google SRE rules are returned.
func TestSLO_DefaultRules(t *testing.T) {
	rules := DefaultRules()
	if len(rules) != 4 {
		t.Fatalf("expected 4 default rules, got %d", len(rules))
	}
	want := []struct {
		name      string
		threshold float64
		severity  string
	}{
		{"fast-burn-page", 14.4, "page"},
		{"medium-burn-page", 6.0, "page"},
		{"slow-burn-ticket", 3.0, "ticket"},
		{"always-on-ticket", 1.0, "ticket"},
	}
	for i, wnt := range want {
		if rules[i].Name != wnt.name || rules[i].Threshold != wnt.threshold || rules[i].Severity != wnt.severity {
			t.Errorf("rule %d mismatch: got %+v, want %+v", i, rules[i], wnt)
		}
	}
}

// TestSLO_SetInterval verifies the interval setter ignores zero/negative.
func TestSLO_SetInterval(t *testing.T) {
	w, _ := newTestWatcher(0.5, 0.5, "fast-burn-page")
	w.SetInterval(0)
	w.SetInterval(-1 * time.Second)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.interval <= 0 {
		t.Errorf("expected positive interval after rejecting zero/negative, got %v", w.interval)
	}
}
