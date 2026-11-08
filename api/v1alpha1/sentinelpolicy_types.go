package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AutonomyLevel is one rung of the autonomy ladder.
// +kubebuilder:validation:Enum=observe;recommend;approve;act-unless-vetoed;autonomous
type AutonomyLevel string

const (
	AutonomyObserve         AutonomyLevel = "observe"
	AutonomyRecommend       AutonomyLevel = "recommend"
	AutonomyApprove         AutonomyLevel = "approve"
	AutonomyActUnlessVetoed AutonomyLevel = "act-unless-vetoed"
	AutonomyAutonomous      AutonomyLevel = "autonomous"
)

// ActionAutonomy caps the autonomy of one action type.
type ActionAutonomy struct {
	// Action is an action type, such as rollout.undo or scale.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Action string `json:"action"`
	// Level is the highest autonomy the action may reach. Track records can
	// only raise an action up to this level, never past it.
	Level AutonomyLevel `json:"level"`
}

// RiskBudgets bound how much risk Sentinel may take in the namespace. They
// replace hard-coded thresholds: detectors and tests derive their cut-offs
// from these numbers.
type RiskBudgets struct {
	// FalseAlarmsPerWeek is the accepted number of false alarms per service per week.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=1
	FalseAlarmsPerWeek *int32 `json:"falseAlarmsPerWeek,omitempty"`
	// AlphaPercent is the accepted false-positive rate of verification and Lab tests, in percent.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:default=5
	AlphaPercent *int32 `json:"alphaPercent,omitempty"`
	// MaxCapacityRemovalPercent caps the share of a workload's replicas an action may take away.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=20
	MaxCapacityRemovalPercent *int32 `json:"maxCapacityRemovalPercent,omitempty"`
}

// SentinelPolicySpec is a namespace owner's opt-in.
type SentinelPolicySpec struct {
	// Autonomy lists the highest autonomy per action type. Unlisted actions stay at observe.
	// +optional
	// +listType=map
	// +listMapKey=action
	// +kubebuilder:validation:MaxItems=64
	Autonomy []ActionAutonomy `json:"autonomy,omitempty"`
	// Budgets bound the risk Sentinel may take.
	// +optional
	Budgets RiskBudgets `json:"budgets,omitempty"`
	// CriticalSelector marks tier-0 workloads, which are held to stricter rules.
	// +optional
	CriticalSelector *metav1.LabelSelector `json:"criticalSelector,omitempty"`
	// QuietPeriodAfterHumanEdit pauses actions on a workload after a person changes it.
	// +optional
	// +kubebuilder:default="30m"
	QuietPeriodAfterHumanEdit *metav1.Duration `json:"quietPeriodAfterHumanEdit,omitempty"`
}

// SentinelPolicyStatus reports whether the policy is in effect.
type SentinelPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SentinelPolicy opts a namespace in to Sentinel and sets how far it may act.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sp,categories=sentinel
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SentinelPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SentinelPolicySpec   `json:"spec,omitempty"`
	Status SentinelPolicyStatus `json:"status,omitempty"`
}

// SentinelPolicyList is a list of SentinelPolicy.
//
// +kubebuilder:object:root=true
type SentinelPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SentinelPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SentinelPolicy{}, &SentinelPolicyList{})
}
