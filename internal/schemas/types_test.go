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
		t.Error("IncidentID should not be Nil")
	}
	if inc.Status != StatusInvestigating {
		t.Errorf("Status = %q, want %q", inc.Status, StatusInvestigating)
	}
	if inc.State.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d, want 5", inc.State.MaxIterations)
	}
	if inc.BetaAlpha != 1.0 || inc.BetaBeta != 1.0 {
		t.Errorf("Beta prior = (%.2f, %.2f), want (1.0, 1.0)", inc.BetaAlpha, inc.BetaBeta)
	}
	if inc.State.IncidentID != inc.IncidentID {
		t.Error("AgentState.IncidentID should match Incident.IncidentID")
	}
	if !inc.CreatedAt.Equal(inc.UpdatedAt) {
		t.Error("CreatedAt and UpdatedAt should match on a fresh incident")
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

func TestStatusValuesDistinct(t *testing.T) {
	statuses := []IncidentStatus{
		StatusDetected, StatusInvestigating, StatusRootCaused, StatusFixProposed,
		StatusGovernanceReview, StatusApproved, StatusRejected, StatusExecuting,
		StatusVerified, StatusFailed, StatusEscalated, StatusResolved,
	}
	seen := make(map[IncidentStatus]bool, len(statuses))
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status: %q", s)
		}
		seen[s] = true
	}
}

func TestProviderConfigIsEnabled(t *testing.T) {
	tr := true
	fa := false
	cases := []struct {
		name string
		c    ProviderConfig
		want bool
	}{
		{"nil", ProviderConfig{}, true},
		{"true", ProviderConfig{Enabled: &tr}, true},
		{"false", ProviderConfig{Enabled: &fa}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.IsEnabled(); got != c.want {
				t.Errorf("IsEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}
