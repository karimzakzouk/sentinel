"use client";

import { useEffect, useState } from "react";
import dynamic from "next/dynamic";
import { Sidebar } from "@/components/sentinel/sidebar";
import { CommandPalette } from "@/components/sentinel/command-palette";
import { ErrorBoundary } from "@/components/sentinel/error-boundary";
import { useSentinelStore, useSSE, useAutoRefresh, type SectionId } from "@/lib/store";
import { LoginGate } from "@/components/sentinel/login-gate";
import { Clock, Search, Wifi, WifiOff, Activity, LogOut } from "lucide-react";
import { clearToken } from "@/lib/api";

// Code splitting: every section is loaded via next/dynamic with ssr:false so
// the initial bundle stays small. recharts (~200KB) loads only when the
// Metrics or Providers section is visited; framer-motion (~50KB) loads only
// when Live Investigation / Sandbox / PRs is visited.
const DashboardSection = dynamic(() => import("@/components/sections/dashboard").then((m) => ({ default: m.DashboardSection })), { ssr: false });
const IncidentsSection = dynamic(() => import("@/components/sections/incidents").then((m) => ({ default: m.IncidentsSection })), { ssr: false });
const LiveInvestigationSection = dynamic(() => import("@/components/sections/live-investigation").then((m) => ({ default: m.LiveInvestigationSection })), { ssr: false });
const SandboxSection = dynamic(() => import("@/components/sections/sandbox").then((m) => ({ default: m.SandboxSection })), { ssr: false });
const PullRequestsSection = dynamic(() => import("@/components/sections/pull-requests").then((m) => ({ default: m.PullRequestsSection })), { ssr: false });
const ProvidersSection = dynamic(() => import("@/components/sections/providers").then((m) => ({ default: m.ProvidersSection })), { ssr: false });
const AutoMergeSection = dynamic(() => import("@/components/sections/auto-merge").then((m) => ({ default: m.AutoMergeSection })), { ssr: false });
const DetectionSection = dynamic(() => import("@/components/sections/detection").then((m) => ({ default: m.DetectionSection })), { ssr: false });
const ChatopsSection = dynamic(() => import("@/components/sections/chatops").then((m) => ({ default: m.ChatopsSection })), { ssr: false });
const MetricsSection = dynamic(() => import("@/components/sections/metrics").then((m) => ({ default: m.MetricsSection })), { ssr: false });
const SettingsSection = dynamic(() => import("@/components/sections/settings").then((m) => ({ default: m.SettingsSection })), { ssr: false });
const ApiExplorerSection = dynamic(() => import("@/components/sections/api-explorer").then((m) => ({ default: m.ApiExplorerSection })), { ssr: false });

const titles: Record<SectionId, { title: string; sub: string }> = {
  dashboard: { title: "Dashboard", sub: "Operational overview" },
  incidents: { title: "Incidents", sub: "All detected and remediated incidents" },
  live: { title: "Live Investigation", sub: "Real-time agent execution pipeline" },
  sandbox: { title: "Sandbox", sub: "Verification runs" },
  prs: { title: "Pull Requests", sub: "Sentinel-generated PRs" },
  providers: { title: "Providers", sub: "LLM pool + circuit breakers" },
  automerge: { title: "Auto-Merge", sub: "Queue, rate limits, cooldown" },
  detection: { title: "Detection", sub: "Autonomous mode + webhooks" },
  chatops: { title: "Chatops", sub: "Commands from /api/meta" },
  metrics: { title: "Metrics", sub: "Aggregate operational metrics" },
  settings: { title: "Settings", sub: "Runtime configuration" },
  api: { title: "API Explorer", sub: "Endpoints from /api/meta" },
};

