// Sentinel API client — thin fetch wrapper with SSE support

import type {
  Incident,
  Provider,
  AutoMergeState,
  RuntimeConfig,
  StartupConfig,
  MetricsSummary,
} from "./types";

// Re-export types so consumers can `import { listIncidents, type Incident } from "./api"`.
export type {
  Incident,
  Provider,
  AutoMergeState,
  RuntimeConfig,
  StartupConfig,
  MetricsSummary,
};

const API_BASE = ""; // same origin (Sentinel serves the UI on the same port as the API)

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers || {}),
    },
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  return res.json() as Promise<T>;
}

// ─── Incidents ──────────────────────────────────────────────────────────────

export async function listIncidents(params?: {
  status?: string;
  service?: string;
  last?: string;
}): Promise<Incident[]> {
  const qs = new URLSearchParams();
  if (params?.status) qs.set("status", params.status);
  if (params?.service) qs.set("service", params.service);
  if (params?.last) qs.set("last", params.last);
  const q = qs.toString();
  return fetchJSON<Incident[]>(`/incidents${q ? "?" + q : ""}`);
}

export async function getIncident(id: string): Promise<Incident> {
  return fetchJSON<Incident>(`/incidents/${id}`);
}

// ─── Admin ──────────────────────────────────────────────────────────────────

export async function triggerInvestigation(
  service: string,
  namespace: string,
  token?: string,
): Promise<{ incident_id: string }> {
  return fetchJSON(`/admin/investigate`, {
    method: "POST",
    body: JSON.stringify({ service, namespace }),
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
}

export async function pauseAutonomous(durationMin: number, token?: string): Promise<void> {
  await fetchJSON(`/admin/pause?duration=${durationMin}m`, {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
}

export async function resumeAutonomous(token?: string): Promise<void> {
  await fetchJSON(`/admin/resume`, {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
}

// ─── Providers ──────────────────────────────────────────────────────────────

export async function listProviders(): Promise<Provider[]> {
  const data = await fetchJSON<{ providers: Provider[] } | Provider[]>(`/providers`);
  return Array.isArray(data) ? data : data.providers || [];
}

// ─── Auto-merge ─────────────────────────────────────────────────────────────

export async function getAutoMergeState(): Promise<AutoMergeState> {
  return fetchJSON<AutoMergeState>(`/automerge`);
}

// ─── Settings ───────────────────────────────────────────────────────────────

export async function getRuntimeConfig(): Promise<RuntimeConfig> {
  return fetchJSON<RuntimeConfig>(`/settings`);
}

export async function updateRuntimeConfig(
  cfg: Partial<RuntimeConfig>,
  token?: string,
): Promise<RuntimeConfig> {
  return fetchJSON<RuntimeConfig>(`/settings`, {
    method: "PUT",
    body: JSON.stringify(cfg),
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
}

export async function getStartupConfig(): Promise<StartupConfig> {
  return fetchJSON<StartupConfig>(`/settings?include_startup=true`);
}

// ─── Metrics ────────────────────────────────────────────────────────────────

export async function getMetricsSummary(): Promise<MetricsSummary> {
  return fetchJSON<MetricsSummary>(`/metrics-summary`);
}

// ─── SSE ────────────────────────────────────────────────────────────────────

export function subscribeToEvents(
  onEvent: (event: { type: string; data: Record<string, unknown> }) => void,
  onError?: (err: Event) => void,
): EventSource {
  const es = new EventSource(`/events`);
  es.onmessage = (msg) => {
    try {
      const parsed = JSON.parse(msg.data);
      onEvent(parsed);
    } catch {
      // ignore non-JSON keepalives
    }
  };
  es.onerror = (err) => {
    onError?.(err);
  };
  return es;
}

// ─── Health ─────────────────────────────────────────────────────────────────

export async function getHealth(): Promise<{ status: string; version: string }> {
  return fetchJSON(`/healthz`);
}
