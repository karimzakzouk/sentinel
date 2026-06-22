"use client";

import useSWR from "swr";

const fetcher = (url: string) => fetch(url).then((r) => r.json());

export interface Incident {
  incident_id: string;
  created_at: string;
  updated_at: string;
  status: string;
  service: string;
  namespace: string;
  severity: string;
  cluster: {
    summary: string;
    anomalies: Array<{
      detector: string;
      score: number;
      explanation: string;
      dimensions: Record<string, number>;
    }>;
  };
  state: {
    hypotheses: Array<{
      h_id: string;
      statement: string;
      confidence: number;
      falsified: boolean;
    }>;
    evidence: Array<{
      e_id: string;
      source: string;
      query: string;
      result: Record<string, unknown>;
      supports_hypothesis_id: string | null;
      contradicts_hypothesis_id: string | null;
    }>;
    root_cause: {
      statement: string;
      confidence: number;
      category: string;
      source_files: string[];
      has_schema_migration: boolean;
    } | null;
    fix_proposal: {
      fix_type: string;
      target_service: string;
      target_namespace: string;
      description: string;
      repo: string | null;
      base_branch: string | null;
      branch_name: string | null;
      files_to_change: Array<{ path: string; new_content: string }>;
      pr_title: string | null;
      pr_body: string | null;
      k8s_action: Record<string, unknown> | null;
      tf_workspace: string | null;
      requires_sandbox: boolean;
      proposed_confidence: number;
    } | null;
    governance_decision: {
      verdict: string;
      confidence_at_decision: number;
      rules_evaluated: Array<{ rule: string; result: boolean }>;
      rollback_blocked_reason: string | null;
    } | null;
    execution_result: {
      success: boolean;
      pr_url: string | null;
      pr_number: number | null;
      pr_branch: string | null;
      sandbox_verified: boolean;
      sandbox_summary: string | null;
      error: string | null;
      k8s_response: Record<string, unknown> | null;
    } | null;
    agent_trace: Array<{ agent: string; action?: string; [k: string]: unknown }>;
    rewind_events: Array<{ raised_by: string; reason: string; [k: string]: unknown }>;
  };
  beta_alpha: number;
  beta_beta: number;
  postmortem: string | null;
}

export function useIncidents() {
  return useSWR<Incident[]>("/api/proxy/incidents", fetcher, {
    refreshInterval: 5000,
  });
}

export function useIncident(id: string) {
  return useSWR<Incident>(`/api/proxy/incidents/${id}`, fetcher, {
    refreshInterval: 3000,
  });
}

export function useMetrics() {
  return useSWR<{ active: number; resolved_24h: number; failed: number; total: number }>(
    "/api/proxy/metrics-summary",
    fetcher,
    { refreshInterval: 5000 },
  );
}
