// Package schemas defines the canonical types that flow between every layer
// of Sentinel. Every package reads from and writes to these types; validation
// happens at JSON unmarshal time via struct tags.
package schemas

import (
	"time"

	"github.com/google/uuid"
)

// SignalSource is the telemetry source a Signal came from.
type SignalSource string

const (
	SignalSourcePrometheus   SignalSource = "prometheus"
	SignalSourceCloudwatch   SignalSource = "cloudwatch"
	SignalSourceLoki         SignalSource = "loki"
	SignalSourceJaeger       SignalSource = "jaeger"
	SignalSourceK8sEvents    SignalSource = "k8s_events"
	SignalSourceCDHistory    SignalSource = "cd_history"
	SignalSourceConfigChange SignalSource = "config_change"
)

// Severity rates the urgency of an anomaly cluster.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// IncidentStatus is the lifecycle state of an incident.
type IncidentStatus string

const (
	StatusDetected         IncidentStatus = "detected"
	StatusInvestigating    IncidentStatus = "investigating"
	StatusRootCaused       IncidentStatus = "root_caused"
	StatusFixProposed      IncidentStatus = "fix_proposed"
	StatusGovernanceReview IncidentStatus = "governance_review"
	StatusApproved         IncidentStatus = "approved"
	StatusRejected         IncidentStatus = "rejected"
	StatusExecuting        IncidentStatus = "executing"
	StatusVerified         IncidentStatus = "verified"
	StatusFailed           IncidentStatus = "failed"
	StatusEscalated        IncidentStatus = "escalated"
	StatusResolved         IncidentStatus = "resolved"
)

// FixType categorizes the kind of remediation Sentinel proposes.
type FixType string

const (
	FixRestart        FixType = "restart"
	FixScale          FixType = "scale"
	FixCodeFix        FixType = "code_fix"
	FixRollback       FixType = "rollback"
	FixRollForward    FixType = "roll_forward"
	FixInfrastructure FixType = "infrastructure"
)

// GovernanceVerdict is the policy decision returned for a proposed fix.
type GovernanceVerdict string

const (
	VerdictAllow                GovernanceVerdict = "allow"
	VerdictRequireHumanApproval GovernanceVerdict = "require_human_approval"
	VerdictDeny                 GovernanceVerdict = "deny"
)

// Signal is the canonical cleaned/normalized telemetry event.
type Signal struct {
	SignalID      uuid.UUID      `json:"signal_id"`
	TS            time.Time      `json:"ts"`
	Source        SignalSource   `json:"source"`
	Service       string         `json:"service"`
	Namespace     string         `json:"namespace"`
	Cluster       string         `json:"cluster"`
	Payload       map[string]any `json:"payload"`
	TraceID       *string        `json:"trace_id,omitempty"`
	SpanID        *string        `json:"span_id,omitempty"`
	GitCommit     *string        `json:"git_commit,omitempty"`
	SchemaVersion string         `json:"schema_version"`
}

// Anomaly is a single anomalous observation flagged by a detector.
type Anomaly struct {
	AnomalyID   uuid.UUID          `json:"anomaly_id"`
	TS          time.Time          `json:"ts"`
	SignalID    uuid.UUID          `json:"signal_id"`
	Service     string             `json:"service"`
	Namespace   string             `json:"namespace"`
	Detector    string             `json:"detector"`
	Score       float64            `json:"score"`
	Dimensions  map[string]float64 `json:"dimensions"`
	Explanation string             `json:"explanation"`
	Annotations map[string]string  `json:"annotations"`
}

// AnomalyCluster is a group of anomalies the detector decided belong together.
type AnomalyCluster struct {
	ClusterID   uuid.UUID         `json:"cluster_id"`
	TS          time.Time         `json:"ts"`
	Service     string            `json:"service"`
	Namespace   string            `json:"namespace"`
	Anomalies   []Anomaly         `json:"anomalies"`
	Summary     string            `json:"summary"`
	TraceID     *string           `json:"trace_id,omitempty"`
	Severity    Severity          `json:"severity"`
	Annotations map[string]string `json:"annotations"`
}

// Hypothesis is one candidate root cause proposed by the investigator.
type Hypothesis struct {
	HID                   string   `json:"h_id"`
	Statement             string   `json:"statement"`
	SupportingEvidence    []string `json:"supporting_evidence"`
	ContradictingEvidence []string `json:"contradicting_evidence"`
	Confidence            float64  `json:"confidence"`
	Falsified             bool     `json:"falsified"`
	SourceFiles           []string `json:"source_files"`
}

