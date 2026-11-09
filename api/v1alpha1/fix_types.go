package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FixSpec asks the Fixer to make an incident's mitigation hold at its
// source: to propose, as a pull request to the repository the workload's
// GitOps owner deploys from, the change that undoes the blamed change. It
// cannot be changed after creation.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type FixSpec struct {
	// IncidentID links the fix to its incident in the Ledger.
	// +kubebuilder:validation:MinLength=1
	IncidentID string `json:"incidentId"`
	// Workload is the workload the mitigation rolled back.
	Workload TargetRef `json:"workload"`
	// FromRevision is the pod template hash of the revision the workload
	// was rolled back from, whose template holds the bad values.
	// +kubebuilder:validation:MinLength=1
	FromRevision string `json:"fromRevision"`
}

// FixPhase is a step in a fix's lifecycle: Proposed once its pull request
// is open, Merged once someone merged it, Ready once the owner's source
// holds the merge, so the owner can be resumed; Declined when the pull
// request was closed unmerged, Abstained when the Fixer would not propose
// a change, saying why.
// +kubebuilder:validation:Enum=Pending;Proposed;Merged;Ready;Declined;Abstained;Failed
type FixPhase string

// FixStatus is written by the Fixer.
type FixStatus struct {
	// +optional
	Phase FixPhase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Owner is what deploys the workload, as Kind namespace/name.
	// +optional
	Owner string `json:"owner,omitempty"`
	// Repository is the clone URL of the owner's source, and Branch the
	// branch it deploys.
	// +optional
	Repository string `json:"repository,omitempty"`
	// +optional
	Branch string `json:"branch,omitempty"`
	// Change says, per line changed, file:line and the change.
	// +optional
	// +listType=atomic
	Change []string `json:"change,omitempty"`
	// PullRequest is the pull request's URL, and Number its number.
	// +optional
	PullRequest string `json:"pullRequest,omitempty"`
	// +optional
	Number int32 `json:"number,omitempty"`
	// MergeCommit is the commit that merged the pull request.
	// +optional
	MergeCommit string `json:"mergeCommit,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// Fix is the Fixer's proposal for one incident.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=sentinel
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workload.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Pull request",type=string,JSONPath=`.status.pullRequest`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Fix struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FixSpec   `json:"spec"`
	Status FixStatus `json:"status,omitempty"`
}

// FixList is a list of Fix.
//
// +kubebuilder:object:root=true
type FixList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Fix `json:"items"`
}
