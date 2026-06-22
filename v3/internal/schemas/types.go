// Package schemas defines the canonical types that flow between every layer
// of Sentinel. These are the Go equivalents of the Python Pydantic models in
// services/common/sentinel_common/schemas.py.
//
// Every layer reads from and writes to these types. Validation happens at
// JSON unmarshal time via struct tags.
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

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// IncidentStatus is the lifecycle state of an incident.
type IncidentStatus string

const (
	StatusDetected          IncidentStatus = "detected"
	StatusInvestigating     IncidentStatus = "investigating"
	StatusRootCaused        IncidentStatus = "root_caused"
	StatusFixProposed       IncidentStatus = "fix_proposed"
	StatusGovernanceReview  IncidentStatus = "governance_review"
	StatusApproved          IncidentStatus = "approved"
	StatusRejected          IncidentStatus = "rejected"
	StatusExecuting         IncidentStatus = "executing"
	StatusVerified          IncidentStatus = "verified"
	StatusFailed            IncidentStatus = "failed"
	StatusEscalated         IncidentStatus = "escalated"
	StatusResolved          IncidentStatus = "resolved"
)

type FixType string

const (
	FixRestart       FixType = "restart"
	FixScale         FixType = "scale"
	FixCodeFix       FixType = "code_fix"
	FixRollback      FixType = "rollback"
	FixRollForward   FixType = "roll_forward"
	FixInfrastructure FixType = "infrastructure"
)

type GovernanceVerdict string

const (
	VerdictAllow                GovernanceVerdict = "allow"
	VerdictRequireHumanApproval GovernanceVerdict = "require_human_approval"
	VerdictDeny                 GovernanceVerdict = "deny"
)

// ---------- Layer 1: Ingestion ----------

// Signal is the canonical cleaned/normalized telemetry event.
type Signal struct {
	SignalID      uuid.UUID       `json:"signal_id"`
	Ts            time.Time       `json:"ts"`
	Source        SignalSource    `json:"source"`
	Service       string          `json:"service"`
	Namespace     string          `json:"namespace"`
	Cluster       string          `json:"cluster"`
	Payload       map[string]any  `json:"payload"`
	TraceID       *string         `json:"trace_id,omitempty"`
	SpanID        *string         `json:"span_id,omitempty"`
	GitCommit     *string         `json:"git_commit,omitempty"`
	SchemaVersion string          `json:"schema_version"`
}

// ---------- Layer 2: Detection ----------

// Anomaly is a single anomalous observation flagged by a detector.
type Anomaly struct {
	AnomalyID   uuid.UUID       `json:"anomaly_id"`
	Ts          time.Time       `json:"ts"`
	SignalID    uuid.UUID       `json:"signal_id"`
	Service     string          `json:"service"`
	Namespace   string          `json:"namespace"`
	Detector    string          `json:"detector"`
	Score       float64         `json:"score"`
	Dimensions  map[string]float64 `json:"dimensions"`
	Explanation string          `json:"explanation"`
	Annotations map[string]string `json:"annotations"`
}

// AnomalyCluster is a group of anomalies the detector decided belong together.
type AnomalyCluster struct {
	ClusterID   uuid.UUID       `json:"cluster_id"`
	Ts          time.Time       `json:"ts"`
	Service     string          `json:"service"`
	Namespace   string          `json:"namespace"`
	Anomalies   []Anomaly       `json:"anomalies"`
	Summary     string          `json:"summary"`
	TraceID     *string         `json:"trace_id,omitempty"`
	Severity    Severity        `json:"severity"`
	Annotations map[string]string `json:"annotations"`
}

// ---------- Layer 3: Reasoning ----------

// Hypothesis is one candidate root cause proposed by the Investigator.
type Hypothesis struct {
	HID                   string   `json:"h_id"`
	Statement             string   `json:"statement"`
	SupportingEvidence    []string `json:"supporting_evidence"`
	ContradictingEvidence []string `json:"contradicting_evidence"`
	Confidence            float64  `json:"confidence"`
	Falsified             bool     `json:"falsified"`
	SourceFiles           []string `json:"source_files"`
}

// Evidence is a piece of evidence the Evidence Gatherer collected.
type Evidence struct {
	EID                    string         `json:"e_id"`
	Source                 string         `json:"source"`
	Query                  string         `json:"query"`
	Result                 map[string]any `json:"result"`
	SupportsHypothesisID   *string        `json:"supports_hypothesis_id,omitempty"`
	ContradictsHypothesisID *string       `json:"contradicts_hypothesis_id,omitempty"`
	Ts                     time.Time      `json:"ts"`
}

// RootCause is the survivor hypothesis after the Analyst's falsification round.
type RootCause struct {
	Statement          string   `json:"statement"`
	Confidence         float64  `json:"confidence"`
	EvidenceIDs        []string `json:"evidence_ids"`
	SourceFiles        []string `json:"source_files"`
	Category           string   `json:"category"`
	HasSchemaMigration bool     `json:"has_schema_migration"`
	GitCommit          *string  `json:"git_commit,omitempty"`
}

// FileChange is one file modification in a fix proposal.
type FileChange struct {
	Path        string `json:"path"`
	NewContent  string `json:"new_content"`
}

