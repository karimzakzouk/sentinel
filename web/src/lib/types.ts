// Sentinel API types — mirrors Go structs in internal/schemas/ + internal/api/

// ─── Incident ───────────────────────────────────────────────────────────────

export interface Hypothesis {
  h_id: string;
  statement: string;
  confidence: number;
  falsified: boolean;
  source_files: string[];
}

export interface RootCause {
  statement: string;
  confidence: number;
  category: string;
  source_files: string[];
}

export interface FixProposal {
  fix_id: string;
  fix_type: string;
  description: string;
  repo: string;
  branch_name: string;
  files_to_change: Array<{
    path: string;
    new_content?: string;
    changes?: Array<{ search: string; replace: string }>;
  }>;
  proposed_confidence: number;
}

export interface Incident {
  incident_id: string;
  created_at: string;
  updated_at: string;
  status: string;
  beta_alpha: number;
  beta_beta: number;
  cluster: {
    service: string;
    namespace: string;
    summary: string;
    severity: string;
    anomalies: Array<{ detector: string; score: number; explanation: string }>;
  };
  state: {
    hypotheses: Hypothesis[];
    root_cause: RootCause | null;
    fix_proposal: FixProposal | null;
    agent_trace: Array<Record<string, unknown>>;
  };
  pr_number?: number;
  pr_repo?: string;
  pr_branch?: string;
  pr_url?: string;
  auto_merge_status?: string;
  // Mirrored from state.execution_result + state.fix_proposal for convenience.
  sandbox_verified?: boolean;
  sandbox_summary?: string;
  fix_type?: string;
}

// ─── Provider ───────────────────────────────────────────────────────────────
// Matches internal/llm/pool.go ProviderStatus struct.
// API returns: { "enabled": bool, "providers": [...] } or
// { "enabled": false, "providers": [], "message": "..." }

export interface Provider {
  id: string;
  provider: string;        // provider type (openrouter, groq, etc.)
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

// ─── Auto-merge ─────────────────────────────────────────────────────────────
// Matches internal/chatops/types.go AutoMergeStatus struct.
// API returns this directly, or { "enabled": false, "message": "..." }

export interface AutoMergeState {
  enabled: boolean;
  namespaces?: string[];
  min_confidence?: number;
  require_ci_green?: boolean;
  max_per_hour?: number;
  merges_this_hour?: number;
  cooldown_active?: boolean;
  cooldown_reason?: string;
  message?: string;  // present when not configured
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
}

export interface StartupConfig {
  // LLM
  llm_provider: string;
  llm_base_url: string;
  llm_fast_model: string;
  llm_frontier_model: string;
  llm_embedding_model: string;
  llm_providers: string; // JSON string
  // GitHub
  github_token: string;
  github_api_url: string;
  git_host: string;
  default_branch: string;
  github_webhook_secret: string;
  github_app_id: string;
  github_app_install_id: string;
  // K8s
  namespace: string;
  release_name: string;
  production_namespaces: string;
  write_namespaces: string;
  skip_namespaces: string;
  high_risk_services: string;
  // Detection
  detection_cooldown_sec: number;
  detection_tail_lines: number;
  max_concurrent_investigations: number;
  max_pipeline_iterations: number;
  // Timeouts
  investigation_timeout: number;
  investigator_timeout: number;
  evidence_gatherer_timeout: number;
  analyst_timeout: number;
  fix_proposer_timeout: number;
  // Confidence
  confidence_threshold_autonomous: number;
  confidence_threshold_human_review: number;
  confidence_loop_back: number;
  // Sandbox
  sandbox_enabled: boolean;
  sandbox_required: boolean;
  sandbox_max_retries: number;
  sandbox_namespace_prefix: string;
  sandbox_probe_paths: string;
  enable_traffic_mirroring: boolean;
  skip_sandbox_non_prod: boolean;
  // Auto-merge
  auto_merge_enabled: boolean;
  auto_merge_max_per_hour: number;
  auto_merge_cooldown_min: number;
  auto_merge_min_confidence: number;
  auto_merge_require_ci: boolean;
  auto_merge_ci_timeout_sec: number;
  auto_merge_namespaces: string;
  // Auto-remediation
  auto_remediation_enabled: boolean;
  scale_fix_replicas: number;
  panic_cpu_threshold_pct: number;
  panic_connections_threshold: number;
  // Observability
  prometheus_url: string;
  loki_url: string;
  jaeger_url: string;
  log_level: string;
  // Slack
  slack_webhook: string;
  slack_bot_token: string;
  slack_bot_user_id: string;
  slack_signing_secret: string;
  // Kaniko
  kaniko_registry: string;
  kaniko_executor_image: string;
  kaniko_init_image: string;
  docker_hub_username: string;
  docker_hub_token: string;
  // Storage
  sqlite_path: string;
  // Feedback
  outcome_watch_duration_min: number;
  outcome_check_interval_sec: number;
  llm_prior_weight: number;
  // API
  api_host: string;
  api_token: string;
  allowed_github_repos: string;
}

// ─── Metrics ────────────────────────────────────────────────────────────────

export interface MetricsSummary {
  incidents_total: Record<string, number>; // status → count
  investigations_started_total: number;
  prs_opened_total: number;
  sandbox_verifications_total: Record<string, number>; // verified → count
  llm_calls_total: Record<string, number>; // "model|status" → count
  llm_total_cost_usd: Record<string, number>; // model → cost
  llm_tokens_total: Record<string, number>; // "model|direction" → count
  investigation_duration_seconds: Record<string, number[]>; // status → buckets
  sandbox_level_failures: Record<string, number>; // level → count
  auto_merge_total: Record<string, number>; // state → count
}

// ─── SSE Events ─────────────────────────────────────────────────────────────

export interface SSEEvent {
  type: string; // "incident" | "pipeline" | "provider" | "automerge"
  data: Record<string, unknown>;
  timestamp?: string;
}
