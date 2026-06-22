"""
Pydantic schemas for every signal flowing through Sentinel.

These schemas are the contract between every layer:
  Ingestion  -> Signal              (Layer 1 output)
  Detection  -> Anomaly / AnomalyCluster   (Layer 2 output)
  Reasoning  -> Incident, RootCause, FixProposal  (Layer 3 output)
  Safety     -> GovernanceDecision         (Layer 4 output)
  Execution  -> ExecutionResult            (Layer 5 output)
  Feedback   -> Incident (updated with outcome)   (Layer 6 output)

They are designed to be:
  - JSON-serializable (so they can flow through Kafka + Schema Registry)
  - Pydantic-validated (so a malformed payload is rejected at the gate)
  - Backward-compatible (new optional fields allowed, never remove/retype)
"""
from __future__ import annotations

from datetime import datetime, timezone
from enum import Enum
from typing import Any, Optional
from uuid import UUID, uuid4

from pydantic import BaseModel, Field, field_validator


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


# ---------- Enums ----------

class SignalSource(str, Enum):
    PROMETHEUS = "prometheus"
    CLOUDWATCH = "cloudwatch"
    LOKI = "loki"
    JAEGER = "jaeger"
    K8S_EVENTS = "k8s_events"
    CD_HISTORY = "cd_history"
    CONFIG_CHANGE = "config_change"


class Severity(str, Enum):
    INFO = "info"
    WARNING = "warning"
    CRITICAL = "critical"


class IncidentStatus(str, Enum):
    DETECTED = "detected"
    INVESTIGATING = "investigating"
    ROOT_CAUSED = "root_caused"
    FIX_PROPOSED = "fix_proposed"
    GOVERNANCE_REVIEW = "governance_review"
    APPROVED = "approved"
    REJECTED = "rejected"
    EXECUTING = "executing"
    VERIFIED = "verified"
    FAILED = "failed"
    ESCALATED = "escalated"
    RESOLVED = "resolved"


class FixType(str, Enum):
    RESTART = "restart"
    SCALE = "scale"
    CODE_FIX = "code_fix"
    ROLLBACK = "rollback"
    ROLL_FORWARD = "roll_forward"
    INFRASTRUCTURE = "infrastructure"


class GovernanceVerdict(str, Enum):
    ALLOW = "allow"
    REQUIRE_HUMAN_APPROVAL = "require_human_approval"
    DENY = "deny"


# ---------- Layer 1: Ingestion ----------

class Signal(BaseModel):
    """The canonical cleaned/normalized telemetry event that flows through Kafka.

    Every source format (Prometheus, Loki, Jaeger, K8s events, ...) is mapped
    to this shape by the ingestion layer before publishing.
    """
    signal_id: UUID = Field(default_factory=uuid4)
    ts: datetime = Field(default_factory=_utcnow, description="Event time (UTC)")
    source: SignalSource
    service: str = Field(..., description="Owning service name, e.g. 'payment-api'")
    namespace: str = Field(default="default")
    cluster: str = Field(default="default")
    # The signal payload itself — a free-form dict because every source looks
    # different. Schema Registry validates the *envelope*; consumers validate
    # the payload themselves based on `source`.
    payload: dict[str, Any] = Field(default_factory=dict)
    # Provenance — used for trace-anchoring and code-churn decay.
    trace_id: Optional[str] = Field(default=None, description="OpenTelemetry trace ID if available")
    span_id: Optional[str] = None
    git_commit: Optional[str] = Field(default=None, description="Commit hash active at signal time")
    # Schema-evolution guard rail — Schema Registry rejects breaking changes.
    schema_version: str = Field(default="1.0.0")

    @field_validator("service")
    @classmethod
    def _non_empty(cls, v: str) -> str:
        if not v or not v.strip():
            raise ValueError("service must be non-empty")
        return v


# ---------- Layer 2: Detection ----------

class Anomaly(BaseModel):
    """A single anomalous observation flagged by the detector."""
    anomaly_id: UUID = Field(default_factory=uuid4)
    ts: datetime = Field(default_factory=_utcnow)
    signal_id: UUID
    service: str
    namespace: str
    detector: str = Field(..., description="e.g. 'isolation_forest', 'cusum', 'rules'")
    score: float = Field(..., ge=0.0, le=1.0, description="Anomaly score 0..1")
    dimensions: dict[str, float] = Field(default_factory=dict, description="Metric->value at anomaly time")
    explanation: str = Field(default="")
    # K8s annotations from the affected workload (e.g. Deployment/StatefulSet).
    # Populated by the ingestion layer's K8s collector. Used by downstream
    # agents to discover the GitHub repo, runbook URLs, team ownership, etc.
    # Standard keys:
    #   sentinel.io/repo        — "github.com/owner/repo" (used by Fix Proposer)
    #   sentinel.io/runbook     — URL to the service's runbook
    #   sentinel.io/team        — owning team name (for governance routing)
    #   sentinel.io/ignore      — "true" to opt this workload out of Sentinel
    annotations: dict[str, str] = Field(default_factory=dict)


