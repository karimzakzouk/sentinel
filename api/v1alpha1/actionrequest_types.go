package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TargetRef names the object an action changes.
type TargetRef struct {
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ActionRequestSpec is the Brain's typed request. It cannot be changed after
// creation, so what was approved is exactly what runs.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type ActionRequestSpec struct {
	// IncidentID links the request to its incident in the Ledger.
	// +kubebuilder:validation:MinLength=1
	IncidentID string `json:"incidentId"`
	// Action is the action type. Each type has a contract the actor checks
	// before running it.
	// +kubebuilder:validation:Enum=rollout.undo;scale;restart;argocd.rollback;flux.rollback;helm.rollback;rollouts.abort;flagger.abort
	Action string `json:"action"`
	// Target is the object the action changes.
	Target TargetRef `json:"target"`
	// Params are the action's arguments, validated against its contract.
	// +optional
	// +kubebuilder:validation:MaxProperties=16
	Params map[string]string `json:"params,omitempty"`
	// Reason explains the request to the people who approve or veto it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Reason string `json:"reason"`
	// ExpiresAt is when an unapproved request lapses.
	ExpiresAt metav1.Time `json:"expiresAt"`
	// DryRunOnly asks for the server-side dry run and nothing else.
	// +optional
	DryRunOnly bool `json:"dryRunOnly,omitempty"`
}

// ActionPhase is a step in an action request's lifecycle.
// +kubebuilder:validation:Enum=Pending;Approved;Rejected;DryRunPassed;Executing;Succeeded;Failed;Undone;Expired
type ActionPhase string

const (
	ActionPending      ActionPhase = "Pending"
	ActionApproved     ActionPhase = "Approved"
	ActionRejected     ActionPhase = "Rejected"
	ActionDryRunPassed ActionPhase = "DryRunPassed"
	ActionExecuting    ActionPhase = "Executing"
	ActionSucceeded    ActionPhase = "Succeeded"
	ActionFailed       ActionPhase = "Failed"
	ActionUndone       ActionPhase = "Undone"
	ActionExpired      ActionPhase = "Expired"
)

// Done reports a phase no request leaves.
func (p ActionPhase) Done() bool {
	switch p {
	case ActionRejected, ActionDryRunPassed, ActionSucceeded, ActionFailed, ActionUndone, ActionExpired:
		return true
	}
	return false
}

// ActionRequestStatus is written by the actor.
type ActionRequestStatus struct {
	// +optional
	Phase ActionPhase `json:"phase,omitempty"`
	// ApprovedBy is the identity whose approval let the action run.
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ActionRequest asks the actor to run one mitigation through the workload's owner.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ar,categories=sentinel
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ActionRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ActionRequestSpec   `json:"spec"`
	Status ActionRequestStatus `json:"status,omitempty"`
}

// ActionRequestList is a list of ActionRequest.
//
// +kubebuilder:object:root=true
type ActionRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ActionRequest `json:"items"`
}
