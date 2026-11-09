package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExperimentSpec describes one sealed Lab run. It cannot be changed after creation.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
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