function ClockBadge() {
  const [now, setNow] = useState<string>("");
  useEffect(() => {
    const tick = () => setNow(new Date().toLocaleTimeString("en-US", { hour12: false }));
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, []);
  return <span className="font-mono text-zinc-300">{now || "—"}</span>;
}

export default function Home() {
  // LoginGate wraps the entire app. SSE + auto-refresh only start AFTER
  // auth succeeds (inside AuthenticatedApp). Otherwise they'd 401 on every
  // fresh visit before the user enters a token.
  return (
    <LoginGate>
      <AuthenticatedApp />
    </LoginGate>
  );
}

function AuthenticatedApp() {
  const active = useSentinelStore((s) => s.activeSection);
  const setCommandOpen = useSentinelStore((s) => s.setCommandOpen);
  const version = useSentinelStore((s) => s.version);
  const sseConnected = useSentinelStore((s) => s.sseConnected);
  const healthy = useSentinelStore((s) => s.healthy);

  // Boot SSE + auto-refresh — only after auth succeeds. These hooks are
  // idempotent (subscribe, fetch, reconnect).
  useSSE();
  useAutoRefresh(30_000);

  // Track which sections have been visited. A section mounts on first
  // visit and stays mounted (hidden via CSS) so switching tabs preserves
  // scroll position, filters, and form drafts.
  const [visitedSections, setVisitedSections] = useState<Set<SectionId>>(new Set(["dashboard"]));
  useEffect(() => {
    setVisitedSections((prev) => {
      if (prev.has(active)) return prev;
      const next = new Set(prev);
      next.add(active);
      return next;
    });
  }, [active]);

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

            <div className="hidden items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] text-zinc-400 md:flex">
              <Activity className="h-3 w-3 text-cyan-400" />
              <span className="font-mono">v{version || "—"}</span>
            </div>

            <div
              className="flex items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] md:flex"
              title={sseConnected ? "SSE connected — receiving live events" : "SSE disconnected — reconnecting"}
            >
              {sseConnected ? <Wifi className="h-3 w-3 text-emerald-400" /> : <WifiOff className="h-3 w-3 text-rose-400" />}
              <span className={`font-mono ${sseConnected ? "text-emerald-400" : "text-rose-400"}`}>
                {sseConnected ? "live" : "offline"}
              </span>
              {!healthy && <span className="ml-1 text-[10px] text-rose-400">·api down</span>}
            </div>

            <div className="hidden items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] text-zinc-400 md:flex">
              <Clock className="h-3 w-3 text-zinc-500" />
              <ClockBadge />
            </div>

            <button
              onClick={() => {
                clearToken();
                window.location.reload();
              }}
              className="flex items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-[11px] text-zinc-500 transition-colors hover:border-zinc-700 hover:bg-zinc-900 hover:text-zinc-300"
              title="Disconnect"
            >
              <LogOut className="h-3 w-3" />
            </button>
          </div>
        </header>

        {/* Each section is wrapped in an ErrorBoundary so a render crash
            in one section does NOT kill the dashboard. Sections mount on
            first visit and stay mounted (hidden via CSS) so switching tabs
            preserves scroll position, filters, and form drafts. */}
        <main className="flex-1 overflow-y-auto scrollbar-thin">
          {visitedSections.has("dashboard") && (
            <div style={{ display: active === "dashboard" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Dashboard">
                <DashboardSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("incidents") && (
            <div style={{ display: active === "incidents" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Incidents">
                <IncidentsSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("live") && (
            <div style={{ display: active === "live" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Live Investigation">
                <LiveInvestigationSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("sandbox") && (
            <div style={{ display: active === "sandbox" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Sandbox">
                <SandboxSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("prs") && (
            <div style={{ display: active === "prs" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Pull Requests">
                <PullRequestsSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("providers") && (
            <div style={{ display: active === "providers" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Providers">
                <ProvidersSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("automerge") && (
            <div style={{ display: active === "automerge" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Auto-Merge">
                <AutoMergeSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("detection") && (
            <div style={{ display: active === "detection" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Detection">
                <DetectionSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("chatops") && (
            <div style={{ display: active === "chatops" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Chatops">
                <ChatopsSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("metrics") && (
            <div style={{ display: active === "metrics" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Metrics">
                <MetricsSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("settings") && (
            <div style={{ display: active === "settings" ? "block" : "none" }}>
              <ErrorBoundary sectionName="Settings">
                <SettingsSection />
              </ErrorBoundary>
            </div>
          )}
          {visitedSections.has("api") && (
            <div style={{ display: active === "api" ? "block" : "none" }}>
              <ErrorBoundary sectionName="API Explorer">
                <ApiExplorerSection />
              </ErrorBoundary>
            </div>
          )}
        </main>
      </div>

      <CommandPalette />
    </div>
  );
}