// Evidence is a single piece of evidence collected for or against a hypothesis.
type Evidence struct {
	EID                     string         `json:"e_id"`
	Source                  string         `json:"source"`
	Query                   string         `json:"query"`
	Result                  map[string]any `json:"result"`
	SupportsHypothesisID    *string        `json:"supports_hypothesis_id,omitempty"`
	ContradictsHypothesisID *string        `json:"contradicts_hypothesis_id,omitempty"`
	TS                      time.Time      `json:"ts"`
}

// RootCause is the survivor hypothesis after the analyst's falsification round.
type RootCause struct {
	Statement          string   `json:"statement"`
	Confidence         float64  `json:"confidence"`
	EvidenceIDs        []string `json:"evidence_ids"`
	SourceFiles        []string `json:"source_files"`
	Category           string   `json:"category"`
	HasSchemaMigration bool     `json:"has_schema_migration"`
	GitCommit          *string  `json:"git_commit,omitempty"`
}

// SearchReplace is one surgical edit: find Search in the file and replace it
// with Replace. The search text must match exactly one location; zero or
// multiple matches cause the change to be skipped.
type SearchReplace struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

// FileChange describes a modification to a single file. Either NewContent
// (full-file replacement) or Changes (surgical search/replace edits) is set.
// Search/Replace is preferred; full-file replacement is the fallback path
// for LLMs that emit it.
type FileChange struct {
	Path       string          `json:"path"`
	NewContent string          `json:"new_content,omitempty"`
	Changes    []SearchReplace `json:"changes,omitempty"`
}

// FixProposal is the fix proposer's translation of a root cause into an action.
type FixProposal struct {
	FixID              uuid.UUID      `json:"fix_id"`
	FixType            FixType        `json:"fix_type"`
	TargetService      string         `json:"target_service"`
	TargetNamespace    string         `json:"target_namespace"`
	Description        string         `json:"description"`
	Repo               *string        `json:"repo,omitempty"`
	BaseBranch         *string        `json:"base_branch,omitempty"`
	BranchName         *string        `json:"branch_name,omitempty"`
	FilesToChange      []FileChange   `json:"files_to_change"`
	PRTitle            *string        `json:"pr_title,omitempty"`
	PRBody             *string        `json:"pr_body,omitempty"`
	DockerfilePath     *string        `json:"dockerfile_path,omitempty"`
	CanaryImage        *string        `json:"canary_image,omitempty"`
	K8sAction          map[string]any `json:"k8s_action,omitempty"`
	RequiresSandbox    bool           `json:"requires_sandbox"`
	ProposedConfidence float64        `json:"proposed_confidence"`
}

// RewindEvent is the recursive-reasoning escape hatch: an agent raises one
// when it needs the pipeline to restart from an earlier stage with new context.
type RewindEvent struct {
	RaisedBy   string         `json:"raised_by"`
	Reason     string         `json:"reason"`
	NewContext map[string]any `json:"new_context"`
	TS         time.Time      `json:"ts"`
}

// GovernanceDecision is the verdict returned by the policy engine for a fix.
type GovernanceDecision struct {
	Verdict                       GovernanceVerdict `json:"verdict"`
	ConfidenceAtDecision          float64           `json:"confidence_at_decision"`
	RulesEvaluated                []map[string]any  `json:"rules_evaluated"`
	ApprovalRequestID             *string           `json:"approval_request_id,omitempty"`
	ApprovalChannel               *string           `json:"approval_channel,omitempty"`
	RollbackBlockedReason         *string           `json:"rollback_blocked_reason,omitempty"`
	SchemaMigrationBlocksRollback bool              `json:"schema_migration_blocks_rollback"`
	DecidedAt                     time.Time         `json:"decided_at"`
}

