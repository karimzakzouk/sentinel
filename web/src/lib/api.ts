// Sentinel API client — thin fetch wrapper with SSE support
// All requests automatically include the API token from localStorage.

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

const API_BASE = ""; // same origin

const TOKEN_KEY = "sentinel_api_token";

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  if (typeof window === "undefined") return;
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  if (typeof window === "undefined") return;
  localStorage.removeItem(TOKEN_KEY);
}

function authHeaders(): Record<string, string> {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(init?.headers || {}),
    },
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  return res.json() as Promise<T>;
}

// ─── Health ─────────────────────────────────────────────────────────────────

export interface HealthResponse {
  status: string;
  service?: string;
  version?: string;
  auth_required?: boolean;
}

export async function getHealth(): Promise<HealthResponse> {
  // Health endpoint is always public — no auth needed
  const res = await fetch(`${API_BASE}/healthz`);
  if (!res.ok) {
    return { status: "error" };
  }
  return res.json();
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
): Promise<{ incident_id: string }> {
  return fetchJSON(`/admin/investigate`, {
    method: "POST",
    body: JSON.stringify({ service, namespace }),
  });
}

export async function pauseAutonomous(durationMin: number): Promise<void> {
  await fetchJSON(`/admin/pause?duration=${durationMin}m`, {
    method: "POST",
  });
}

export async function resumeAutonomous(): Promise<void> {
  await fetchJSON(`/admin/resume`, {
    method: "POST",
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
): Promise<RuntimeConfig> {
  return fetchJSON<RuntimeConfig>(`/settings`, {
    method: "PUT",
    body: JSON.stringify(cfg),
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
// EventSource can't set Authorization headers, so pass token via query param.

export function subscribeToEvents(
  onEvent: (event: { type: string; data: Record<string, unknown> }) => void,
  onError?: (err: Event) => void,
): EventSource {
  const token = getToken();
  const url = token ? `/events?token=${encodeURIComponent(token)}` : `/events`;
  const es = new EventSource(url);
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

// ─── Auth ───────────────────────────────────────────────────────────────────

export async function validateToken(token: string): Promise<boolean> {
  try {
    const res = await fetch(`${API_BASE}/incidents?last=1s`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    return res.ok;
  } catch {
    return false;
  }
}