// FixProposal is the Fix Proposer's translation of a root cause into a concrete action.
type FixProposal struct {
	FixID             uuid.UUID      `json:"fix_id"`
	FixType           FixType        `json:"fix_type"`
	TargetService     string         `json:"target_service"`
	TargetNamespace   string         `json:"target_namespace"`
	Description       string         `json:"description"`
	Repo              *string        `json:"repo,omitempty"`
	BaseBranch        *string        `json:"base_branch,omitempty"`
	BranchName        *string        `json:"branch_name,omitempty"`
	FilesToChange     []FileChange   `json:"files_to_change"`
	PRTitle           *string        `json:"pr_title,omitempty"`
	PRBody            *string        `json:"pr_body,omitempty"`
	DockerfilePath    *string        `json:"dockerfile_path,omitempty"`
	CanaryImage       *string        `json:"canary_image,omitempty"`
	K8sAction         map[string]any `json:"k8s_action,omitempty"`
	RequiresSandbox   bool           `json:"requires_sandbox"`
	ProposedConfidence float64       `json:"proposed_confidence"`
}

// RewindEvent is the recursive-reasoning escape hatch.
type RewindEvent struct {
	RaisedBy   string         `json:"raised_by"`
	Reason     string         `json:"reason"`
	NewContext map[string]any `json:"new_context"`
	Ts         time.Time      `json:"ts"`
}

// ---------- Layer 4: Safety / Governance ----------

// GovernanceDecision is the verdict for a proposed fix.
type GovernanceDecision struct {
	Verdict                     GovernanceVerdict `json:"verdict"`
	ConfidenceAtDecision        float64           `json:"confidence_at_decision"`
	RulesEvaluated              []map[string]any  `json:"rules_evaluated"`
	ApprovalRequestID           *string           `json:"approval_request_id,omitempty"`
	ApprovalChannel             *string           `json:"approval_channel,omitempty"`
	RollbackBlockedReason       *string           `json:"rollback_blocked_reason,omitempty"`
	SchemaMigrationBlocksRollback bool            `json:"schema_migration_blocks_rollback"`
	DecidedAt                   time.Time         `json:"decided_at"`
}

// ---------- Layer 5: Execution ----------

// ExecutionResult is the outcome of executing a FixProposal.
type ExecutionResult struct {
	FixID            uuid.UUID      `json:"fix_id"`
	FixType          FixType        `json:"fix_type"`
	Success          bool           `json:"success"`
	PRURL            *string        `json:"pr_url,omitempty"`
	PRNumber         *int           `json:"pr_number,omitempty"`
	PRBranch         *string        `json:"pr_branch,omitempty"`
	K8sResponse      map[string]any `json:"k8s_response,omitempty"`
	SandboxVerified  bool           `json:"sandbox_verified"`
	SandboxSummary   *string        `json:"sandbox_summary,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	Error            *string        `json:"error,omitempty"`
}

// AgentState is the structured object passed between agents in the state machine.
type AgentState struct {
	IncidentID         uuid.UUID            `json:"incident_id"`
	Cluster            AnomalyCluster       `json:"cluster"`
	Hypotheses         []Hypothesis         `json:"hypotheses"`
	Evidence           []Evidence           `json:"evidence"`
	RootCause          *RootCause           `json:"root_cause,omitempty"`
	FixProposal        *FixProposal         `json:"fix_proposal,omitempty"`
	GovernanceDecision *GovernanceDecision  `json:"governance_decision,omitempty"`
	ExecutionResult    *ExecutionResult     `json:"execution_result,omitempty"`
	RetrievedIncidents []map[string]any     `json:"retrieved_incidents"`
	SourceFiles        []string             `json:"source_files"`
	FetchedCode        map[string]string    `json:"fetched_code"`
	Runbooks           []string             `json:"runbooks"`
	RewindEvents       []RewindEvent        `json:"rewind_events"`
	AgentTrace         []map[string]any     `json:"agent_trace"`
	Iterations         int                  `json:"iterations"`
	MaxIterations      int                  `json:"max_iterations"`
	Status             IncidentStatus       `json:"status"`
	Error              *string              `json:"error,omitempty"`
}

// ---------- Layer 6: Feedback ----------

// Incident is the end-to-end incident record. One Incident == one full Sentinel run.
type Incident struct {
	IncidentID          uuid.UUID      `json:"incident_id"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	Cluster             AnomalyCluster `json:"cluster"`
	State               AgentState     `json:"state"`
	Status              IncidentStatus `json:"status"`
	BetaAlpha           float64        `json:"beta_alpha"`
	BetaBeta            float64        `json:"beta_beta"`
	WeaviateUUID        *string        `json:"weaviate_uuid,omitempty"` // kept for compat, unused in v3
	GitCommitAtResolution *string      `json:"git_commit_at_resolution,omitempty"`
	Postmortem          *string        `json:"postmortem,omitempty"`
}

// NewAgentState returns a fresh AgentState with defaults.
func NewAgentState(incidentID uuid.UUID, cluster AnomalyCluster) AgentState {
	return AgentState{
		IncidentID:    incidentID,
		Cluster:       cluster,
		MaxIterations: 5,
		Status:        StatusInvestigating,
	}
}

// NewIncident returns a fresh Incident for a cluster.
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