// ExecutionResult is the outcome of executing a FixProposal.
type ExecutionResult struct {
	FixID           uuid.UUID      `json:"fix_id"`
	FixType         FixType        `json:"fix_type"`
	Success         bool           `json:"success"`
	PRURL           *string        `json:"pr_url,omitempty"`
	PRNumber        *int           `json:"pr_number,omitempty"`
	PRBranch        *string        `json:"pr_branch,omitempty"`
	K8sResponse     map[string]any `json:"k8s_response,omitempty"`
	SandboxVerified bool           `json:"sandbox_verified"`
	// SandboxLevels records how far sandbox verification got, e.g.
	// "L1-L5" (full) or "L1-L3" (no canary image — L4/L5 skipped).
	SandboxLevels  string     `json:"sandbox_levels,omitempty"`
	SandboxSummary *string    `json:"sandbox_summary,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Error          *string    `json:"error,omitempty"`

	// SandboxOverride and CIOverride are admin-set bypass flags
	// (/sentinel override sandbox|ci). When true, the auto-merge policy
	// treats the corresponding gate as passed without running the check.
	// Every use is audit-logged.
	SandboxOverride bool `json:"sandbox_override,omitempty"`
	CIOverride      bool `json:"ci_override,omitempty"`
}

// PodRuntimeContext is ground truth about the crashing workload's
// runtime configuration, read from the live pod spec (v1.7.4). It exists
// so the investigation and fix-proposal stages never have to GUESS what
// the environment provides — the user-api incident of eval run #11
// produced a harmful fix because the LLM assumed a canonical
// `DATABASE_URL` env var while the deployment actually sets a
// service-prefixed variant. Env var VALUES are deliberately excluded:
// only names and references cross the LLM boundary.
type PodRuntimeContext struct {
	PodName       string `json:"pod_name"`
	ContainerName string `json:"container_name,omitempty"`
	Image         string `json:"image,omitempty"`
	// EnvVarNames are the NAMES of directly-set environment variables
	// (values never collected).
	EnvVarNames []string `json:"env_var_names,omitempty"`
	// ConfigMapRefs / SecretRefs are the names of configmaps/secrets the
	// container sources env vars or volume mounts from.
	ConfigMapRefs []string `json:"config_map_refs,omitempty"`
	SecretRefs    []string `json:"secret_refs,omitempty"`
	// Resources are the container's requests/limits as formatted strings.
	CPURequest    string `json:"cpu_request,omitempty"`
	MemoryRequest string `json:"memory_request,omitempty"`
	CPULimit      string `json:"cpu_limit,omitempty"`
	MemoryLimit   string `json:"memory_limit,omitempty"`
	// Command/Args expose the container entrypoint (first token only) —
	// relevant for diagnosing OOM and crash-loop shapes without leaking
	// argument secrets.
	Command []string `json:"command,omitempty"`
}

// AgentState is the structured object passed between agents in the pipeline.
type AgentState struct {
	IncidentID         uuid.UUID           `json:"incident_id"`
	Cluster            AnomalyCluster      `json:"cluster"`
	Hypotheses         []Hypothesis        `json:"hypotheses"`
	Evidence           []Evidence          `json:"evidence"`
	RootCause          *RootCause          `json:"root_cause,omitempty"`
	FixProposal        *FixProposal        `json:"fix_proposal,omitempty"`
	GovernanceDecision *GovernanceDecision `json:"governance_decision,omitempty"`
	ExecutionResult    *ExecutionResult    `json:"execution_result,omitempty"`
	RetrievedIncidents []map[string]any    `json:"retrieved_incidents"`
	SourceFiles        []string            `json:"source_files"`
	FetchedCode        map[string]string   `json:"fetched_code"`
	Runbooks           []string            `json:"runbooks"`
	RewindEvents       []RewindEvent       `json:"rewind_events"`
	AgentTrace         []map[string]any    `json:"agent_trace"`
	Iterations         int                 `json:"iterations"`
	MaxIterations      int                 `json:"max_iterations"`
	Status             IncidentStatus      `json:"status"`
	Error              *string             `json:"error,omitempty"`

	// SandboxFeedback carries the failure details from a failed sandbox
	// verification back to the fix proposer on the next retry.
	SandboxFeedback *string `json:"sandbox_feedback,omitempty"`

	// RuntimeContext is the live pod's configuration ground truth (env var
	// names, config refs, resource limits), fetched by the investigator so
	// hypothesis-forming and fix-writing never reason blind about the
	// environment (v1.7.4). Nil when the pod could not be inspected.
	RuntimeContext *PodRuntimeContext `json:"runtime_context,omitempty"`
}

// Incident is the end-to-end incident record. One Incident == one Sentinel run.
type Incident struct {
	IncidentID            uuid.UUID      `json:"incident_id"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	Cluster               AnomalyCluster `json:"cluster"`
	State                 AgentState     `json:"state"`
	Status                IncidentStatus `json:"status"`
	BetaAlpha             float64        `json:"beta_alpha"`
	BetaBeta              float64        `json:"beta_beta"`
	GitCommitAtResolution *string        `json:"git_commit_at_resolution,omitempty"`
	Postmortem            *string        `json:"postmortem,omitempty"`

	PRRepo          string `json:"pr_repo,omitempty"`
	PRNumber        *int   `json:"pr_number,omitempty"`
	PRBranch        string `json:"pr_branch,omitempty"`
	PRURL           string `json:"pr_url,omitempty"`
	AutoMergeStatus string `json:"auto_merge_status,omitempty"`

	// Sandbox + FixType mirrors of State.ExecutionResult for the frontend.
	SandboxVerified bool   `json:"sandbox_verified,omitempty"`
	SandboxSummary  string `json:"sandbox_summary,omitempty"`
	FixType         string `json:"fix_type,omitempty"`
}

