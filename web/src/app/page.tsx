"use client";

import { useEffect, useState } from "react";
import { Sidebar } from "@/components/sentinel/sidebar";
import { CommandPalette } from "@/components/sentinel/command-palette";
import { DashboardSection } from "@/components/sections/dashboard";
import { IncidentsSection } from "@/components/sections/incidents";
import { LiveInvestigationSection } from "@/components/sections/live-investigation";
import { SandboxSection } from "@/components/sections/sandbox";
import { PullRequestsSection } from "@/components/sections/pull-requests";
import { ProvidersSection } from "@/components/sections/providers";
import { AutoMergeSection } from "@/components/sections/auto-merge";
import { DetectionSection } from "@/components/sections/detection";
import { ChatopsSection } from "@/components/sections/chatops";
import { MetricsSection } from "@/components/sections/metrics";
import { SettingsSection } from "@/components/sections/settings";
import { ApiExplorerSection } from "@/components/sections/api-explorer";
import { useSentinelStore, useSSE, useAutoRefresh, type SectionId } from "@/lib/store";
import { Clock, Search, Wifi, WifiOff, Activity } from "lucide-react";

const titles: Record<SectionId, { title: string; sub: string }> = {
  dashboard: { title: "Dashboard", sub: "Operational overview — last 7 days" },
  incidents: { title: "Incidents", sub: "All detected and remediated incidents" },
  live: { title: "Live Investigation", sub: "Real-time agent execution pipeline" },
  sandbox: { title: "Sandbox", sub: "L1–L5 verification runs" },
  prs: { title: "Pull Requests", sub: "Sentinel-generated PRs and auto-merge gates" },
  providers: { title: "Providers", sub: "LLM pool, circuit breakers, and cost" },
  automerge: { title: "Auto-Merge", sub: "Queue, rate limits, and gate status" },
  detection: { title: "Detection", sub: "Autonomous mode, SSE, Alertmanager" },
  chatops: { title: "Chatops", sub: "Commands and audit log" },
  metrics: { title: "Metrics", sub: "Aggregate operational and cost metrics" },
  settings: { title: "Settings", sub: "Runtime + startup configuration" },
  api: { title: "API Explorer", sub: "REST endpoints with interactive try-it" },
};

function ClockBadge() {
  const [now, setNow] = useState<string>("");
  useEffect(() => {
    const tick = () =>
      setNow(new Date().toLocaleTimeString("en-US", { hour12: false }));
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, []);
  return (
    <span className="font-mono text-zinc-300">
      {now || "—"}
    </span>
  );
}

export default function Home() {
  const active = useSentinelStore((s) => s.activeSection);
  const setCommandOpen = useSentinelStore((s) => s.setCommandOpen);
  const version = useSentinelStore((s) => s.version);
  const sseConnected = useSentinelStore((s) => s.sseConnected);
  const healthy = useSentinelStore((s) => s.healthy);

  // Boot SSE + auto-refresh on mount — these are the only data sources for the
  // whole dashboard. The hooks are idempotent (subscribe, fetch, reconnect).
  useSSE();
  useAutoRefresh(30_000);

  // Cmd+K shortcut
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setCommandOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setCommandOpen]);

  const meta = titles[active] ?? titles.dashboard;

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      <Sidebar />

      <div className="flex flex-1 flex-col overflow-hidden">
        {/* Top bar */}
        <header className="flex h-14 shrink-0 items-center justify-between border-b border-zinc-800 bg-zinc-950/80 px-6 backdrop-blur">
          <div className="flex flex-col">
            <h1 className="text-sm font-semibold tracking-tight text-zinc-50">{meta.title}</h1>
            <p className="text-[11px] text-zinc-500">{meta.sub}</p>
          </div>

          <div className="flex items-center gap-3">
            <button
              onClick={() => setCommandOpen(true)}
              className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-xs text-zinc-500 transition-colors hover:border-zinc-700 hover:bg-zinc-900"
            >
              <Search className="h-3 w-3" />
              <span className="hidden sm:inline">Search or run a command</span>
              <kbd className="rounded border border-zinc-700 bg-zinc-800 px-1 font-mono text-[10px] text-zinc-400">⌘K</kbd>
            </button>

            {/* Version badge (from /healthz) */}
            <div className="hidden items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] text-zinc-400 md:flex">
              <Activity className="h-3 w-3 text-cyan-400" />
              <span className="font-mono">
                v{version || "—"}
              </span>
            </div>

            {/* SSE connection indicator */}
            <div
              className="flex items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] md:flex"
              title={sseConnected ? "SSE connected — receiving live events" : "SSE disconnected — reconnecting"}
            >
              {sseConnected ? (
                <Wifi className="h-3 w-3 text-emerald-400" />
              ) : (
                <WifiOff className="h-3 w-3 text-rose-400" />
              )}
              <span className={`font-mono ${sseConnected ? "text-emerald-400" : "text-rose-400"}`}>
                {sseConnected ? "live" : "offline"}
              </span>
              {!healthy && (
                <span className="ml-1 text-[10px] text-rose-400">·api down</span>
              )}
            </div>

            {/* Real clock */}
            <div className="hidden items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] text-zinc-400 md:flex">
              <Clock className="h-3 w-3 text-zinc-500" />
              <ClockBadge />
            </div>
          </div>
        </header>

        {/* Main content */}
        <main className="flex-1 overflow-y-auto scrollbar-thin">
          {active === "dashboard" && <DashboardSection />}
          {active === "incidents" && <IncidentsSection />}
          {active === "live" && <LiveInvestigationSection />}
          {active === "sandbox" && <SandboxSection />}
          {active === "prs" && <PullRequestsSection />}
          {active === "providers" && <ProvidersSection />}
          {active === "automerge" && <AutoMergeSection />}
          {active === "detection" && <DetectionSection />}
          {active === "chatops" && <ChatopsSection />}
          {active === "metrics" && <MetricsSection />}
          {active === "settings" && <SettingsSection />}
          {active === "api" && <ApiExplorerSection />}
        </main>
      </div>

      <CommandPalette />
    </div>
  );
}
