package policy

import (
	"testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

func allow(action string, level v1alpha1.AutonomyLevel) v1alpha1.SentinelPolicy {
	return v1alpha1.SentinelPolicy{Spec: v1alpha1.SentinelPolicySpec{Autonomy: []v1alpha1.ActionAutonomy{{Action: action, Level: level}}}}
}

func TestAutonomy(t *testing.T) {
	cases := []struct {
		policies []v1alpha1.SentinelPolicy
		want     v1alpha1.AutonomyLevel
	}{
		{nil, v1alpha1.AutonomyObserve},
		{[]v1alpha1.SentinelPolicy{allow("scale", v1alpha1.AutonomyAutonomous)}, v1alpha1.AutonomyObserve},
		{[]v1alpha1.SentinelPolicy{allow("rollout.undo", v1alpha1.AutonomyAutonomous)}, v1alpha1.AutonomyAutonomous},
		{[]v1alpha1.SentinelPolicy{allow("rollout.undo", v1alpha1.AutonomyAutonomous), allow("rollout.undo", v1alpha1.AutonomyApprove)},
			v1alpha1.AutonomyApprove},
	}
	for _, tc := range cases {
		if got := Autonomy(tc.policies, "rollout.undo"); got != tc.want {
			t.Errorf("Autonomy(%+v) = %s, want %s", tc.policies, got, tc.want)
		}
	}
}

func TestBudgetSpentKnowsItsOwnMessage(t *testing.T) {
	if !BudgetSpent(BudgetSpentMessage(6)) || BudgetSpent("the namespace's policy no longer allows it") || BudgetSpent("") {
		t.Fatal("BudgetSpent misread a message")
	}
}
