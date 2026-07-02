"use client";

import { create } from "zustand";
import { useEffect, useRef } from "react";
import type {
  Provider,
  AutoMergeState,
  MetricsSummary,
  RuntimeConfig,
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
  updateSettings: (cfg: Partial<RuntimeConfig>) => Promise<void>;

  // Health
  healthy: boolean;
  version: string;
  refreshHealth: () => Promise<void>;

  // Loading
  loading: boolean;
  error: string | null;

  // Data fetch
  fetchIncidents: () => Promise<void>;
  fetchIncident: (id: string) => Promise<Incident | null>;

  // Admin actions
  triggerInvestigation: (service: string, namespace: string) => Promise<void>;
  pauseAutonomous: (durationMin: number) => Promise<void>;
  resumeAutonomous: () => Promise<void>;

  // SSE
  sseConnected: boolean;
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
    } catch (err) {
      // silently fail — UI shows stale data
    }
  },

  autoMerge: null,
  refreshAutoMerge: async () => {
    try {
      const autoMerge = await getAutoMergeState();
      set({ autoMerge });
    } catch (err) {
      // silently fail
    }
  },

  metrics: null,
  refreshMetrics: async () => {
    try {
      const metrics = await getMetricsSummary();
      set({ metrics });
    } catch (err) {
      // silently fail
    }
  },

  runtimeConfig: null,
  refreshSettings: async () => {
    try {
      const runtimeConfig = await getRuntimeConfig();
      set({ runtimeConfig });
    } catch (err) {
      // silently fail
    }
  },
  updateSettings: async (cfg) => {
    try {
      const updated = await updateRuntimeConfig(cfg);
      set({ runtimeConfig: updated });
    } catch (err) {
      throw err;
    }
  },

  healthy: false,
  version: "",
  refreshHealth: async () => {
    try {
      const h = await getHealth();
      set({ healthy: h.status === "ok", version: h.version });
    } catch {
      set({ healthy: false });
    }
  },

  loading: false,
  error: null,

  fetchIncidents: async () => {
    set({ loading: true, error: null });
    try {
      const incidents = await listIncidents({ last: "7d" });
      set({ incidents, loading: false });
    } catch (err) {
      set({ loading: false, error: err instanceof Error ? err.message : "Failed to fetch incidents" });
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

  triggerInvestigation: async (service, namespace) => {
    await triggerInvestigation(service, namespace);
    // Refresh incidents after a short delay
    setTimeout(() => get().fetchIncidents(), 2000);
  },

  pauseAutonomous: async (durationMin) => {
    await pauseAutonomous(durationMin);
  },

  resumeAutonomous: async () => {
    await resumeAutonomous();
  },

  sseConnected: false,
}));

// ─── SSE Hook ───────────────────────────────────────────────────────────────

export function useSSE() {
  const pushActivity = useSentinelStore((s) => s.pushActivity);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const setSSE = (v: boolean) => useSentinelStore.setState({ sseConnected: v });
  const esRef = useRef<EventSource | null>(null);
  const reconnectRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;

    function connect() {
      if (cancelled) return;
      const es = subscribeToEvents(
        (event) => {
          setSSE(true);

          // Map SSE events to activity feed entries
          const eventType = event.type || "unknown";
          const data = event.data || {};
          const asStr = (v: unknown): string =>
            typeof v === "string" ? v : v == null ? "" : String(v);
          const service = asStr(data.service);
          const phase = asStr(data.phase);
          const status = asStr(data.status);
          const message = asStr(data.message);

          let icon = "Activity";
          let feedMsg: string = message;

          if (eventType === "incident") {
            icon = data.type === "started" ? "AlertTriangle" : "CheckCircle2";
            feedMsg = `${service}: ${asStr(data.type) || "updated"}`;
          } else if (eventType === "pipeline") {
            icon = "Radio";
            feedMsg = `${service} — ${phase}: ${status}`;
          }

          pushActivity({
            id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
            type: eventType,
            message: feedMsg || `${eventType}: ${JSON.stringify(data).slice(0, 100)}`,
            time: new Date().toISOString(),
            icon,
          });

          // Refresh incidents when pipeline/incident events arrive
          if (eventType === "incident" || (eventType === "pipeline" && (phase === "resolved" || status === "complete"))) {
            fetchIncidents();
          }
        },
        (err) => {
          setSSE(false);
          // Reconnect with exponential backoff
          if (reconnectRef.current) clearTimeout(reconnectRef.current);
          reconnectRef.current = setTimeout(connect, 3000);
        },
      );
      esRef.current = es;
    }

    connect();

    return () => {
      cancelled = true;
      if (esRef.current) esRef.current.close();
      if (reconnectRef.current) clearTimeout(reconnectRef.current);
    };
  }, [pushActivity, fetchIncidents]);
}

// ─── Auto-refresh hook ──────────────────────────────────────────────────────

export function useAutoRefresh(intervalMs: number = 30000) {
  const refreshProviders = useSentinelStore((s) => s.refreshProviders);
  const refreshAutoMerge = useSentinelStore((s) => s.refreshAutoMerge);
  const refreshMetrics = useSentinelStore((s) => s.refreshMetrics);
  const refreshHealth = useSentinelStore((s) => s.refreshHealth);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);

  useEffect(() => {
    // Initial fetch
    fetchIncidents();
    refreshProviders();
    refreshAutoMerge();
    refreshMetrics();
    refreshHealth();

    const interval = setInterval(() => {
      fetchIncidents();
      refreshProviders();
      refreshAutoMerge();
      refreshMetrics();
      refreshHealth();
    }, intervalMs);

    return () => clearInterval(interval);
  }, [intervalMs, fetchIncidents, refreshProviders, refreshAutoMerge, refreshMetrics, refreshHealth]);
}
