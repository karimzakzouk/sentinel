"use client";

import { create } from "zustand";
import {
  activityFeedSeed,
  incidents as seedIncidents,
  sandboxConsoleLines,
  pipelinePhases,
  type Incident,
} from "./mock-data";

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

  // Live investigation
  livePhaseIndex: number;
  livePhaseProgress: number; // 0-100 within current phase
  consoleLines: { level: number; text: string; ts: string }[];
  consoleIndex: number;
  traceExpandedNodes: Set<string>;
  toggleTraceNode: (id: string) => void;

  // Detection
  detectionPaused: boolean;
  detectionPauseRemaining: number; // seconds
  toggleDetection: () => void;

  // Activity feed
  activityFeed: ActivityEvent[];
  pushActivity: (e: Omit<ActivityEvent, "id" | "time">) => void;

  // Tick counter for re-renders
  tick: number;
  bumpTick: () => void;
}

const icons = ["Radio", "FlaskConical", "GitPullRequest", "Cpu", "AlertTriangle", "Eye", "Merge"];

function randomEventMessage(): { type: string; message: string; icon: string } {
  const pool = [
    { type: "investigation", message: `Investigator spawned for ${seedIncidents[Math.floor(Math.random() * 3)].id}`, icon: "Radio" },
    { type: "sandbox", message: `Sandbox L${1 + Math.floor(Math.random() * 5)} ${Math.random() > 0.2 ? "passed" : "failed"} for ${seedIncidents[Math.floor(Math.random() * 5)].id}`, icon: "FlaskConical" },
    { type: "pr", message: `PR #${4800 + Math.floor(Math.random() * 30)} opened by sentinel-bot`, icon: "GitPullRequest" },
    { type: "provider", message: `Provider ${Math.random() > 0.5 ? "OpenRouter" : "Nemotron"} latency p99 within SLO`, icon: "Cpu" },
    { type: "automerge", message: `PR #${4800 + Math.floor(Math.random() * 30)} all gates passed`, icon: "Merge" },
    { type: "detection", message: `Error spike detected in checkout-api logs`, icon: "Eye" },
  ];
  return pool[Math.floor(Math.random() * pool.length)];
}

let nextId = 1000;

export const useSentinel = create<SentinelState>((set, get) => ({
  activeSection: "dashboard",
  setActiveSection: (s) => set({ activeSection: s }),

  commandOpen: false,
  setCommandOpen: (b) => set({ commandOpen: b }),

  incidents: seedIncidents,
  selectedIncidentId: null,
  drawerOpen: false,
  setSelectedIncident: (id) => set({ selectedIncidentId: id, drawerOpen: id !== null }),
  setDrawerOpen: (b) => set({ drawerOpen: b }),

  livePhaseIndex: 5, // Execution
  livePhaseProgress: 62,
  consoleLines: [],
  consoleIndex: 0,
  traceExpandedNodes: new Set(["n0", "n1"]),
  toggleTraceNode: (id) =>
    set((st) => {
      const next = new Set(st.traceExpandedNodes);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return { traceExpandedNodes: next };
    }),

  detectionPaused: false,
  detectionPauseRemaining: 0,
  toggleDetection: () =>
    set((st) => ({
      detectionPaused: !st.detectionPaused,
      detectionPauseRemaining: !st.detectionPaused ? 1800 : 0,
    })),

  activityFeed: activityFeedSeed,
  pushActivity: (e) =>
    set((st) => ({
      activityFeed: [
        { ...e, id: `evt-${nextId++}`, time: new Date().toISOString() },
        ...st.activityFeed,
      ].slice(0, 50),
    })),

  tick: 0,
  bumpTick: () => set((st) => ({ tick: st.tick + 1 })),
}));

// Live simulation: starts on client only
let started = false;
export function startLiveSimulation() {
  if (started || typeof window === "undefined") return;
  started = true;

  // 1. Console lines trickle in every 1.5s
  setInterval(() => {
    const st = useSentinel.getState();
    if (st.consoleIndex >= sandboxConsoleLines.length) {
      // Reset for looping demo
      useSentinel.setState({ consoleIndex: 0, consoleLines: [] });
      return;
    }
    const line = sandboxConsoleLines[st.consoleIndex];
    useSentinel.setState({
      consoleLines: [
        ...st.consoleLines,
        { level: line.level, text: line.text, ts: new Date().toISOString() },
      ],
      consoleIndex: st.consoleIndex + 1,
    });
  }, 1500);

  // 2. Pipeline phase advances every 8-12s
  setInterval(() => {
    const st = useSentinel.getState();
    const newProgress = st.livePhaseProgress + 8 + Math.random() * 6;
    if (newProgress >= 100) {
      const nextIdx = (st.livePhaseIndex + 1) % pipelinePhases.length;
      useSentinel.setState({
        livePhaseIndex: nextIdx,
        livePhaseProgress: 0,
      });
      // Push activity event for phase advance
      const phaseName = pipelinePhases[nextIdx];
      st.pushActivity({
        type: "investigation",
        message: `Phase advanced: ${phaseName} for INC-7A3F9B2C`,
        icon: icons[Math.floor(Math.random() * icons.length)],
      });
    } else {
      useSentinel.setState({ livePhaseProgress: newProgress });
    }
  }, 9000);

  // 3. Random activity feed events every 12-20s
  setInterval(() => {
    const e = randomEventMessage();
    useSentinel.getState().pushActivity(e);
  }, 14000);

  // 4. Detection pause countdown
  setInterval(() => {
    const st = useSentinel.getState();
    if (st.detectionPaused && st.detectionPauseRemaining > 0) {
      useSentinel.setState({ detectionPauseRemaining: st.detectionPauseRemaining - 1 });
    } else if (st.detectionPaused && st.detectionPauseRemaining <= 0) {
      useSentinel.setState({ detectionPaused: false });
    }
  }, 1000);
}
