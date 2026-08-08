"use client";

import { create } from "zustand";
import { useEffect, useRef } from "react";
import type {
  Provider,
  AutoMergeState,
  MetricsSummary,
  RuntimeConfig,
  ApiMeta,
} from "./types";
import {
  listIncidents,
  getIncident,
  triggerInvestigation,
  pauseAutonomous,
  resumeAutonomous,
  getAutoMergeState,
  listProviders,
  getRuntimeConfig,
  updateRuntimeConfig,
  getMetricsSummary,
  getHealth,
  getApiMeta,
  subscribeToEvents,
  type Incident,
} from "./api";

export type SectionId =
  | "dashboard"
  | "incidents"
  | "live"
  | "sandbox"
  | "prs"
  | "providers"
  | "automerge"
  | "detection"
  | "chatops"
  | "metrics"
  | "settings"
  | "api";

export interface ActivityEvent {
  id: string;
  type: string;
  message: string;
  time: string;
  icon: string;
}

interface SentinelState {
  // Navigation
  activeSection: SectionId;
  setActiveSection: (s: SectionId) => void;

  // Command palette
  commandOpen: boolean;
  setCommandOpen: (b: boolean) => void;

  // Incidents
  incidents: Incident[];
  selectedIncidentId: string | null;
  drawerOpen: boolean;
  setSelectedIncident: (id: string | null) => void;
  setDrawerOpen: (b: boolean) => void;

  // Activity feed (live via SSE)
  activityFeed: ActivityEvent[];
  pushActivity: (e: ActivityEvent) => void;

  // Providers
  providers: Provider[];
  refreshProviders: () => Promise<void>;

  // Auto-merge
  autoMerge: AutoMergeState | null;
  refreshAutoMerge: () => Promise<void>;

  // Metrics
  metrics: MetricsSummary | null;
  refreshMetrics: () => Promise<void>;

  // Settings
  runtimeConfig: RuntimeConfig | null;
  refreshSettings: () => Promise<void>;
  saveSettings: (cfg: Partial<RuntimeConfig>) => Promise<void>;

  // Health
  healthy: boolean;
  version: string;
  authRequired: boolean;
  refreshHealth: () => Promise<void>;

  // API meta (endpoints + chatops commands)
  apiMeta: ApiMeta | null;
  refreshApiMeta: () => Promise<void>;

  // Loading — only true during the *initial* fetch. SSE-driven refetches
  // must not flip this back to true, otherwise the dashboard flickers
  // every time an event arrives.
  loading: boolean;
  error: string | null;
  incidentsLoaded: boolean;

  // Data fetch — `silent` skips the loading flag (used by SSE refetches).
  fetchIncidents: (opts?: { silent?: boolean }) => Promise<void>;
  fetchIncident: (id: string) => Promise<Incident | null>;

  // Admin actions
  triggerInvestigation: (service: string, namespace: string, reason?: string) => Promise<void>;
  pauseAutonomous: (durationMin: number) => Promise<void>;
  resumeAutonomous: () => Promise<void>;

  // SSE
  sseConnected: boolean;
  setSseConnected: (v: boolean) => void;
}

export const useSentinelStore = create<SentinelState>((set, get) => ({
  activeSection: "dashboard",
  setActiveSection: (s) => set({ activeSection: s }),

  commandOpen: false,
  setCommandOpen: (b) => set({ commandOpen: b }),

  incidents: [],
  selectedIncidentId: null,
  drawerOpen: false,
  setSelectedIncident: (id) => set({ selectedIncidentId: id, drawerOpen: id !== null }),
  setDrawerOpen: (b) => set({ drawerOpen: b }),

  activityFeed: [],
  pushActivity: (e) =>
    set((state) => ({
      activityFeed: [e, ...state.activityFeed].slice(0, 100),
    })),

  providers: [],
  refreshProviders: async () => {
    try {
      const providers = await listProviders();
      set({ providers });
    } catch {
      // silently fail — UI shows stale data
    }
  },

  autoMerge: null,
  refreshAutoMerge: async () => {
    try {
      const autoMerge = await getAutoMergeState();
      set({ autoMerge });
    } catch {
      // silently fail
    }
  },

  metrics: null,
  refreshMetrics: async () => {
    try {
      const metrics = await getMetricsSummary();
      set({ metrics });
    } catch {
      // silently fail
    }
  },

  runtimeConfig: null,
  refreshSettings: async () => {
    try {
      const runtimeConfig = await getRuntimeConfig();
      set({ runtimeConfig });
    } catch {
      // silently fail
    }
  },
  saveSettings: async (cfg) => {
    await updateRuntimeConfig(cfg);
    const refreshed = await getRuntimeConfig();
    set({ runtimeConfig: refreshed });
  },

  healthy: false,
  version: "",
  authRequired: false,
  refreshHealth: async () => {
    try {
      const h = await getHealth();
      set({ healthy: h.status === "ok", version: h.version ?? "", authRequired: !!h.auth_required });
    } catch {
      set({ healthy: false });
    }
  },

  apiMeta: null,
  refreshApiMeta: async () => {
    try {
      const apiMeta = await getApiMeta();
      set({ apiMeta });
    } catch {
      // silently fail — UI shows "no data"
    }
  },

  loading: false,
  error: null,
  incidentsLoaded: false,

  fetchIncidents: async (opts) => {
    const silent = opts?.silent ?? false;
    // Only flip loading=true on the *initial* fetch. SSE-driven refetches
    // pass silent:true so the dashboard doesn't flicker on every event.
    if (!silent && !get().incidentsLoaded) {
      set({ loading: true, error: null });
    }
    try {
      const incidents = await listIncidents(50);
      set({ incidents, loading: false, incidentsLoaded: true, error: null });
    } catch (err) {
      set({
        loading: false,
        error: err instanceof Error ? err.message : "Failed to fetch incidents",
      });
    }
  },

  fetchIncident: async (id) => {
    try {
      const incident = await getIncident(id);
      const current = get().incidents;
      const idx = current.findIndex((i) => i.incident_id === id);
      if (idx >= 0) {
        const updated = [...current];
        updated[idx] = incident;
        set({ incidents: updated });
      }
      return incident;
    } catch {
      return null;
    }
  },

  triggerInvestigation: async (service, namespace, reason) => {
    await triggerInvestigation(service, namespace, reason);
    // Refresh incidents after a short delay so the new one appears.
    window.setTimeout(() => get().fetchIncidents({ silent: true }), 2000);
  },

  pauseAutonomous: async (durationMin) => {
    await pauseAutonomous(durationMin);
  },

  resumeAutonomous: async () => {
    await resumeAutonomous();
  },

  sseConnected: false,
  setSseConnected: (v) => set({ sseConnected: v }),
}));