class AnomalyCluster(BaseModel):
    """A group of anomalies the detector has decided belong together.

    This is the unit of work the reasoning engine receives.
    """
    cluster_id: UUID = Field(default_factory=uuid4)
    ts: datetime = Field(default_factory=_utcnow)
    service: str
    namespace: str
    anomalies: list[Anomaly]
    summary: str = Field(default="", description="Human-readable summary of the cluster")
    # Trace-anchoring: if any signal in the cluster carried a trace_id, the
    # reasoning engine uses it to anchor the investigation and skip thousands
    # of downstream 504s.
    trace_id: Optional[str] = None
    severity: Severity = Severity.WARNING
    # Aggregated annotations from all member anomalies (last-write-wins).
    # Convenience field so downstream agents don't have to iterate.
    annotations: dict[str, str] = Field(default_factory=dict)


# ---------- Layer 3: Reasoning ----------

class Hypothesis(BaseModel):
    """One candidate root cause proposed by the Investigator agent."""
    h_id: str = Field(..., description="Stable ID like 'H1', 'H2', ...")
    statement: str = Field(..., description="Natural language hypothesis statement")
    supporting_evidence: list[str] = Field(default_factory=list)
    contradicting_evidence: list[str] = Field(default_factory=list)
    confidence: float = Field(default=0.0, ge=0.0, le=1.0)
    falsified: bool = False
    # Files the hypothesis is anchored to — used by the Fix Proposer.
    source_files: list[str] = Field(default_factory=list)


class Evidence(BaseModel):
    """A piece of evidence the Evidence Gatherer agent collected."""
    e_id: str
    source: str = Field(..., description="loki | prometheus | jaeger | k8s | git")
    query: str = Field(..., description="The query that produced this evidence")
    result: dict[str, Any]
    supports_hypothesis_id: Optional[str] = None
    contradicts_hypothesis_id: Optional[str] = None
    ts: datetime = Field(default_factory=_utcnow)


class RootCause(BaseModel):
    """The survivor hypothesis after the Analyst agent's falsification round."""
    statement: str
    confidence: float = Field(..., ge=0.0, le=1.0)
    evidence_ids: list[str] = Field(default_factory=list)
    source_files: list[str] = Field(default_factory=list)
    category: str = Field(..., description="e.g. 'oom', 'bad_deploy', 'config_drift', 'upstream_down'")
    # Used by OPA: schema-migration detection is required to hard-block rollbacks.
    has_schema_migration: bool = False
    git_commit: Optional[str] = None


class FixProposal(BaseModel):
    """The Fix Proposer agent's translation of a root cause into a concrete action."""
    fix_id: UUID = Field(default_factory=uuid4)
    fix_type: FixType
    target_service: str
    target_namespace: str
    description: str
    # For code_fix: the patch + repo info
    repo: Optional[str] = Field(default=None, description="github.com/owner/repo")
    repo_annotation_key: Optional[str] = Field(default=None, description="K8s annotation key used to discover the repo")
    base_branch: Optional[str] = Field(default="main")
    branch_name: Optional[str] = None
    files_to_change: list[dict[str, str]] = Field(
        default_factory=list,
        description="List of {path, new_content} dicts",
    )
    pr_title: Optional[str] = None
    pr_body: Optional[str] = None
    # Canary image for sandbox verification (set by the execution layer
    # after the branch is pushed + CI builds the image)
    canary_image: Optional[str] = Field(default=None, description="Image tag for sandbox canary, e.g. 'karimzakzouk/sentinel-sim:sentinel-abc123'")
    # For restart/scale: K8s API params
    k8s_action: Optional[dict[str, Any]] = Field(default=None, description="e.g. {kind:'Deployment', patch:{spec:{replicas:10}}}")
    # For infrastructure: terraform
    tf_workspace: Optional[str] = None
    tf_plan: Optional[str] = None
    # Sandbox spec
    requires_sandbox: bool = Field(default=True, description="Run shadow-traffic test before applying")
    # Confidence used by governance
    proposed_confidence: float = Field(..., ge=0.0, le=1.0)


