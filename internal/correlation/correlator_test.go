package correlation

import (
        "sync"
        "testing"
        "time"
)

// TestCorrelator_DetectsCascade verifies that 3+ services failing within
// the window triggers a correlation event.
func TestCorrelator_DetectsCascade(t *testing.T) {
        var event CorrelationEvent
        var eventMu sync.Mutex
        done := make(chan struct{})

        c := New(func(e CorrelationEvent) {
                eventMu.Lock()
                defer eventMu.Unlock()
                event = e
                select {
                case done <- struct{}{}:
                default:
                }
        })
        c.SetConfig(60*time.Second, 3, 5*time.Minute)

        now := time.Now()

        // First two incidents — below threshold, no event
        if c.RegisterIncident("api", "prod", now) {
                t.Error("should not emit event for 1 incident")
        }
        if c.RegisterIncident("web", "prod", now.Add(10*time.Second)) {
                t.Error("should not emit event for 2 incidents")
        }

        // Third incident — exceeds threshold, should emit
        if !c.RegisterIncident("worker", "prod", now.Add(20*time.Second)) {
                t.Error("should emit event for 3 incidents")
        }

        // Wait for the handler
        select {
        case <-done:
        case <-time.After(1 * time.Second):
                t.Fatal("handler did not run within 1s")
        }

        eventMu.Lock()
        defer eventMu.Unlock()
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
        var mu sync.Mutex
        done := make(chan struct{})
        c := New(func(e CorrelationEvent) {
                mu.Lock()
                defer mu.Unlock()
                eventCount++
                // Signal that the handler ran
                select {
                case done <- struct{}{}:
                default:
                }
        })
        c.SetConfig(60*time.Second, 3, 5*time.Minute)

        now := time.Now()
        c.RegisterIncident("api", "prod", now)
        c.RegisterIncident("web", "prod", now.Add(10*time.Second))
        c.RegisterIncident("worker", "prod", now.Add(20*time.Second)) // emits

        // Wait for the handler to run
        select {
        case <-done:
        case <-time.After(1 * time.Second):
                t.Fatal("handler did not run within 1s")
        }

        // Same cascade, different services — should be suppressed by cooldown
        c.RegisterIncident("scheduler", "prod", now.Add(30*time.Second))
        c.RegisterIncident("cache", "prod", now.Add(40*time.Second))

        // Give a moment for any spurious handler calls
        time.Sleep(100 * time.Millisecond)

        mu.Lock()
        defer mu.Unlock()
        if eventCount != 1 {
                t.Errorf("expected 1 event (cooldown), got %d", eventCount)
        }
}

// TestCorrelator_WindowExpiry verifies that incidents outside the window
// don't count toward the threshold.
func TestCorrelator_WindowExpiry(t *testing.T) {
        gotEvent := false
        var mu sync.Mutex
        c := New(func(e CorrelationEvent) {
                mu.Lock()
                gotEvent = true
                mu.Unlock()
        })
        c.SetConfig(60*time.Second, 3, 5*time.Minute)

        now := time.Now()
        // First two incidents 2 minutes ago — outside the 60s window
        c.RegisterIncident("api", "prod", now.Add(-2*time.Minute))
        c.RegisterIncident("web", "prod", now.Add(-2*time.Minute))

        // Third incident now — but the first two expired
        c.RegisterIncident("worker", "prod", now)

        time.Sleep(100 * time.Millisecond)
        mu.Lock()
        defer mu.Unlock()
        if gotEvent {
                t.Error("should not emit event — first two incidents expired")
        }
}

// TestCorrelator_LikelyCause verifies that the most-recently-deployed service
// is identified as the likely cause.
func TestCorrelator_LikelyCause(t *testing.T) {
        var event CorrelationEvent
        var mu sync.Mutex
        done := make(chan struct{})
        c := New(func(e CorrelationEvent) {
                mu.Lock()
                defer mu.Unlock()
                event = e
                select {
                case done <- struct{}{}:
                default:
                }
        })
        c.SetConfig(60*time.Second, 3, 5*time.Minute)

        now := time.Now()

        // Service "payment-api" deployed 30 seconds ago
        c.RegisterDeployment("payment-api", "prod", now.Add(-30*time.Second))

        // Now 3 other services start failing
        c.RegisterIncident("api", "prod", now)
        c.RegisterIncident("web", "prod", now.Add(5*time.Second))
        c.RegisterIncident("worker", "prod", now.Add(10*time.Second)) // emits

        // Wait for the handler
        select {
        case <-done:
        case <-time.After(1 * time.Second):
                t.Fatal("handler did not run within 1s")
        }

        mu.Lock()
        defer mu.Unlock()
        if event.LikelyCause != "payment-api/prod" {
                t.Errorf("LikelyCause = %q, want payment-api/prod", event.LikelyCause)
        }
}

// TestCorrelator_IsLikelyCascade verifies the helper used by the detection
// layer to decide whether to skip individual investigation.
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
                t.Error("should not be cascade with 2 incidents (below threshold)")
        }

        c.RegisterIncident("worker", "prod", now.Add(10*time.Second))
        if !c.IsLikelyCascade("api", "prod") {
                t.Error("should be cascade with 3 incidents")
        }
}

// TestCorrelator_DistinctServices verifies that the SAME service failing
// multiple times doesn't trigger a cascade (it's one incident, not a cascade).
func TestCorrelator_DistinctServices(t *testing.T) {
        gotEvent := false
        c := New(func(e CorrelationEvent) {
                gotEvent = true
        })
        c.SetConfig(60*time.Second, 3, 5*time.Minute)

        now := time.Now()
        // Same service, same namespace, 3 times — should NOT trigger
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
