package schemas

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewIncident(t *testing.T) {
	cluster := AnomalyCluster{
		ClusterID: uuid.New(),
		Service:   "api",
		Namespace: "default",
		Anomalies: []Anomaly{
			{
				AnomalyID: uuid.New(),
				SignalID:  uuid.New(),
				Service:   "api",
				Namespace: "default",
				Detector:  "rules",
				Score:     0.9,
			},
		},
	}
	inc := NewIncident(cluster)
	if inc.IncidentID == uuid.Nil {
		t.Error("IncidentID should not be nil")
	}
	if inc.Status != StatusInvestigating {
		t.Errorf("Status = %q, want %q", inc.Status, StatusInvestigating)
	}
	if inc.State.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d, want 5", inc.State.MaxIterations)
	}
	if inc.BetaAlpha != 1.0 || inc.BetaBeta != 1.0 {
		t.Error("Beta prior should start at 1.0/1.0")
	}
}

func TestFixTypeConstants(t *testing.T) {
	cases := []struct {
		got, want FixType
	}{
		{FixRestart, FixType("restart")},
		{FixScale, FixType("scale")},
		{FixCodeFix, FixType("code_fix")},
		{FixRollback, FixType("rollback")},
		{FixRollForward, FixType("roll_forward")},
		{FixInfrastructure, FixType("infrastructure")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestStatusTransitions(t *testing.T) {
	// Sanity check that all statuses are distinct strings
	statuses := []IncidentStatus{
		StatusDetected, StatusInvestigating, StatusRootCaused, StatusFixProposed,
		StatusGovernanceReview, StatusApproved, StatusRejected, StatusExecuting,
		StatusVerified, StatusFailed, StatusEscalated, StatusResolved,
	}
	seen := make(map[IncidentStatus]bool)
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status: %q", s)
		}
		seen[s] = true
	}
}