class RewindEvent(BaseModel):
    """The recursive-reasoning escape hatch.

    Any agent can raise a RewindEvent if it discovers that the current
    hypotheses are entirely on the wrong track (e.g. Evidence Gatherer finds
    a hard dependency is down). The graph wipes the hypotheses array and
    restarts the Investigator with the new context, preserving audit trail.
    """
    raised_by: str
    reason: str
    new_context: dict[str, Any]
    ts: datetime = Field(default_factory=_utcnow)


# ---------- Layer 4: Safety / Governance ----------

class GovernanceDecision(BaseModel):
    """The OPA verdict for a proposed fix."""
    verdict: GovernanceVerdict
    confidence_at_decision: float = Field(..., ge=0.0, le=1.0)
    # Reasons (machine-readable)
    rules_evaluated: list[dict[str, Any]] = Field(default_factory=list)
    # If REQUIRE_HUMAN_APPROVAL: where the human clicks
    approval_request_id: Optional[str] = None
    approval_channel: Optional[str] = Field(default=None, description="e.g. 'slack:#ops'")
    rollback_blocked_reason: Optional[str] = None
    # Whether a rollback would be unsafe due to schema migration
    schema_migration_blocks_rollback: bool = False
    decided_at: datetime = Field(default_factory=_utcnow)


# ---------- Layer 5: Execution ----------

class ExecutionResult(BaseModel):
    """The outcome of executing a FixProposal."""
    fix_id: UUID
    fix_type: FixType
    success: bool
    # For code_fix: PR URL
    pr_url: Optional[str] = None
    pr_number: Optional[int] = None
    pr_branch: Optional[str] = None
    # For k8s actions: response
    k8s_response: Optional[dict[str, Any]] = None
    # For infrastructure: plan URL / apply status
    tf_plan_url: Optional[str] = None
    tf_applied: bool = False
    # Sandbox verification result (shadow traffic test)
    sandbox_verified: bool = False
    sandbox_summary: Optional[str] = None
    # Audit
    started_at: datetime = Field(default_factory=_utcnow)
    finished_at: Optional[datetime] = None
    error: Optional[str] = None


class AgentState(BaseModel):
    """The structured JSON object passed between agents in the LangGraph state machine.

    Every agent reads from and writes to this object. Pydantic validation at
    every node boundary means a malformed agent output is caught immediately
    rather than poisoning downstream agents.
    """
    incident_id: UUID
    cluster: AnomalyCluster
    hypotheses: list[Hypothesis] = Field(default_factory=list)
    evidence: list[Evidence] = Field(default_factory=list)
    root_cause: Optional[RootCause] = None
    fix_proposal: Optional[FixProposal] = None
    governance_decision: Optional[GovernanceDecision] = None
    execution_result: Optional[ExecutionResult] = None
    # Investigation context retrieved by RAG / trace parsing
    retrieved_incidents: list[dict[str, Any]] = Field(default_factory=list)
    source_files: list[str] = Field(default_factory=list)
    fetched_code: dict[str, str] = Field(default_factory=dict, description="path -> content")
    runbooks: list[str] = Field(default_factory=list)
    # Audit
    rewind_events: list[RewindEvent] = Field(default_factory=list)
    agent_trace: list[dict[str, Any]] = Field(default_factory=list, description="One entry per agent invocation")
    iterations: int = Field(default=0, ge=0)
    max_iterations: int = Field(default=5, ge=1)
    # Final
    status: IncidentStatus = IncidentStatus.DETECTED
    error: Optional[str] = None


# ---------- Layer 6: Feedback ----------

class Incident(BaseModel):
    """The end-to-end incident record. One Incident == one full Sentinel run."""
    incident_id: UUID = Field(default_factory=uuid4)
    created_at: datetime = Field(default_factory=_utcnow)
    updated_at: datetime = Field(default_factory=_utcnow)
    cluster: AnomalyCluster
    state: AgentState
    # Final outcome
    status: IncidentStatus = IncidentStatus.DETECTED
    # Beta tracking — `feedback` service updates these after resolution
    beta_alpha: float = Field(default=1.0, description="Prior successes for this fix pattern + 1")
    beta_beta: float = Field(default=1.0, description="Prior failures for this fix pattern + 1")
    # Vector DB pointer
    weaviate_uuid: Optional[str] = None
    # Code-churn decay
    git_commit_at_resolution: Optional[str] = None
    # Postmortem markdown
    postmortem: Optional[str] = None

    def touch(self) -> None:
        self.updated_at = _utcnow()
