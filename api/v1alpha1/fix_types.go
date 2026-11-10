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
	// FaultClass is the class Sentinel gave the incident. When that
	// revision changed several values, it says which of them to blame; the
	// fix leaves the others as the revision set them.
	// +optional
	FaultClass string `json:"faultClass,omitempty"`
	// MemoryLimits are, per container, the memory limit Sentinel learned
	// from what the workload used while it was healthy. For a memory-limit
	// incident a fix proposes it, since putting back the old limit can be
	// as wrong as the new one.
	// +optional
	MemoryLimits map[string]string `json:"memoryLimits,omitempty"`
	// Merge lets the Fixer merge its pull request itself: the namespace's
	// policy grants fix.merge at autonomous, and Sentinel's fixes for this
	// fault on this workload have earned it where the policy asks them to.
	// The Fixer checks the policy again before it merges.
	// +optional
	Merge bool `json:"merge,omitempty"`
	// Crash is the stack the failing revision crashed with, when its logs
	// held one: a fix to the image's code starts from it.
	// +optional
	Crash *Crash `json:"crash,omitempty"`
}

// Crash is a crash's stack as Sentinel parsed it from redacted logs.
type Crash struct {
	// Language is the runtime that printed it, such as go.
	Language string `json:"language"`
	// Frames are the application's own functions, innermost first.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Frames []string `json:"frames"`
	// Stack is the same frames with where each ran, where the runtime
	// printed it.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Stack []CrashFrame `json:"stack,omitempty"`
}

// CrashFrame is one frame of a crash's stack.
type CrashFrame struct {
	// Function is the frame's function, as the runtime named it; empty
	// when it printed only where.
	// +optional
	Function string `json:"function,omitempty"`
	// File is the source file it ran in, as the runtime printed it.
	// +optional
	File string `json:"file,omitempty"`
	// +optional
	Line int32 `json:"line,omitempty"`
}

// CodeFix is a fix proposed to a failing image's source.
type CodeFix struct {
	// Repository is the source's URL, and Revision the commit the failing
	// image was built from.
	// +optional
	Repository string `json:"repository,omitempty"`
	// +optional
	Revision string `json:"revision,omitempty"`
	// Function is what the fix puts back as it was.
	// +optional
	Function string `json:"function,omitempty"`
	// +optional
	// +listType=atomic
	Change []string `json:"change,omitempty"`
	// +optional
	PullRequest string `json:"pullRequest,omitempty"`
	// +optional
	Number int32 `json:"number,omitempty"`
	// Failing is the image that crashed; the image built from the fix is
	// looked for in its repository.
	// +optional
	Failing string `json:"failing,omitempty"`
	// Commit is the fix's commit, whose image the source's own pipeline
	// builds, and Image that image once the registry has it.
	// +optional
	Commit string `json:"commit,omitempty"`
	// +optional
	Image string `json:"image,omitempty"`
	// Verdict is the Lab's on that image raced against the failing one:
	// Passed, Failed or NotSure.
	// +optional
	Verdict string `json:"verdict,omitempty"`
	// Message says why there is no code fix, or how it stands.
	// +optional
	Message string `json:"message,omitempty"`
}

// FixPhase is a step in a fix's lifecycle: Proposed once its pull request
// is open, Merged once someone merged it, Ready once the owner's source
// holds the merge, so a person can resume the owner, and Applied once the
// owner is resumed and has applied it; Declined when the pull request was
// closed unmerged, Abstained when the Fixer would not propose a change,
// saying why.
// +kubebuilder:validation:Enum=Pending;Proposed;Merged;Ready;Applied;Declined;Abstained;Failed
type FixPhase string

// FixStatus is written by the Fixer.
type FixStatus struct {
	// +optional
	Phase FixPhase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Owner is what deploys the workload, as Kind namespace/name, and
	// Resume the command a person runs to resume it once the fix is in.
	// +optional
	Owner string `json:"owner,omitempty"`
	// +optional
	Resume string `json:"resume,omitempty"`
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
	// Code is the fix proposed to the failing image's source, beside the
	// one that pins the image back, when the fault is in its code.
	// +optional
	Code *CodeFix `json:"code,omitempty"`
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
