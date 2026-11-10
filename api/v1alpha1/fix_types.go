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
	// CPULimits are, per container, the CPU limit Sentinel learned from
	// what the workload used while it was healthy. For a cpu-limit
	// incident a fix proposes it, since putting back the old limit can be
	// as wrong as the new one.
	// +optional
	CPULimits map[string]string `json:"cpuLimits,omitempty"`
	// DiskLimits are, per container, the ephemeral-storage limit Sentinel
	// learned from what the workload used while it was healthy. For a
	// disk-limit incident a fix proposes it, since putting back the old
	// limit can be as wrong as the new one.
	// +optional
	DiskLimits map[string]string `json:"diskLimits,omitempty"`
	// ReplicasBad is the replica count the rolled-back revision set, when
	// a fix also restores spec.replicas; the good count is what runs now.
	// +optional
	ReplicasBad *int32 `json:"replicasBad,omitempty"`
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
	// Errors are the error lines the failing revision logged, as masked
	// templates; where no stack says where it failed, the source that logs
	// them may.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=5
	Errors []string `json:"errors,omitempty"`
	// CodeOnly asks for a fix to the code alone, for a crash nothing was
	// rolled back from: FromRevision is the revision that crashes, still
	// running, and the culprit is the commit that last changed the code the
	// crash ran through.
	// +optional
	CodeOnly bool `json:"codeOnly,omitempty"`
	// LabVerdict is what the Lab found of the values the fix sets, raced
	// in a copy of the workload before the pull request opens.
	// +optional
	LabVerdict string `json:"labVerdict,omitempty"`
}

// Crash is a crash's stack as Sentinel parsed it from redacted logs.
type Crash struct {
	// Language is the runtime that printed it, such as go.
	Language string `json:"language"`
	// Frames are the application's own functions, innermost first.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Frames []string `json:"frames"`
	// Types are the crash's exception types, outermost first, or the
	// message a runtime prints for a panic, masked.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	Types []string `json:"types,omitempty"`
	// Message is the message of the exception the frames belong to, masked.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`
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
	// looked for in its repository. Previous is the image the workload was
	// rolled back to.
	// +optional
	Failing string `json:"failing,omitempty"`
	// +optional
	Previous string `json:"previous,omitempty"`
	// Base is the commit fixes are measured from: the previous image's, or,
	// for a crash nothing was rolled back from, the parent of the commit
	// that last changed the code the crash ran through.
	// +optional
	Base string `json:"base,omitempty"`
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
	// Fixer names what proposed the current change: restore (a function
	// put back as it was), template, mine (a repair from the repository's
	// own history), revert (every file the commit changed put back) or llm.
	// +optional
	Fixer string `json:"fixer,omitempty"`
	// Attempt counts the changes proposed, the current one included.
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// State is the pull request's: open, merged or closed.
	// +optional
	State string `json:"state,omitempty"`
	// Tried are the earlier changes, oldest first, and what became of them.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	Tried []CodeAttempt `json:"tried,omitempty"`
}

// CodeAttempt is a change to the code the Fixer proposed and moved on from:
// the Lab found it did not fix the crash, or a person closed it.
type CodeAttempt struct {
	Fixer string `json:"fixer"`
	// +optional
	// +listType=atomic
	Change []string `json:"change,omitempty"`
	// +optional
	PullRequest string `json:"pullRequest,omitempty"`
	// +optional
	Commit string `json:"commit,omitempty"`
	// +optional
	Image string `json:"image,omitempty"`
	// Verdict is the Lab's, or Declined when a person closed the pull
	// request.
	// +optional
	Verdict string `json:"verdict,omitempty"`
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
