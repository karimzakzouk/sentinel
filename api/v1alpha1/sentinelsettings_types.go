package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SentinelSettingsSpec holds cluster-wide knobs operators may change without a
// Helm reinstall. Install-time choices (namespaces, privileged agents, forge
// secret mounts, Lab pool size) stay in the chart; this object covers what
// can take effect by rewriting Sentinel's own ConfigMaps.
type SentinelSettingsSpec struct {
	// LogLevel is debug, info, warn or error.
	// +optional
	// +kubebuilder:validation:Enum=debug;info;warn;error
	LogLevel string `json:"logLevel,omitempty"`
	// Detection thresholds and windows for the count detector.
	// +optional
	Detection DetectionSettings `json:"detection,omitempty"`
	// Logs turns container-log evidence and sampling on or off.
	// +optional
	Logs LogsSettings `json:"logs,omitempty"`
	// Lab holds Lab behaviour that does not resize the pool.
	// +optional
	Lab LabSettings `json:"lab,omitempty"`
	// Actor holds the kill switch and hourly budget once the actor is installed.
	// +optional
	Actor ActorSettings `json:"actor,omitempty"`
	// Fixer holds code-fix behaviour once the Fixer is installed. Forge
	// URLs and token Secret mounts stay in Helm.
	// +optional
	Fixer FixerSettings `json:"fixer,omitempty"`
	// Capture holds cluster-wide traffic-capture behaviour once the capture
	// agent is installed. Per-namespace recording stays on SentinelPolicy.
	// +optional
	Capture CaptureSettings `json:"capture,omitempty"`
	// Capabilities holds how often capability probes run.
	// +optional
	Capabilities CapabilitiesSettings `json:"capabilities,omitempty"`
}

// DetectionSettings is the cluster-wide false-alarm budget and timing.
type DetectionSettings struct {
	// FalseAlarmsPerWeek is the accepted false alarms per workload per week.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	FalseAlarmsPerWeek *int32 `json:"falseAlarmsPerWeek,omitempty"`
	// Window is the detection window.
	// +optional
	Window *metav1.Duration `json:"window,omitempty"`
	// Tick is how often detectors evaluate.
	// +optional
	Tick *metav1.Duration `json:"tick,omitempty"`
	// HalfLife is how fast old history fades.
	// +optional
	HalfLife *metav1.Duration `json:"halfLife,omitempty"`
	// IncidentGap is how long an incident stays open after signals go quiet.
	// +optional
	IncidentGap *metav1.Duration `json:"incidentGap,omitempty"`
	// WarmUp is how long a workload learns before it may alarm.
	// +optional
	WarmUp *metav1.Duration `json:"warmUp,omitempty"`
}

// LogsSettings configures log evidence collection.
type LogsSettings struct {
	// Enabled defaults to true when unset on the Settings object only after
	// Helm has seeded it; an explicit false turns collection off.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// SampleInterval is how often each workload's errors are sampled; 0s
	// turns sampling off.
	// +optional
	SampleInterval *metav1.Duration `json:"sampleInterval,omitempty"`
}

// LabSettings is Lab behaviour that does not create namespaces.
type LabSettings struct {
	// VerifyRollbacks asks the Lab whether each proposed rollback fixes its
	// incident.
	// +optional
	VerifyRollbacks *bool `json:"verifyRollbacks,omitempty"`
}

// ActorSettings is the actor's kill switch and budget.
type ActorSettings struct {
	// ActionsEnabled is the kill switch: false rejects every request.
	// +optional
	ActionsEnabled *bool `json:"actionsEnabled,omitempty"`
	// PerHour bounds actions per namespace per hour.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	PerHour *int32 `json:"perHour,omitempty"`
}

// FixerSettings is Fixer behaviour once it is installed.
type FixerSettings struct {
	// Code holds fixers that change source, beside pinning an image back.
	// +optional
	Code FixerCodeSettings `json:"code,omitempty"`
}

// FixerCodeSettings names which code fixers run and the optional LLM.
type FixerCodeSettings struct {
	// WithoutRollback also asks for a code fix when nothing was rolled back.
	// +optional
	WithoutRollback *bool `json:"withoutRollback,omitempty"`
	// Fixers are tried in order: restore, template, mine, revert, llm.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	Fixers []string `json:"fixers,omitempty"`
	// LLM is the model the llm fixer asks. Its API key Secret stays a Helm
	// mount; only URL, model, format and attempts are set here.
	// +optional
	LLM *FixerLLMSettings `json:"llm,omitempty"`
}

// FixerLLMSettings is the language model the llm fixer calls.
type FixerLLMSettings struct {
	// Format is openai or anthropic.
	// +optional
	// +kubebuilder:validation:Enum=openai;anthropic
	Format string `json:"format,omitempty"`
	// URL is the model's API base URL. Empty turns the llm fixer off.
	// +optional
	URL string `json:"url,omitempty"`
	// Model is the model name.
	// +optional
	Model string `json:"model,omitempty"`
	// MaxAttempts bounds model changes per fix.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	MaxAttempts *int32 `json:"maxAttempts,omitempty"`
}

// CaptureSettings is cluster-wide traffic capture once the agent is installed.
type CaptureSettings struct {
	// ExportBodies asks OBI to export HTTP body attributes on traces.
	// Namespaces still need SentinelPolicy capture.bodies to keep them.
	// +optional
	ExportBodies *bool `json:"exportBodies,omitempty"`
	// BodyBufferBytes is OBI's per-side HTTP buffer for body extraction.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=65536
	BodyBufferBytes *int32 `json:"bodyBufferBytes,omitempty"`
	// MitmEnabled runs the HTTPS CONNECT MITM that posts bodies for clients
	// that trust the capture CA and set HTTPS_PROXY. Requires capture.mitm
	// installed by Helm.
	// +optional
	MitmEnabled *bool `json:"mitmEnabled,omitempty"`
	// MitmInject copies the capture CA into namespaces that record bodies and
	// labels them so the mutating webhook mounts the CA and sets HTTPS_PROXY
	// on new pods. Apps that pin certificates stay out of reach.
	// +optional
	MitmInject *bool `json:"mitmInject,omitempty"`
}

// CapabilitiesSettings holds capability-probe timing.
type CapabilitiesSettings struct {
	// Interval between capability reports.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// InstalledComponents reports which chart components exist so the console can
// grey out knobs that need a Helm install first.
type InstalledComponents struct {
	// +optional
	Actor bool `json:"actor,omitempty"`
	// +optional
	Lab bool `json:"lab,omitempty"`
	// +optional
	Fixer bool `json:"fixer,omitempty"`
	// +optional
	Capture bool `json:"capture,omitempty"`
	// +optional
	CaptureMITM bool `json:"captureMitm,omitempty"`
	// +optional
	NodeAgent bool `json:"nodeAgent,omitempty"`
}

// SentinelSettingsStatus is what the controller last applied.
type SentinelSettingsStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	Installed InstalledComponents `json:"installed,omitempty"`
}

// SentinelSettings is the cluster-wide control plane for runtime knobs.
// One object named "sentinel" lives in Sentinel's own namespace.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sset,categories=sentinel
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SentinelSettings struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SentinelSettingsSpec   `json:"spec,omitempty"`
	Status SentinelSettingsStatus `json:"status,omitempty"`
}

// SentinelSettingsList is a list of SentinelSettings.
//
// +kubebuilder:object:root=true
type SentinelSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SentinelSettings `json:"items"`
}
