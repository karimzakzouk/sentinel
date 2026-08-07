package correlation

import (
	"testing"
	"time"
)

// TestCorrelator_DetectsCascade verifies that 3+ services failing within
// the window triggers a correlation event.
func TestCorrelator_DetectsCascade(t *testing.T) {
	var event CorrelationEvent
	c := New(func(e CorrelationEvent) { event = e })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	if c.RegisterIncident("api", "prod", now) {
		t.Error("should not emit event for 1 incident")
	}
	if c.RegisterIncident("web", "prod", now.Add(10*time.Second)) {
		t.Error("should not emit event for 2 incidents")
	}
	if !c.RegisterIncident("worker", "prod", now.Add(20*time.Second)) {
		t.Error("should emit event for 3 incidents")
	}
	if event.IncidentCount != 3 {
		t.Errorf("IncidentCount = %d, want 3", event.IncidentCount)
	}
}

// TestCorrelator_BelowThreshold verifies no event when below threshold.
func TestCorrelator_BelowThreshold(t *testing.T) {
	gotEvent := false
	c := New(func(e CorrelationEvent) { gotEvent = true })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(10*time.Second))
	if gotEvent {
		t.Error("should not emit event below threshold")
	}
}

// TestCorrelator_Cooldown verifies that a second cascade within the cooldown
// does not emit a second event.
func TestCorrelator_Cooldown(t *testing.T) {
	eventCount := 0
	c := New(func(e CorrelationEvent) { eventCount++ })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(10*time.Second))
	c.RegisterIncident("worker", "prod", now.Add(20*time.Second)) // emits
	c.RegisterIncident("scheduler", "prod", now.Add(30*time.Second))
	c.RegisterIncident("cache", "prod", now.Add(40*time.Second))
	if eventCount != 1 {
		t.Errorf("expected 1 event (cooldown), got %d", eventCount)
	}
}

// TestCorrelator_WindowExpiry verifies that incidents outside the window
// don't count toward the threshold.
func TestCorrelator_WindowExpiry(t *testing.T) {
	gotEvent := false
	c := New(func(e CorrelationEvent) { gotEvent = true })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now.Add(-2*time.Minute))
	c.RegisterIncident("web", "prod", now.Add(-2*time.Minute))
	c.RegisterIncident("worker", "prod", now)
	if gotEvent {
		t.Error("should not emit event — first two incidents expired")
	}
}

// TestCorrelator_LikelyCause verifies that the most-recently-deployed service
// is identified as the likely cause.
func TestCorrelator_LikelyCause(t *testing.T) {
	var event CorrelationEvent
	c := New(func(e CorrelationEvent) { event = e })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterDeployment("payment-api", "prod", now.Add(-30*time.Second))
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(5*time.Second))
	c.RegisterIncident("worker", "prod", now.Add(10*time.Second)) // emits

	if event.LikelyCause != "payment-api/prod" {
		t.Errorf("LikelyCause = %q, want payment-api/prod", event.LikelyCause)
	}
}

// TestCorrelator_IsLikelyCascade verifies the helper.
func TestCorrelator_IsLikelyCascade(t *testing.T) {
	c := New(nil)
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	if c.IsLikelyCascade("api", "prod") {
		t.Error("should not be cascade with 0 incidents")
	}
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(5*time.Second))
	if c.IsLikelyCascade("api", "prod") {
		t.Error("should not be cascade with 2 incidents")
	}
	c.RegisterIncident("worker", "prod", now.Add(10*time.Second))
	if !c.IsLikelyCascade("api", "prod") {
		t.Error("should be cascade with 3 incidents")
	}
}

// TestCorrelator_DistinctServices verifies same service 3x doesn't trigger.
func TestCorrelator_DistinctServices(t *testing.T) {
	gotEvent := false
	c := New(func(e CorrelationEvent) { gotEvent = true })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("api", "prod", now.Add(5*time.Second))
	c.RegisterIncident("api", "prod", now.Add(10*time.Second))
	if gotEvent {
		t.Error("same service failing 3x should not trigger cascade")
	}
}

// TestCorrelator_ActiveIncidentCount verifies the count helper.
func TestCorrelator_ActiveIncidentCount(t *testing.T) {
	c := New(nil)
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(5*time.Second))
	if count := c.ActiveIncidentCount(); count != 2 {
		t.Errorf("ActiveIncidentCount = %d, want 2", count)
	}
}

// TestCorrelator_CascadeDetection is the v1 regression test: a multi-service
// cascade after a single deployment should emit exactly one event naming
// the deployed service as the likely cause. The event carries the snapshot
// of distinct services at the moment of emission (the threshold-crossing
// incident is included; later registrations are suppressed by cooldown).
func TestCorrelator_CascadeDetection(t *testing.T) {
	var events []CorrelationEvent
	c := New(func(e CorrelationEvent) { events = append(events, e) })
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterDeployment("payment-api", "prod", now.Add(-45*time.Second))

	c.RegisterIncident("api", "prod", now.Add(0*time.Second))
	c.RegisterIncident("web", "prod", now.Add(5*time.Second))
	// Third distinct service crosses the threshold → emits with 3 services.
	c.RegisterIncident("worker", "prod", now.Add(10*time.Second))
	// Fourth distinct service arrives within the cooldown → suppressed.
	c.RegisterIncident("scheduler", "prod", now.Add(15*time.Second))

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 cascade event, got %d", len(events))
	}
	ev := events[0]
	if ev.LikelyCause != "payment-api/prod" {
		t.Errorf("LikelyCause = %q, want payment-api/prod", ev.LikelyCause)
	}
	if len(ev.Services) != 3 {
		t.Errorf("Services = %v, want 3 entries (snapshot at emission)", ev.Services)
	}
	if len(ev.Namespaces) != 1 || ev.Namespaces[0] != "prod" {
		t.Errorf("Namespaces = %v, want [prod]", ev.Namespaces)
	}
	if ev.IncidentCount != 3 {
		t.Errorf("IncidentCount = %d, want 3", ev.IncidentCount)
	}
}

// TestSplitKey verifies the key-splitter for a couple of edge cases.
func TestSplitKey(t *testing.T) {
	cases := []struct {
		in      string
		wantSvc string
		wantNs  string
	}{
		{"api/prod", "api", "prod"},
		{"payment-api/prod", "payment-api", "prod"},
		{"github.com/org/repo/default", "github.com/org/repo", "default"},
		{"standalone", "standalone", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			svc, ns := splitKey(c.in)
			if svc != c.wantSvc || ns != c.wantNs {
				t.Errorf("splitKey(%q) = (%q, %q), want (%q, %q)",
					c.in, svc, ns, c.wantSvc, c.wantNs)
			}
		})
	}
}
