// Package policy holds what namespaces' SentinelPolicies mean, so that the
// controller and the actor read them the same way.
package policy

import (
	"slices"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

var ladder = []v1alpha1.AutonomyLevel{v1alpha1.AutonomyObserve, v1alpha1.AutonomyRecommend, v1alpha1.AutonomyApprove,
	v1alpha1.AutonomyActUnlessVetoed, v1alpha1.AutonomyAutonomous}

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
