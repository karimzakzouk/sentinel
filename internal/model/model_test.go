package model

import (
	"math"
	"testing"
)

func TestIncidentStateMachine(t *testing.T) {
	allowed := [][2]IncidentState{
		{IncidentOpen, IncidentMitigating},
		{IncidentMitigating, IncidentMitigated},
		{IncidentMitigated, IncidentFixing},
		{IncidentFixing, IncidentResolved},
		{IncidentResolved, IncidentClosed},
		{IncidentMitigating, IncidentOpen},
	}
	for _, tr := range allowed {
		if !CanTransition(tr[0], tr[1]) {
			t.Errorf("%s -> %s should be allowed", tr[0], tr[1])
		}
	}
	forbidden := [][2]IncidentState{
		{IncidentClosed, IncidentOpen},
		{IncidentOpen, IncidentMitigated},
		{IncidentResolved, IncidentMitigating},
	}
	for _, tr := range forbidden {
		if CanTransition(tr[0], tr[1]) {
			t.Errorf("%s -> %s should be forbidden", tr[0], tr[1])
		}
	}
}

func TestVerdictValidate(t *testing.T) {
	ok := Verdict{Outcome: OutcomePass, Alpha: 0.05, Lower: -0.1, Upper: 0.2}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid verdict rejected: %v", err)
	}
	bad := []Verdict{
		{Outcome: OutcomePass, Alpha: 0, Lower: 0, Upper: 1},
		{Outcome: OutcomePass, Alpha: 0.05, Lower: 1, Upper: 0},
		{Outcome: OutcomePass, Alpha: 0.05, Lower: math.NaN(), Upper: 0},
		{Outcome: "maybe", Alpha: 0.05, Lower: 0, Upper: 1},
	}
	for _, v := range bad {
		if v.Validate() == nil {
			t.Errorf("invalid verdict accepted: %+v", v)
		}
	}
}

func TestTrackRecordMean(t *testing.T) {
	if got := (TrackRecord{Alpha: 3, Beta: 1}).Mean(); got != 0.75 {
		t.Fatalf("Mean = %v, want 0.75", got)
	}
	if got := (TrackRecord{}).Mean(); got != 0 {
		t.Fatalf("Mean of an empty record = %v, want 0", got)
	}
}

func TestObjectRefString(t *testing.T) {
	cases := map[string]ObjectRef{
		"Deployment.apps/shop/cart": {Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "cart"},
		"Node/worker-1":             {Kind: "Node", Name: "worker-1"},
	}
	for want, ref := range cases {
		if got := ref.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestNewIDIsTimeOrdered(t *testing.T) {
	a, b := NewID(), NewID()
	if a >= b {
		t.Fatalf("IDs are not increasing: %s then %s", a, b)
	}
}