// ─── SSE normalizer ──────────────────────────────────────────────────────────
// Maps an SSE event into the shape the activity feed wants. Pure function
// so it's unit-testable without a React tree.

export function normalizeSSEEvent(raw: {
  type: string;
  data: Record<string, unknown>;
}): { icon: string; message: string; skipFeed: boolean } {
  const eventType = raw.type || "unknown";
  const data = raw.data || {};
  const asStr = (v: unknown): string =>
    typeof v === "string" ? v : v == null ? "" : String(v);
  const service = asStr(data.service);
  const phase = asStr(data.phase);
  const status = asStr(data.status);
  const message = asStr(data.message);

  if (eventType === "connected") {
    // Initial connection ack — don't add to the feed, just mark live.
    return { icon: "Activity", message: "", skipFeed: true };
  }
  if (eventType === "incident") {
    const sub = asStr(data.type) || "updated";
    return {
      icon: sub === "started" ? "AlertTriangle" : "CheckCircle2",
      message: `${service}: ${sub}`,
      skipFeed: false,
    };
  }
  if (eventType === "pipeline") {
    // Build "service — phase" + ": status" only when status is non-empty,
    // so an empty status doesn't leave a trailing colon.
    const prefix = `${service} — ${phase}`.replace(/[—\s]+$/g, "").trim();
    const msg = status ? `${prefix}: ${status}` : prefix;
    return {
      icon: "Radio",
      message: msg || `${eventType} event`,
      skipFeed: false,
    };
  }
  return {
    icon: "Activity",
    message: message || `${eventType}: ${JSON.stringify(data).slice(0, 100)}`,
    skipFeed: false,
  };
}

// ─── SSE Hook ─────────────────────────────────────────────────────────────────

export function useSSE() {
  const pushActivity = useSentinelStore((s) => s.pushActivity);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const setSseConnected = useSentinelStore((s) => s.setSseConnected);
  const esRef = useRef<EventSource | null>(null);
  // Use the bare `setTimeout` symbol so the ref type matches whatever the
  // TS lib resolves to (number in browsers, NodeJS.Timeout under @types/node).
  const reconnectRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;

    function connect() {
      if (cancelled) return;
      const es = subscribeToEvents({
        onOpen: () => setSseConnected(true),
        onEvent: (event) => {
          setSseConnected(true);
          const norm = normalizeSSEEvent(event);
          if (norm.skipFeed) return;
          pushActivity({
            id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
            type: event.type,
            message: norm.message,
            time: new Date().toISOString(),
            icon: norm.icon,
          });
          // Refresh incidents when pipeline/incident events arrive — silent
          // so the dashboard doesn't flicker on every SSE event.
          if (event.type === "incident" || event.type === "pipeline") {
            const phase = typeof event.data.phase === "string" ? event.data.phase : "";
            const status = typeof event.data.status === "string" ? event.data.status : "";
            if (event.type === "incident" || phase === "resolved" || status === "complete") {
              fetchIncidents({ silent: true });
            }
          }
        },
        onError: () => {
          setSseConnected(false);
          es.close();
          if (reconnectRef.current) clearTimeout(reconnectRef.current);
          reconnectRef.current = setTimeout(connect, 3000);
        },
      });
      esRef.current = es;
    }

    connect();

    return () => {
      cancelled = true;
      if (esRef.current) esRef.current.close();
      if (reconnectRef.current) clearTimeout(reconnectRef.current);
    };
  }, [pushActivity, fetchIncidents, setSseConnected]);
}

// ─── Auto-refresh hook ───────────────────────────────────────────────────────

export function useAutoRefresh(intervalMs = 30000) {
  const refreshProviders = useSentinelStore((s) => s.refreshProviders);
  const refreshAutoMerge = useSentinelStore((s) => s.refreshAutoMerge);
  const refreshMetrics = useSentinelStore((s) => s.refreshMetrics);
  const refreshHealth = useSentinelStore((s) => s.refreshHealth);
  const refreshApiMeta = useSentinelStore((s) => s.refreshApiMeta);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);

  useEffect(() => {
    // Initial fetch — loading flag is true only for the first incidents
    // fetch. All other refreshers are silent.
    fetchIncidents();
    refreshProviders();
    refreshAutoMerge();
    refreshMetrics();
    refreshHealth();
    refreshApiMeta();

    const interval = window.setInterval(() => {
      fetchIncidents({ silent: true });
      refreshProviders();
      refreshAutoMerge();
      refreshMetrics();
      refreshHealth();
    }, intervalMs);

    return () => window.clearInterval(interval);
  }, [intervalMs, fetchIncidents, refreshProviders, refreshAutoMerge, refreshMetrics, refreshHealth, refreshApiMeta]);
}
