package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExperimentSpec describes one sealed Lab run. It cannot be changed after creation.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.purpose == 'prove' || has(self.workload)",message="every purpose but prove names a workload"
type ExperimentSpec struct {
	// IncidentID links the experiment to its incident in the Ledger.
	// +kubebuilder:validation:MinLength=1
	IncidentID string `json:"incidentId"`
	// Purpose says what the experiment tests: prove seals the Lab and
	// proves its isolation, and runs no workload.
	// +kubebuilder:validation:Enum=prove;reproduce;verify-fix;verify-config
	Purpose string `json:"purpose"`
	// LabNamespace is a namespace from the pool created at install.
	// +kubebuilder:validation:MinLength=1
	LabNamespace string `json:"labNamespace"`
	// Workload is the production workload the experiment copies into the
	// Lab, with its dependencies.
	// +optional
	Workload *TargetRef `json:"workload,omitempty"`
	// Revision is the workload's failing revision: a Deployment's
	// ReplicaSet or a StatefulSet's ControllerRevision. Empty means its
	// current template.
	// +optional
	Revision string `json:"revision,omitempty"`
	// Candidate identifies the fix under test.
	// +optional
	Candidate string `json:"candidate,omitempty"`
	// AlphaPercent bounds the false-PASS rate of the verdict, in percent.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:default=5
	AlphaPercent int32 `json:"alphaPercent,omitempty"`
	// MaxRuns caps the paired runs before the verdict falls back to not-sure.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=200
	MaxRuns int32 `json:"maxRuns,omitempty"`
	// Timeout caps the experiment's wall-clock time.
	// +optional
	// +kubebuilder:default="30m"
	Timeout metav1.Duration `json:"timeout,omitempty"`
	// Replay lists reads traffic capture saw production answer, which each
	// run sends its copy once the copy is ready: a copy works only if it
	// answers every one, without a server error unless its world's
	// datastores are throwaway and empty.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Replay []ReplayRequest `json:"replay,omitempty"`
	// Overrides, for verify-fix, make the candidate the failing revision
	// with these values in place: a limit learned from healthy use, raced
	// before the pull request that sets it opens.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Overrides []ValueOverride `json:"overrides,omitempty"`
}

// ValueOverride is one value a candidate sets: a container's resource
// limit or request, as "limits.memory" or "requests.cpu".
type ValueOverride struct {
	Container string `json:"container"`
	// +kubebuilder:validation:Enum=limits.memory;limits.cpu;requests.memory;requests.cpu
	Field string `json:"field"`
	Value string `json:"value"`
}

// ReplayRequest is one GET a run sends its copy.
type ReplayRequest struct {
	// Path, with no query string.
	// +kubebuilder:validation:Pattern=`^/[^?#\s]*$`
	// +kubebuilder:validation:MaxLength=1024
	Path string `json:"path"`
	// Port the copy serves it on; its first declared port when omitted.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// ExperimentPhase is a step in an experiment's lifecycle.
// +kubebuilder:validation:Enum=Pending;Sealing;Running;Passed;Failed;NotSure;Aborted
type ExperimentPhase string

// ExperimentStatus is written by the Lab.
type ExperimentStatus struct {
	// +optional
	Phase ExperimentPhase `json:"phase,omitempty"`
	// Runs is the number of paired runs completed.
	// +optional
	Runs int32 `json:"runs,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Fidelity lists every way the Lab's copy differs from production.
	// +optional
	// +listType=atomic
	Fidelity []string `json:"fidelity,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Experiment is one Lab run: reproduce a failure, or judge a fix against it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=exp,categories=sentinel
// +kubebuilder:printcolumn:name="Purpose",type=string,JSONPath=`.spec.purpose`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.runs`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Experiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExperimentSpec   `json:"spec"`
	Status ExperimentStatus `json:"status,omitempty"`
}

// ExperimentList is a list of Experiment.
//
// +kubebuilder:object:root=true
type ExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Experiment `json:"items"`
}
