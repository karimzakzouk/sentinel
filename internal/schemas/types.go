// Package schemas defines the canonical types that flow between every layer
// of Sentinel. These are the Go equivalents of the Python Pydantic models in
// services/common/sentinel_common/schemas.py.
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
// Supports BOTH approaches:
//   - Search/Replace (preferred): LLM returns Changes (list of search→replace
//     blocks). Sentinel finds the exact "search" text in the file and swaps it
//     with "replace". Minimal, surgical, no full-file rewrite needed. If the
//     search text isn't found, that change is skipped (no corruption).
//   - Full file (legacy): LLM returns NewContent with the complete file.
//     Sentinel replaces the entire file. Kept for backward compatibility.
// Search/Replace is the default — the fix proposer prompt asks for it.
// If the LLM returns new_content instead, that path still works.

type FileChange struct {
        Path       string        `json:"path"`
        NewContent string        `json:"new_content,omitempty"` // legacy: full file replacement
        Changes    []SearchReplace `json:"changes,omitempty"`    // v3.9.0: surgical edits
}

// SearchReplace is one surgical edit: find "search" in the file, replace
// with "replace". The search block must be unique enough to match exactly
// one location in the file. If it matches zero or multiple locations, the
// change is skipped.

type SearchReplace struct {
        Search  string `json:"search"`
        Replace string `json:"replace"`
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
        // Sandbox failure feedback. When the sandbox verification fails,
        // the failure details are stored here and fed back to the fix proposer on
        // the next retry. This implements the "generate → verify → feed error back
        // → retry" loop that Aider/OpenHands/SWE-agent all use.

        SandboxFeedback    *string              `json:"sandbox_feedback,omitempty"`
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

        // PR linkage (populated by the v4.0.0 DB migration + UpdateIncidentPR).
        // Also mirrored to State.ExecutionResult for backwards compat. The
        // frontend reads these top-level fields for the PRs tab + incident detail.
        PRRepo    string  `json:"pr_repo,omitempty"`
        PRNumber  *int    `json:"pr_number,omitempty"`
        PRBranch  string  `json:"pr_branch,omitempty"`
        PRURL     string  `json:"pr_url,omitempty"`

        // Auto-merge status (set by the auto-merger as it tracks + completes PRs).
        AutoMergeStatus string `json:"auto_merge_status,omitempty"`

        // Sandbox outcome (mirrored from State.ExecutionResult for the frontend).
        SandboxVerified bool   `json:"sandbox_verified,omitempty"`
        SandboxSummary  string `json:"sandbox_summary,omitempty"`

        // FixType (mirrored from State.FixProposal.FixType for the frontend).
        FixType string `json:"fix_type,omitempty"`
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

// ─── Fix verification types ──────────────────────────────────────────────────
// These were
// (LEAK-1 from the architecture audit). Moved here so the executors package
// doesn't have to import agents — outer adapter was reaching into inner
// reasoning layer. The agents package keeps thin wrappers for back-compat.
// ParsedError is the structured form of a runtime error extracted from logs.
// Multi-language: works for Python, Go, Java, JavaScript, Rust.

type ParsedError struct {
        // ErrorType is the error class (e.g. "KeyError", "panic",
        // "NullPointerException", "TypeError").

        ErrorType string

        // ErrorDetail is the error-specific detail (e.g. "'DATABASE_URL'",
        // "runtime error: index out of range").

        ErrorDetail string

        // SourceFile is the file path from the stack trace (e.g.
        // "app/main.py", "src/handler.go", "com/acme/Foo.java").

        SourceFile string

        // SourceLine is the line number as a string (e.g. "11", "42", "1337").

        SourceLine string

        // Function is the function/method name extracted from the stack
        // trace frame (e.g. "get_config", "main.handler", "Foo.bar").
        // Universal — works across Python, Go, Java, JS, Rust, Ruby, C#.
        Function string

        // Language is the detected language: "python", "go", "java",
        // "javascript", "rust", or "unknown".

        Language string

        // RawLine is the full original error line from the logs.

        RawLine string
}

// GeneratedTest is an LLM-generated test that reproduces the original error.
// Used by sandbox Level 4 verification — if the test fails against the
// canary, the fix didn't actually fix the bug.

type GeneratedTest struct {
        TestFile      string `json:"test_file"`
        TestFramework string `json:"test_framework"`
        TestLanguage  string `json:"test_language"`
        TestContent   string `json:"test_content"`
        TestCommand   string `json:"test_command"`
        Description   string `json:"description"`
}

// GeneratedReproducer is an LLM-generated reproducer script. Used by sandbox
// Level 5 verification (strongest) — runs the script against the canary and
// checks for non-zero exit.

type GeneratedReproducer struct {
        ScriptFile     string `json:"script_file"`
        ScriptLanguage string `json:"script_language"`
        ScriptContent  string `json:"script_content"`
        RunCommand     string `json:"run_command"`
        Description    string `json:"description"`
}

// GitHubAppStatus is the status of the GitHub App configuration.
// Used by both the API layer (GET /settings/github-app) and the runtime
// config manager. Lives in schemas to avoid a circular dependency between
// api and runtimecfg.
type GitHubAppStatus struct {
        Configured bool   `json:"configured"`
        AppID      string `json:"app_id"`
        Slug       string `json:"slug"`
        InstallID  string `json:"install_id"`
}

// ProviderConfig is the configuration for a single LLM provider entry.
// Used by the config layer (env var loading) and the llm layer (pool).
// Lives in schemas to avoid config → llm circular dependency.
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

// IsEnabled returns true unless Enabled is explicitly false.
func (c *ProviderConfig) IsEnabled() bool {
        return c.Enabled == nil || *c.Enabled
}
