// Sentinel API types — mirrors the Go structs in internal/schemas/types.go
// and internal/api/handlers.go. Field names use snake_case to match the
// JSON tags on the Go side.

// ─── Incident ───────────────────────────────────────────────────────────────

export interface Hypothesis {
  h_id: string;
  statement: string;
  supporting_evidence?: string[];
  contradicting_evidence?: string[];
  confidence: number;
  falsified: boolean;
  source_files?: string[];
}

export interface RootCause {
  statement: string;
  confidence: number;
  evidence_ids?: string[];
  source_files?: string[];
  category: string;
  has_schema_migration: boolean;
  git_commit?: string | null;
}

export interface SearchReplace {
  search: string;
  replace: string;
}

export interface FileChange {
  path: string;
  new_content?: string;
  changes?: SearchReplace[];
}

export interface FixProposal {
  fix_id: string;
  fix_type: string;
  target_service: string;
  target_namespace: string;
  description: string;
  repo?: string | null;
  base_branch?: string | null;
  branch_name?: string | null;
  files_to_change: FileChange[];
  pr_title?: string | null;
  pr_body?: string | null;
  requires_sandbox: boolean;
  proposed_confidence: number;
}

export interface ExecutionResult {
  fix_id: string;
  fix_type: string;
  success: boolean;
  pr_url?: string | null;
  pr_number?: number | null;
  pr_branch?: string | null;
  sandbox_verified: boolean;
  sandbox_summary?: string | null;
  started_at: string;
  finished_at?: string | null;
  error?: string | null;
  sandbox_override?: boolean;
  ci_override?: boolean;
}

export interface AgentTraceEntry {
  agent?: string;
  phase?: string;
  action?: string;
  status?: string;
  message?: string;
  duration?: string | number;
  tokens?: number;
  cost?: number;
  iteration?: number;
  [key: string]: unknown;
}

export interface Anomaly {
  anomaly_id: string;
  ts: string;
  service: string;
  namespace: string;
  detector: string;
  score: number;
  explanation: string;
}

export interface AnomalyCluster {
  cluster_id: string;
  ts: string;
  service: string;
  namespace: string;
  summary: string;
  severity: string;
  anomalies: Anomaly[];
}

export interface AgentState {
  incident_id: string;
  cluster: AnomalyCluster;
  hypotheses: Hypothesis[];
  root_cause?: RootCause | null;
  fix_proposal?: FixProposal | null;
  execution_result?: ExecutionResult | null;
  agent_trace: AgentTraceEntry[];
  iterations: number;
  max_iterations: number;
  status: string;
  error?: string | null;
}

export interface Incident {
  incident_id: string;
  created_at: string;
  updated_at: string;
  cluster: AnomalyCluster;
  state: AgentState;
  status: string;
  beta_alpha: number;
  beta_beta: number;
  // PR fields mirrored onto the top level by the API enricher.
  pr_repo?: string;
  pr_number?: number | null;
  pr_branch?: string;
  pr_url?: string;
  auto_merge_status?: string;
  sandbox_verified?: boolean;
  sandbox_summary?: string;
  fix_type?: string;
}

// ─── Provider (LLM pool) ────────────────────────────────────────────────────
// The /providers endpoint returns either:
//   { "enabled": true,  "providers": [...] }
//   { "enabled": false, "providers": [], "message": "..." }

export interface Provider {
  id: string;
  provider: string;
  priority: number;
  enabled: boolean;
  tripped: boolean;
  trippedUntil?: string;
  lastFailure?: string;
  lastFailureReason?: string;
  consecutiveFailures: number;
  totalCalls: number;
  totalSuccesses: number;
  totalFailures: number;
}

export interface ProvidersResponse {
  enabled: boolean;
  providers: Provider[];
  message?: string;
}

// ─── Auto-merge ─────────────────────────────────────────────────────────────

export interface AutoMergeState {
  enabled: boolean;
  namespaces?: string[];
  min_confidence?: number;
  require_ci_green?: boolean;
  max_per_hour?: number;
  merges_this_hour?: number;
  cooldown_active?: boolean;
  cooldown_reason?: string;
  message?: string;
}

// ─── Settings ───────────────────────────────────────────────────────────────

export interface RuntimeConfig {
  llm_provider?: string;
  llm_base_url?: string;
  llm_api_key?: string;
  llm_fast_model?: string;
  llm_frontier_model?: string;
  llm_embedding_model?: string;
  investigator_timeout_sec?: number;
  evidence_gatherer_timeout_sec?: number;
  analyst_timeout_sec?: number;
  fix_proposer_timeout_sec?: number;
  confidence_loop_back_threshold?: number;
  llm_max_retries?: number;
  llm_retry_base_delay_ms?: number;
  skip_sandbox_non_prod?: boolean;
  sandbox_enabled?: boolean;
  sandbox_required?: boolean;
  sandbox_max_retries?: number;
  prometheus_url?: string;
  loki_url?: string;
  jaeger_url?: string;
  github_app_id?: string;
  github_app_private_key?: string;
  github_app_install_id?: string;
  log_level?: string;
  [key: string]: unknown;
}

// ─── Metrics ────────────────────────────────────────────────────────────────

export interface MetricsSummary {
  active?: number;
  resolved_24h?: number;
  failed?: number;
  total?: number;
}

// ─── API meta ───────────────────────────────────────────────────────────────
// GET /api/meta — endpoints + chatops commands + version + auth_required.

export interface ChatopsCommandMeta {
  name: string;
  description: string;
  usage: string;
  example: string;
  permission: "read" | "write" | "admin" | string;
  tier: "Informational" | "Operational" | "Destructive" | string;
  cancelable: boolean;
}

export interface ApiMeta {
  version: string;
  endpoints: string[];
  commands: ChatopsCommandMeta[];
  auth_required: boolean;
}

// ─── SSE events ─────────────────────────────────────────────────────────────

export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

// ─── Health ─────────────────────────────────────────────────────────────────

export interface HealthResponse {
  status: string;
  service?: string;
  version?: string;
  auth_required?: boolean;
}
