// Package policy holds what namespaces' SentinelPolicies mean, so that the
// controller and the actor read them the same way.
package policy

import (
	"fmt"
	"slices"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// ladder orders the levels from strictest. lab-verified sits above approve,
// since it may act without a person, and below act-unless-vetoed, since it
// acts only on the Lab's evidence.
var ladder = []v1alpha1.AutonomyLevel{v1alpha1.AutonomyObserve, v1alpha1.AutonomyRecommend, v1alpha1.AutonomyApprove,
	v1alpha1.AutonomyLabVerified, v1alpha1.AutonomyActUnlessVetoed, v1alpha1.AutonomyAutonomous}

// Autonomy returns the level a namespace's policies allow an action: the
// lowest any of them names, since the strictest wins, and observe when none
// names it.
func Autonomy(policies []v1alpha1.SentinelPolicy, action string) v1alpha1.AutonomyLevel {
	level, found := v1alpha1.AutonomyObserve, false
	for _, p := range policies {
		for _, a := range p.Spec.Autonomy {
			if a.Action == action && (!found || rank(a.Level) < rank(level)) {
				level, found = a.Level, true
			}
		}
	}
	return level
}

func rank(l v1alpha1.AutonomyLevel) int { return slices.Index(ladder, l) }

// Above reports whether level a is less strict than level b.
func Above(a, b v1alpha1.AutonomyLevel) bool { return rank(a) > rank(b) }

// Earned reports whether any of a namespace's policies holds an action at
// approve until its track record earns its level; the strictest wins.
func Earned(policies []v1alpha1.SentinelPolicy, action string) bool {
	for _, p := range policies {
		for _, a := range p.Spec.Autonomy {
			if a.Action == action && a.Earned {
				return true
			}
		}
	}
	return false
}

// DefaultMaxCapacityRemoval is the share of replicas, in percent, an action
// may take away when no policy says.
const DefaultMaxCapacityRemoval = 20

// MaxCapacityRemoval returns the share of a workload's replicas, in percent,
// an action may take away: the lowest any policy sets.
func MaxCapacityRemoval(policies []v1alpha1.SentinelPolicy) int32 {
	limit, found := int32(DefaultMaxCapacityRemoval), false
	for _, p := range policies {
		if v := p.Spec.Budgets.MaxCapacityRemovalPercent; v != nil && (!found || *v < limit) {
			limit, found = *v, true
		}
	}
	return limit
}

// budgetSpent is the message of a request the hourly budget turned down.
const budgetSpent = "the namespace's budget of %d actions an hour is spent"

// BudgetSpentMessage is what the actor says of a request a namespace's
// hourly budget of perHour actions turned down.
func BudgetSpentMessage(perHour int) string { return fmt.Sprintf(budgetSpent, perHour) }

// BudgetSpent reports whether a rejected request's message says the
// namespace's hourly budget turned it down: a limit on how often Sentinel
// acts, not a judgment of the action.
func BudgetSpent(message string) bool {
	var n int
	_, err := fmt.Sscanf(message, budgetSpent, &n)
	return err == nil && BudgetSpentMessage(n) == message
}

// Capture reports whether a namespace's policies turn traffic capture on:
// any one that does.
func Capture(policies []v1alpha1.SentinelPolicy) bool {
	for _, p := range policies {
		if p.Spec.Capture != nil && p.Spec.Capture.Requests {
			return true
		}
	}
	return false
}