// NewAgentState returns a fresh AgentState with default settings.
func NewAgentState(incidentID uuid.UUID, cluster AnomalyCluster) AgentState {
	return AgentState{
		IncidentID:    incidentID,
		Cluster:       cluster,
		MaxIterations: 5,
		Status:        StatusInvestigating,
	}
}

// NewIncident returns a fresh Incident for a cluster, with a uniform Beta(1,1)
// prior (no prior belief about whether the fix will succeed).
func NewIncident(cluster AnomalyCluster) Incident {
	id := uuid.New()
	now := time.Now().UTC()
	return Incident{
		IncidentID: id,
		CreatedAt:  now,
		UpdatedAt:  now,
		Cluster:    cluster,
		State:      NewAgentState(id, cluster),
		Status:     StatusInvestigating,
		BetaAlpha:  1.0,
		BetaBeta:   1.0,
	}
}

// ParsedError is the structured form of a runtime error extracted from logs.
// Multi-language: works for Python, Go, Java, JavaScript, Rust, Ruby, C#.
type ParsedError struct {
	ErrorType   string `json:"error_type"`
	ErrorDetail string `json:"error_detail"`
	SourceFile  string `json:"source_file"`
	SourceLine  string `json:"source_line"`
	Function    string `json:"function"`
	Language    string `json:"language"`
	RawLine     string `json:"raw_line"`
}

// GeneratedTest is an LLM-generated test that reproduces the original error.
// Used by sandbox Level 4 verification.
type GeneratedTest struct {
	TestFile      string `json:"test_file"`
	TestFramework string `json:"test_framework"`
	TestLanguage  string `json:"test_language"`
	TestContent   string `json:"test_content"`
	TestCommand   string `json:"test_command"`
	Description   string `json:"description"`
}

// GeneratedReproducer is an LLM-generated reproducer script. Used by sandbox
// Level 5 verification (the strongest signal).
type GeneratedReproducer struct {
	ScriptFile     string `json:"script_file"`
	ScriptLanguage string `json:"script_language"`
	ScriptContent  string `json:"script_content"`
	RunCommand     string `json:"run_command"`
	Description    string `json:"description"`
}

// GitHubAppStatus is the configuration state of the optional GitHub App.
type GitHubAppStatus struct {
	Configured bool   `json:"configured"`
	AppID      string `json:"app_id"`
	Slug       string `json:"slug"`
	InstallID  string `json:"install_id"`
}

// ProviderConfig is the configuration for a single LLM provider entry in
// the multi-provider failover pool.
type ProviderConfig struct {
	ID            string `json:"id" yaml:"id"`
	ProviderType  string `json:"provider" yaml:"provider"`
	BaseURL       string `json:"baseUrl,omitempty" yaml:"baseUrl,omitempty"`
	APIKey        string `json:"apiKey" yaml:"apiKey"`
	FastModel     string `json:"fastModel,omitempty" yaml:"fastModel,omitempty"`
	FrontierModel string `json:"frontierModel,omitempty" yaml:"frontierModel,omitempty"`
	Priority      int    `json:"priority,omitempty" yaml:"priority,omitempty"`
	Weight        int    `json:"weight,omitempty" yaml:"weight,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	BedrockRegion string `json:"bedrockRegion,omitempty" yaml:"bedrockRegion,omitempty"`
}

// IsEnabled reports whether the provider is enabled. Absent Enabled field
// means enabled (matches the zero value semantics).
func (c *ProviderConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}
