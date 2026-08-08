// Sentinel API client — thin fetch wrapper with SSE support.
// All requests automatically include the API token from localStorage.

import type {
  Incident,
  Provider,
  ProvidersResponse,
  AutoMergeState,
  RuntimeConfig,
  MetricsSummary,
  ApiMeta,
  HealthResponse,
} from "./types";

export type {
  Incident,
  Provider,
  ProvidersResponse,
  AutoMergeState,
  RuntimeConfig,
  MetricsSummary,
  ApiMeta,
  HealthResponse,
};

const TOKEN_KEY = "sentinel_api_token";

/** SSR-safe localStorage access. Returns null when running on the server
 *  (Next.js SSR) or in a non-browser environment without localStorage. */
function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    // Some browsers throw on localStorage access in private mode / sandbox.
    return null;
  }
}

export function getToken(): string | null {
  return storage()?.getItem(TOKEN_KEY) ?? null;
}

export function setToken(token: string): void {
  storage()?.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  storage()?.removeItem(TOKEN_KEY);
}

function authHeaders(): Record<string, string> {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** Fetch wrapper that auto-injects the bearer token, intercepts 401s, and
 *  parses JSON. Throws on non-2xx responses. Returns parsed JSON (or
 *  undefined for 204 No Content). */
export async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(init?.headers || {}),
    },
  });
  // 401 interceptor: token expired mid-session → clear it + reload so the
  // login gate shows again. Without this, every fetch 401s silently and
  // the user sees stale data with no indicator.
  if (res.status === 401) {
    clearToken();
    if (typeof window !== "undefined") {
      window.location.reload();
    }
    throw new Error("Session expired — redirecting to login");
  }
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return res.json() as Promise<T>;
}

// ─── Health ────────────────────────────────────────────────────────────────

export async function getHealth(): Promise<HealthResponse> {
  try {
    const res = await fetch("/healthz");
    if (!res.ok) return { status: "error" };
    return (await res.json()) as HealthResponse;
  } catch {
    return { status: "error" };
  }
}

// ─── Incidents ──────────────────────────────────────────────────────────────

export async function listIncidents(limit = 50): Promise<Incident[]> {
  const data = await fetchJSON<Incident[] | null>(`/incidents?limit=${limit}`);
  return Array.isArray(data) ? data : [];
}

export async function getIncident(id: string): Promise<Incident> {
  return fetchJSON<Incident>(`/incidents/${encodeURIComponent(id)}`);
}

// ─── Admin ──────────────────────────────────────────────────────────────────

export interface InvestigateResponse {
  status: string;
  service: string;
  cluster_id: string;
}

export async function triggerInvestigation(
  service: string,
  namespace: string,
  reason?: string,
): Promise<InvestigateResponse> {
  return fetchJSON<InvestigateResponse>(`/admin/investigate`, {
    method: "POST",
    body: JSON.stringify({ service, namespace, reason: reason ?? "manual trigger" }),
  });
}

export async function pauseAutonomous(durationMin: number, reason?: string): Promise<void> {
  await fetchJSON(`/admin/pause`, {
    method: "POST",
    body: JSON.stringify({ duration_min: durationMin, reason: reason ?? "paused via UI" }),
  });
}

export async function resumeAutonomous(): Promise<void> {
  await fetchJSON(`/admin/resume`, { method: "POST" });
}

// ─── Providers ──────────────────────────────────────────────────────────────

export async function listProviders(): Promise<Provider[]> {
  const data = await fetchJSON<ProvidersResponse | Provider[]>(`/providers`);
  if (Array.isArray(data)) return data;
  return data.providers ?? [];
}

// ─── Auto-merge ─────────────────────────────────────────────────────────────

export async function getAutoMergeState(): Promise<AutoMergeState> {
  return fetchJSON<AutoMergeState>(`/automerge`);
}

// ─── Settings ───────────────────────────────────────────────────────────────

export async function getRuntimeConfig(): Promise<RuntimeConfig> {
  return fetchJSON<RuntimeConfig>(`/settings`);
}

export async function updateRuntimeConfig(cfg: Partial<RuntimeConfig>): Promise<{ status: string; message: string }> {
  return fetchJSON<{ status: string; message: string }>(`/settings`, {
    method: "POST",
    body: JSON.stringify(cfg),
  });
}

// ─── Metrics ────────────────────────────────────────────────────────────────

export async function getMetricsSummary(): Promise<MetricsSummary> {
  return fetchJSON<MetricsSummary>(`/metrics-summary`);
}

// ─── API meta ───────────────────────────────────────────────────────────────

export async function getApiMeta(): Promise<ApiMeta> {
  return fetchJSON<ApiMeta>(`/api/meta`);
}

// ─── SSE ────────────────────────────────────────────────────────────────────
// EventSource can't set Authorization headers, so the token (when present)
// is appended as ?token=. The Go server's authMiddleware whitelists this
// for /events only.

export interface SSEHandlers {
  onEvent: (event: { type: string; data: Record<string, unknown> }) => void;
  onError?: (err: Event) => void;
  onOpen?: () => void;
}

export function subscribeToEvents(handlers: SSEHandlers): EventSource {
  const token = getToken();
  const url = token ? `/events?token=${encodeURIComponent(token)}` : `/events`;
  const es = new EventSource(url);
  es.onopen = () => handlers.onOpen?.();
  es.onmessage = (msg) => {
    try {
      const parsed = JSON.parse(msg.data) as unknown;
      // Backend sends `{type, data}`. Normalize defensively in case the
      // shape is flat (keepalive pings are non-JSON comments and skip
      // onmessage entirely).
      if (parsed && typeof parsed === "object" && "type" in parsed) {
        const p = parsed as { type: string; data?: unknown };
        const data: Record<string, unknown> =
          p.data && typeof p.data === "object"
            ? (p.data as Record<string, unknown>)
            : {};
        handlers.onEvent({ type: p.type, data });
      } else {
        handlers.onEvent({ type: "message", data: {} });
      }
    } catch {
      // ignore non-JSON keepalives (e.g. `: ping` comments)
    }
  };
  es.onerror = (err) => handlers.onError?.(err);
  return es;
}

// ─── Auth ───────────────────────────────────────────────────────────────────

export async function validateToken(token: string): Promise<boolean> {
  try {
    const res = await fetch(`/admin/whoami`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    return res.ok;
  } catch {
    return false;
  }
}
