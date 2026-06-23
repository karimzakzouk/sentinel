package correlation

import (
	"testing"
	"time"
)

// TestCorrelator_DetectsCascade verifies that 3+ services failing within
// the window triggers a correlation event.
func TestCorrelator_DetectsCascade(t *testing.T) {
	var event CorrelationEvent

	c := New(func(e CorrelationEvent) {
		event = e
	})
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

	// Handler is synchronous — no wait needed
	if event.IncidentCount != 3 {
		t.Errorf("IncidentCount = %d, want 3", event.IncidentCount)
	}
}

// TestCorrelator_BelowThreshold verifies no event when below threshold.
func TestCorrelator_BelowThreshold(t *testing.T) {
	gotEvent := false
	c := New(func(e CorrelationEvent) {
		gotEvent = true
	})
	c.SetConfig(60*time.Second, 3, 5*time.Minute)

	now := time.Now()
	c.RegisterIncident("api", "prod", now)
	c.RegisterIncident("web", "prod", now.Add(10*time.Second))

	if gotEvent {
		t.Error("should not emit event below threshold")
	}
}

// TestCorrelator_Cooldown verifies that a second cascade within the cooldown
// period does not emit a second event.
func TestCorrelator_Cooldown(t *testing.T) {
	eventCount := 0
	c := New(func(e CorrelationEvent) {
		eventCount++
	})
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
	c := New(func(e CorrelationEvent) {
		gotEvent = true
	})
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
	c := New(func(e CorrelationEvent) {
		event = e
	})
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
	c := New(func(e CorrelationEvent) {
		gotEvent = true
	})
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
