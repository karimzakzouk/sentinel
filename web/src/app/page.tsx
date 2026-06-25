"use client";

import { useState, useEffect, useCallback, useRef } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Input } from "@/components/ui/input";
import {
  Activity, AlertTriangle, CheckCircle2, Clock, GitPullRequest,
  Shield, Zap, Server, Cpu, TrendingUp, Pause, Play, RefreshCw,
  ExternalLink, Search, Terminal, Brain, Settings, ChevronRight,
  CircleDot, ArrowRight, Code2, Database, Gauge, Bell, X
} from "lucide-react";

// ─── Types ──────────────────────────────────────────────────────────────────

interface Incident {
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
    hypotheses: Array<{
      h_id: string; statement: string; confidence: number;
      falsified: boolean; source_files: string[];
    }>;
    root_cause: {
      statement: string; confidence: number; category: string;
      source_files: string[];
    } | null;
    fix_proposal: {
      fix_type: string; description: string; repo: string;
      branch_name: string;
      files_to_change: Array<{ path: string; new_content?: string; changes?: Array<{ search: string; replace: string }> }>;
      proposed_confidence: number;
    } | null;
    execution_result: {
      success: boolean; pr_url: string; pr_number: number;
      sandbox_verified: boolean; sandbox_summary: string;
    } | null;
    agent_trace: Array<Record<string, unknown>>;
    error?: string;
  };
}

interface LogEntry { time: string; level: string; msg: string; }

// ─── Mock Data ──────────────────────────────────────────────────────────────

const mockIncidents: Incident[] = [
  {
    incident_id: "3eb12213-d9d5-42b1-8ee3-38a5dd2bc327",
    created_at: new Date(Date.now() - 120000).toISOString(),
    updated_at: new Date(Date.now() - 60000).toISOString(),
    status: "resolved",
    beta_alpha: 2, beta_beta: 1,
    cluster: {
      service: "demo-app", namespace: "sim",
      summary: "ZeroDivisionError: division by zero",
      severity: "warning",
      anomalies: [{ detector: "auto_detection", score: 0.9,
        explanation: 'ZeroDivisionError: division by zero\n  File "/app/app/main.py", line 30\n    result = a / b' }],
    },
    state: {
      hypotheses: [
        { h_id: "H1", statement: "The /divide endpoint doesn't check if b is zero before dividing. Need to add a guard.", confidence: 0.7, falsified: false, source_files: ["app/main.py"] },
        { h_id: "H2", statement: "The value 'division by zero' is missing, misconfigured, or not provided.", confidence: 0.3, falsified: false, source_files: [] },
      ],
      root_cause: {
        statement: "The /divide endpoint crashes with ZeroDivisionError because b=0 is not checked before the division operation.",
        confidence: 0.58, category: "other",
        source_files: ["app/main.py"],
      },
      fix_proposal: {
        fix_type: "code_fix",
        description: "Add zero division guard in /divide endpoint to prevent ZeroDivisionError. Returns HTTP 400 if b=0.",
        repo: "karimzakzouk/demo-app",
        branch_name: "sentinel/demo-app-3eb12213",
        files_to_change: [{ path: "app/main.py",
          new_content: 'if b == 0:\n    raise HTTPException(status_code=400, detail="Division by zero is not allowed")\nresult = a / b' }],
        proposed_confidence: 0.58,
      },
      execution_result: {
        success: true,
        pr_url: "https://github.com/karimzakzouk/demo-app/pull/11",
        pr_number: 11, sandbox_verified: false,
        sandbox_summary: "skipped (non-production namespace — fast mode)",
      },
      agent_trace: [
        { agent: "investigator", hypothesis_count: 2, search_queries: 6, search_results: 2, fetched_files: ["app/main.py"] },
        { agent: "analyst", survivor: "H1", final_confidence: 0.58 },
        { agent: "fix_proposer", fix_type: "code_fix", files_changed: 1, confidence: 0.58 },
      ],
    },
  },
];

const mockLogs: LogEntry[] = [
  { time: "01:35:26", level: "INFO", msg: "Sentinel v3.7.0 starting — watching for incidents" },
  { time: "01:35:26", level: "INFO", msg: "[detection] found annotated pod: sim/demo-app (repo=karimzakzouk/demo-app)" },
  { time: "01:35:26", level: "INFO", msg: "[detection] log error: sim/demo-app — ZeroDivisionError" },
  { time: "01:35:28", level: "INFO", msg: "investigation started — service=demo-app namespace=sim" },
  { time: "01:35:29", level: "INFO", msg: "[investigator] cloning repo karimzakzouk/demo-app@main" },
  { time: "01:35:30", level: "INFO", msg: "[investigator] repo cloned — 7 files" },
  { time: "01:35:35", level: "INFO", msg: "[investigator] round 2 returned 5 hypotheses" },
  { time: "01:35:35", level: "INFO", msg: "[pipeline] confidence 0.95 >= 0.7 — skipping evidence gathering (fast path)" },
  { time: "01:36:00", level: "INFO", msg: "execution Phase 1: preparing fix branch" },
  { time: "01:36:02", level: "INFO", msg: "skipping sandbox for non-production namespace (fast mode)" },
  { time: "01:36:03", level: "INFO", msg: "execution Phase 4: opening PR" },
  { time: "01:36:04", level: "INFO", msg: "PR opened: https://github.com/karimzakzouk/demo-app/pull/11" },
  { time: "01:36:04", level: "INFO", msg: "investigation resolved — status=resolved confidence=58% time=1m5s" },
];

// ─── API ────────────────────────────────────────────────────────────────────

// v3.8.4: API_BASE is always same-origin in production (the Go binary serves
// the embedded Next.js static export). The old __SENTINEL_API__ window global
// was never set anywhere in the Go code and was dead code; removed.
const API_BASE = "";

// v3.8.4: fetchAPI no longer silently falls back to mock data on error.
// Previous implementation returned mockIncidents / fake metrics / fake
// settings when the API was unreachable — dangerous for an SRE tool because
// a misconfigured deployment looks like it's working (showing fake incidents)
// instead of surfacing the connectivity problem. Now: on error, throw, and
// the caller is responsible for showing an "API unreachable" state.
async function fetchAPI<T>(path: string): Promise<T> {
  const resp = await fetch(`${API_BASE}${path}`);
  if (!resp.ok) throw new Error(`API ${resp.status} on ${path}`);
  return await resp.json() as T;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

function statusColor(s: string): string {
  switch (s) {
    case "resolved": case "verified": return "text-emerald-400 bg-emerald-500/10 border-emerald-500/20";
    case "failed": case "escalated": return "text-red-400 bg-red-500/10 border-red-500/20";
    case "investigating": case "executing": return "text-cyan-400 bg-cyan-500/10 border-cyan-500/20";
    default: return "text-amber-400 bg-amber-500/10 border-amber-500/20";
  }
}

function confColor(c: number): string {
  if (c >= 0.7) return "text-emerald-400";
  if (c >= 0.4) return "text-amber-400";
  return "text-red-400";
}

// ─── Main ───────────────────────────────────────────────────────────────────

export default function Page() {
  const [view, setView] = useState("dashboard");
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [loading, setLoading] = useState(true);
  // v3.8.4: Track API errors explicitly so we can surface them instead of
  // silently showing mock data. Empty string = no error.
  const [apiError, setApiError] = useState<string>("");
  const [selectedIncident, setSelectedIncident] = useState<Incident | null>(null);
  const [showDetailPage, setShowDetailPage] = useState(false);
  const [paused, setPaused] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [logs, setLogs] = useState<LogEntry[]>(mockLogs);
  const [logFilter, setLogFilter] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const [sseConnected, setSseConnected] = useState(false);
  const [pipelineEvents, setPipelineEvents] = useState<Array<{ incident_id: string; phase: string; status: string; message: string; timestamp: string }>>([]);
  const eventSourceRef = useRef<EventSource | null>(null);

  const loadData = useCallback(async () => {
    try {
      const inc = await fetchAPI<Incident[]>("/incidents?limit=50");
      setIncidents(inc || []);
      setApiError("");
    } catch (e) {
      // v3.8.4: Surface the error instead of falling back to mock data.
      // The previous behavior was to silently show mockIncidents, which
      // made a misconfigured deployment look like it was working.
      setApiError(e instanceof Error ? e.message : String(e));
      setIncidents([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData();
    if (!autoRefresh) return;
    const interval = setInterval(loadData, 5000);
    return () => clearInterval(interval);
  }, [loadData, autoRefresh]);

  // v3.7.0: SSE connection for real-time updates
  useEffect(() => {
    if (!autoRefresh) return;
    try {
      const es = new EventSource(`${API_BASE}/events`);
      eventSourceRef.current = es;

      es.addEventListener("connected", () => setSseConnected(true));
      es.addEventListener("incident", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data);
          if (data.type === "new" || data.type === "resolved" || data.type === "started") {
            loadData(); // refresh incidents
          }
        } catch {}
      });
      // v3.9.2: Live pipeline trace — each phase broadcasts in real-time
      es.addEventListener("pipeline", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data);
          setPipelineEvents(prev => [...prev.slice(-100), data]);
          // Also refresh incidents when a phase completes
          if (data.status === "complete") {
            loadData();
          }
        } catch {}
      });
      es.addEventListener("pr_opened", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data);
          setPipelineEvents(prev => [...prev.slice(-100), {
            incident_id: "",
            phase: "pr_opened",
            status: "complete",
            message: `PR opened: ${data.pr_url}`,
            timestamp: new Date().toISOString(),
          }]);
          loadData();
        } catch {}
      });
      es.addEventListener("log", (e) => {
        try {
          const entry = JSON.parse((e as MessageEvent).data);
          setLogs(prev => [...prev.slice(-200), entry]);
        } catch {}
      });
      es.onerror = () => setSseConnected(false);

      return () => { es.close(); setSseConnected(false); };
    } catch {
      // SSE not available — fall back to polling
    }
  }, [autoRefresh, loadData]);

  const openIncident = (inc: Incident) => {
    setSelectedIncident(inc);
    setShowDetailPage(true);
  };

  const closeIncident = () => {
    setShowDetailPage(false);
    setTimeout(() => setSelectedIncident(null), 200);
  };

  const filteredLogs = logs.filter(l =>
    !logFilter || l.msg.toLowerCase().includes(logFilter.toLowerCase()) ||
    l.level.toLowerCase().includes(logFilter.toLowerCase())
  );

  const filteredIncidents = incidents.filter(inc =>
    !searchQuery ||
    inc.cluster.service.toLowerCase().includes(searchQuery.toLowerCase()) ||
    inc.cluster.summary.toLowerCase().includes(searchQuery.toLowerCase()) ||
    inc.incident_id.includes(searchQuery)
  );

  const navItems = [
    { id: "dashboard", label: "Dashboard", icon: Gauge },
    { id: "incidents", label: "Incidents", icon: AlertTriangle, badge: incidents.length },
    { id: "logs", label: "Live Logs", icon: Terminal },
    { id: "memory", label: "Memory", icon: Brain },
    { id: "settings", label: "Settings", icon: Settings },
  ];

  // If showing incident detail page, render it full-screen
  if (showDetailPage && selectedIncident) {
    return <IncidentFullPage incident={selectedIncident} onBack={closeIncident} />;
  }

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100 flex">
      {/* Sidebar */}
      <aside className="w-56 border-r border-zinc-800/80 bg-zinc-950 flex flex-col h-screen sticky top-0">
        <div className="flex items-center gap-2.5 px-4 h-14 border-b border-zinc-800/80">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-cyan-500/10 border border-cyan-500/20">
            <Shield className="h-3.5 w-3.5 text-cyan-400" />
          </div>
          <div>
            <span className="font-mono text-sm font-bold tracking-tight">SENTINEL</span>
            <span className="ml-1.5 text-[9px] text-zinc-600 font-mono">v3.7.0</span>
          </div>
        </div>

        <nav className="flex-1 p-2 space-y-0.5">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = view === item.id;
            return (
              <button key={item.id} onClick={() => setView(item.id)}
                className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-md text-xs font-medium transition-colors ${
                  active ? "bg-zinc-800/80 text-cyan-400" : "text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900/50"
                }`}>
                <Icon className="h-3.5 w-3.5" />
                {item.label}
                {item.badge ? <span className="ml-auto text-[10px] font-mono bg-zinc-800 px-1.5 py-0.5 rounded">{item.badge}</span> : null}
              </button>
            );
          })}
        </nav>

        <div className="p-3 border-t border-zinc-800/80">
          <div className="flex items-center gap-2 text-[10px]">
            <div className={`h-2 w-2 rounded-full ${paused ? "bg-amber-400" : sseConnected ? "bg-emerald-400 animate-pulse" : "bg-emerald-400"}`} />
            <span className="text-zinc-500">{paused ? "PAUSED" : sseConnected ? "LIVE" : "ACTIVE"}</span>
          </div>
        </div>
      </aside>

      {/* Main */}
      <main className="flex-1 overflow-y-auto h-screen">
        <header className="sticky top-0 z-40 bg-zinc-950/80 backdrop-blur-sm border-b border-zinc-800/80 h-14 flex items-center justify-between px-6">
          <div className="flex items-center gap-3">
            <h2 className="text-sm font-medium text-zinc-200 capitalize">{view}</h2>
            {view === "incidents" && (
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-zinc-600" />
                <Input placeholder="Search incidents..." value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  className="h-7 w-64 pl-7 text-xs bg-zinc-900 border-zinc-800 placeholder:text-zinc-600" />
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" className="h-7 text-xs text-zinc-400 hover:text-zinc-100"
              onClick={() => setAutoRefresh(!autoRefresh)}>
              <RefreshCw className={`h-3 w-3 mr-1 ${autoRefresh ? "animate-spin" : ""}`} />
              {autoRefresh ? "Live" : "Paused"}
            </Button>
            <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setPaused(!paused)}>
              {paused ? <Play className="h-3 w-3 mr-1" /> : <Pause className="h-3 w-3 mr-1" />}
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
        </header>

        <div className="p-6">
          {/* v3.8.4: API error banner — surfaces connectivity problems
              instead of silently showing empty/mock data. */}
          {apiError && (
            <div className="mb-4 rounded-lg border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-300">
              <div className="font-semibold mb-1">API unreachable</div>
              <div className="text-red-400/80 font-mono text-xs">{apiError}</div>
              <div className="mt-2 text-red-300/70">
                Sentinel could not reach its own API. Check that the pod is running
                ({`kubectl -n sentinel get pods`}), the Service exists, and your
                port-forward / ingress is healthy. Retrying every 5s.
              </div>
            </div>
          )}
          {view === "dashboard" && <DashboardView incidents={incidents} loading={loading} onSelect={openIncident} pipelineEvents={pipelineEvents} sseConnected={sseConnected} />}
          {view === "incidents" && <IncidentsView incidents={filteredIncidents} loading={loading} onSelect={openIncident} />}
          {view === "logs" && <LogsView logs={filteredLogs} filter={logFilter} setFilter={setLogFilter} />}
          {view === "memory" && <MemoryView incidents={incidents} />}
          {view === "settings" && <SettingsView paused={paused} setPaused={setPaused} />}
        </div>
      </main>
    </div>
  );
}

// ─── Dashboard View ─────────────────────────────────────────────────────────

function DashboardView({ incidents, loading, onSelect, pipelineEvents, sseConnected }: {
  incidents: Incident[]; loading: boolean; onSelect: (inc: Incident) => void;
  pipelineEvents: Array<{ incident_id: string; phase: string; status: string; message: string; timestamp: string }>;
  sseConnected: boolean;
}) {
  const resolved = incidents.filter(i => i.status === "resolved").length;
  const failed = incidents.filter(i => i.status === "failed").length;
  const prsOpened = incidents.filter(i => i.state?.execution_result?.pr_url).length;
  const sandboxPass = incidents.filter(i => i.state?.execution_result?.sandbox_verified).length;
  const avgConf = incidents.filter(i => i.state?.root_cause);
  const avgConfidence = avgConf.reduce((a, i) => a + (i.state.root_cause!.confidence || 0), 0) / Math.max(avgConf.length, 1);

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <MetricCard icon={<AlertTriangle className="h-4 w-4" />} label="Total Incidents" value={incidents.length} sublabel={`${incidents.filter(i => i.status !== "resolved" && i.status !== "failed").length} active`} color="text-cyan-400" />
        <MetricCard icon={<GitPullRequest className="h-4 w-4" />} label="PRs Opened" value={prsOpened} sublabel="autonomous" color="text-violet-400" />
        <MetricCard icon={<CheckCircle2 className="h-4 w-4" />} label="Sandbox Pass" value={`${sandboxPass}/${prsOpened}`} sublabel={prsOpened ? `${Math.round(sandboxPass / Math.max(prsOpened, 1) * 100)}% pass rate` : "no PRs yet"} color="text-emerald-400" />
        <MetricCard icon={<TrendingUp className="h-4 w-4" />} label="Avg Confidence" value={`${Math.round(avgConfidence * 100)}%`} sublabel="across all incidents" color="text-amber-400" />
      </div>

      <div className="grid lg:grid-cols-3 gap-6">
        <div className="lg:col-span-2">
          <Card className="border-zinc-800 bg-zinc-900/30">
            <CardHeader className="flex flex-row items-center justify-between pb-3">
              <CardTitle className="text-sm font-medium text-zinc-300">Recent Incidents</CardTitle>
              <Badge variant="outline" className="text-[10px] border-zinc-700 text-zinc-500 font-mono">{incidents.length} total</Badge>
            </CardHeader>
            <CardContent className="p-0">
              <IncidentsTable incidents={incidents.slice(0, 8)} onSelect={onSelect} loading={loading} compact />
            </CardContent>
          </Card>
        </div>

        <div className="space-y-4">
          <Card className="border-zinc-800 bg-zinc-900/30">
            <CardHeader className="pb-3"><CardTitle className="text-sm font-medium text-zinc-300">System Status</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <StatusRow icon={<Server className="h-3 w-3" />} label="Detection" status="active" detail="Live streaming" />
              <StatusRow icon={<Brain className="h-3 w-3" />} label="LLM Router" status="active" detail="openai/gpt-oss-120b:free" />
              <StatusRow icon={<Cpu className="h-3 w-3" />} label="Memory (HNSW)" status="active" detail={`${incidents.length} vectors`} />
              <StatusRow icon={<Shield className="h-3 w-3" />} label="Safety Policy" status="active" detail="5 rules active" />
              <StatusRow icon={<Database className="h-3 w-3" />} label="SQLite" status="active" detail="/data/sentinel.db" />
            </CardContent>
          </Card>

          <Card className="border-zinc-800 bg-zinc-900/30">
            <CardHeader className="pb-3"><CardTitle className="text-sm font-medium text-zinc-300">Resolution Rate</CardTitle></CardHeader>
            <CardContent>
              <div className="flex items-center gap-4">
                <div className="relative h-20 w-20">
                  <svg className="h-20 w-20 -rotate-90" viewBox="0 0 80 80">
                    <circle cx="40" cy="40" r="32" fill="none" stroke="#27272A" strokeWidth="6" />
                    <circle cx="40" cy="40" r="32" fill="none" stroke="#10B981" strokeWidth="6"
                      strokeDasharray={`${(resolved / Math.max(incidents.length, 1)) * 201} 201`} strokeLinecap="round" />
                  </svg>
                  <div className="absolute inset-0 flex items-center justify-center">
                    <span className="font-mono text-lg font-bold text-zinc-100">{Math.round(resolved / Math.max(incidents.length, 1) * 100)}%</span>
                  </div>
                </div>
                <div className="space-y-1 text-xs">
                  <div className="flex items-center gap-2"><div className="h-2 w-2 rounded-full bg-emerald-500" /><span className="text-zinc-400">Resolved: {resolved}</span></div>
                  <div className="flex items-center gap-2"><div className="h-2 w-2 rounded-full bg-red-500" /><span className="text-zinc-400">Failed: {failed}</span></div>
                  <div className="flex items-center gap-2"><div className="h-2 w-2 rounded-full bg-zinc-600" /><span className="text-zinc-400">Total: {incidents.length}</span></div>
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      {/* v3.9.2: Live Pipeline Activity Feed */}
      {pipelineEvents.length > 0 && (
        <Card className="border-zinc-800 bg-zinc-900/30">
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <CardTitle className="text-sm font-medium text-zinc-300 flex items-center gap-2">
              {sseConnected && <div className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse" />}
              Live Pipeline Activity
            </CardTitle>
            <Badge variant="outline" className="text-[10px] border-zinc-700 text-zinc-500 font-mono">{pipelineEvents.length} events</Badge>
          </CardHeader>
          <CardContent className="p-0">
            <div className="max-h-64 overflow-y-auto">
              {pipelineEvents.slice().reverse().map((evt, i) => {
                const phaseColors: Record<string, string> = {
                  detection: "text-cyan-400",
                  investigation: "text-violet-400",
                  safety: "text-amber-400",
                  execution: "text-blue-400",
                  resolved: "text-emerald-400",
                  pr_opened: "text-emerald-400",
                };
                const phaseIcons: Record<string, string> = {
                  detection: "🔍",
                  investigation: "🧠",
                  safety: "🛡️",
                  execution: "⚡",
                  resolved: "✅",
                  pr_opened: "🔗",
                };
                return (
                  <div key={i} className="flex items-start gap-3 px-4 py-2 border-b border-zinc-800/50 hover:bg-zinc-800/20">
                    <span className="text-xs mt-0.5">{phaseIcons[evt.phase] || "•"}</span>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className={`text-[10px] font-mono font-semibold uppercase ${phaseColors[evt.phase] || "text-zinc-400"}`}>{evt.phase}</span>
                        <span className={`text-[10px] font-mono ${evt.status === "complete" ? "text-emerald-400" : "text-amber-400 animate-pulse"}`}>{evt.status}</span>
                        <span className="text-[10px] text-zinc-600 ml-auto font-mono">{new Date(evt.timestamp).toLocaleTimeString("en-US", { hour12: false })}</span>
                      </div>
                      <p className="text-xs text-zinc-300 mt-0.5 truncate">{evt.message}</p>
                    </div>
                  </div>
                );
              })}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

// ─── Incidents View ─────────────────────────────────────────────────────────

function IncidentsView({ incidents, loading, onSelect }: {
  incidents: Incident[]; loading: boolean; onSelect: (inc: Incident) => void;
}) {
  return (
    <Card className="border-zinc-800 bg-zinc-900/30">
      <CardContent className="p-0">
        <IncidentsTable incidents={incidents} onSelect={onSelect} loading={loading} />
      </CardContent>
    </Card>
  );
}

// ─── Logs View ──────────────────────────────────────────────────────────────

function LogsView({ logs, filter, setFilter }: { logs: LogEntry[]; filter: string; setFilter: (s: string) => void; }) {
  const logEndRef = useRef<HTMLDivElement>(null);
  useEffect(() => { logEndRef.current?.scrollIntoView({ behavior: "smooth" }); }, [logs]);

  const levelColor = (level: string) => {
    switch (level) {
      case "ERROR": return "text-red-400"; case "WARN": return "text-amber-400";
      default: return "text-zinc-400";
    }
  };

  return (
    <Card className="border-zinc-800 bg-zinc-950">
      <CardHeader className="flex flex-row items-center justify-between pb-3">
        <CardTitle className="text-sm font-medium text-zinc-300 flex items-center gap-2">
          <Terminal className="h-3.5 w-3.5 text-cyan-400" /> Live Logs
        </CardTitle>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-zinc-600" />
            <Input placeholder="Filter logs..." value={filter} onChange={(e) => setFilter(e.target.value)}
              className="h-7 w-48 pl-7 text-xs bg-zinc-900 border-zinc-800 font-mono placeholder:text-zinc-600" />
          </div>
          <Badge variant="outline" className="text-[10px] border-zinc-700 text-zinc-500 font-mono">{logs.length} lines</Badge>
        </div>
      </CardHeader>
      <CardContent>
        <div className="rounded-md bg-zinc-950 border border-zinc-800/50 font-mono text-xs max-h-[60vh] overflow-y-auto">
          {logs.length === 0 ? <div className="p-8 text-center text-zinc-600">No logs matching filter.</div> : (
            logs.map((log, i) => (
              <div key={i} className="flex items-start gap-3 px-3 py-1 border-b border-zinc-900/50 hover:bg-zinc-900/30">
                <span className="text-zinc-600 shrink-0">{log.time}</span>
                <span className={`shrink-0 w-12 ${levelColor(log.level)}`}>{log.level}</span>
                <span className="text-zinc-300 break-all">{log.msg}</span>
              </div>
            ))
          )}
          <div ref={logEndRef} />
        </div>
      </CardContent>
    </Card>
  );
}

// ─── Memory View ────────────────────────────────────────────────────────────

function MemoryView({ incidents }: { incidents: Incident[] }) {
  const learned = incidents.filter(i => i.state?.root_cause && i.state?.execution_result);
  return (
    <div className="space-y-4">
      <Card className="border-zinc-800 bg-zinc-900/30">
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-zinc-300 flex items-center gap-2">
            <Brain className="h-3.5 w-3.5 text-cyan-400" /> Incident Memory (HNSW Vector Store)
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-zinc-500 mb-4">
            Sentinel learns from every incident. When a new error is detected, it queries this memory for similar past incidents.
            If a high-similarity match is found with a prior success, the known fix is applied directly.
          </p>
          {learned.length === 0 ? <p className="text-sm text-zinc-600 text-center py-8">No incidents in memory yet.</p> : (
            <div className="space-y-2">
              {learned.map((inc) => {
                const success = inc.state.execution_result!.success;
                return (
                  <div key={inc.incident_id} className="flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
                    <div className={`flex h-8 w-8 items-center justify-center rounded-full ${success ? "bg-emerald-500/10 text-emerald-400" : "bg-red-500/10 text-red-400"}`}>
                      {success ? <CheckCircle2 className="h-4 w-4" /> : <AlertTriangle className="h-4 w-4" />}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-xs text-zinc-200 truncate">{inc.state.root_cause!.statement}</p>
                      <div className="flex items-center gap-2 mt-0.5">
                        <span className="text-[10px] text-zinc-500 font-mono">{inc.cluster.service}</span>
                        <span className="text-[10px] text-zinc-600">·</span>
                        <span className="text-[10px] text-zinc-500">{inc.state.fix_proposal?.fix_type}</span>
                        <span className="text-[10px] text-zinc-600">·</span>
                        <span className={`text-[10px] ${success ? "text-emerald-400" : "text-red-400"}`}>{success ? "success" : "failure"}</span>
                      </div>
                    </div>
                    <div className="text-right">
                      <p className="text-[10px] text-zinc-600">Beta prior</p>
                      <p className="font-mono text-[10px] text-zinc-400">α={inc.beta_alpha} β={inc.beta_beta}</p>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ─── Settings View ──────────────────────────────────────────────────────────

function SettingsView({ paused, setPaused }: { paused: boolean; setPaused: (p: boolean) => void; }) {
  const [config, setConfig] = useState({
    llm_provider: "custom", llm_base_url: "https://openrouter.ai/api/v1",
    llm_api_key: "", llm_fast_model: "openai/gpt-oss-120b:free",
    llm_frontier_model: "openai/gpt-oss-120b:free", llm_embedding_model: "text-embedding-3-small",
    github_token: "", github_webhook_secret: "",
    docker_hub_username: "", docker_hub_token: "",
    slack_webhook: "", slack_signing_secret: "", slack_bot_token: "", slack_bot_user_id: "",
    prometheus_url: "", loki_url: "", jaeger_url: "",
    write_namespaces: "sim", production_namespaces: "prod,production,payments",
    confidence_autonomous: "0.92", confidence_loop_back: "0.3",
    investigator_timeout: "120", evidence_gatherer_timeout: "90",
    analyst_timeout: "15", fix_proposer_timeout: "120",
    llm_max_retries: "3", llm_retry_base_delay_ms: "500",
    skip_sandbox_non_prod: true, sandbox_probe_paths: "/,/healthz,/readyz",
    log_level: "INFO",
  });

  return (
    <div className="space-y-6 max-w-3xl">
      {/* LLM Configuration */}
      <SettingsCard title="LLM Configuration" icon={<Brain className="h-3.5 w-3.5 text-cyan-400" />}>
        <SettingField label="Provider" value={config.llm_provider} onChange={(v) => setConfig({...config, llm_provider: v})}
          options={["openai", "zai", "gemini", "ollama", "vllm", "litellm", "custom"]} />
        <SettingField label="Base URL" value={config.llm_base_url} onChange={(v) => setConfig({...config, llm_base_url: v})} mono />
        <SettingField label="API Key" value={config.llm_api_key} onChange={(v) => setConfig({...config, llm_api_key: v})} password />
        <SettingField label="Frontier Model" value={config.llm_frontier_model} onChange={(v) => setConfig({...config, llm_frontier_model: v})} mono />
        <SettingField label="Fast Model" value={config.llm_fast_model} onChange={(v) => setConfig({...config, llm_fast_model: v})} mono />
        <SettingField label="Embedding Model" value={config.llm_embedding_model} onChange={(v) => setConfig({...config, llm_embedding_model: v})} mono />
      </SettingsCard>

      {/* Pipeline Configuration */}
      <SettingsCard title="Pipeline Configuration" icon={<Zap className="h-3.5 w-3.5 text-amber-400" />}>
        <SettingField label="Investigator Timeout (s)" value={config.investigator_timeout} onChange={(v) => setConfig({...config, investigator_timeout: v})} mono />
        <SettingField label="Evidence Gatherer Timeout (s)" value={config.evidence_gatherer_timeout} onChange={(v) => setConfig({...config, evidence_gatherer_timeout: v})} mono />
        <SettingField label="Fix Proposer Timeout (s)" value={config.fix_proposer_timeout} onChange={(v) => setConfig({...config, fix_proposer_timeout: v})} mono />
        <SettingField label="Confidence Loop-Back Threshold" value={config.confidence_loop_back} onChange={(v) => setConfig({...config, confidence_loop_back: v})} mono />
        <SettingField label="Confidence Autonomous Threshold" value={config.confidence_autonomous} onChange={(v) => setConfig({...config, confidence_autonomous: v})} mono />
        <SettingField label="LLM Max Retries" value={config.llm_max_retries} onChange={(v) => setConfig({...config, llm_max_retries: v})} mono />
        <SettingField label="LLM Retry Base Delay (ms)" value={config.llm_retry_base_delay_ms} onChange={(v) => setConfig({...config, llm_retry_base_delay_ms: v})} mono />
        <div className="flex items-center justify-between border-b border-zinc-800/50 pb-2">
          <span className="text-xs text-zinc-500">Skip Sandbox (non-prod)</span>
          <button onClick={() => setConfig({...config, skip_sandbox_non_prod: !config.skip_sandbox_non_prod})}
            className={`relative h-5 w-9 rounded-full transition-colors ${config.skip_sandbox_non_prod ? "bg-cyan-500" : "bg-zinc-700"}`}>
            <span className={`absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${config.skip_sandbox_non_prod ? "translate-x-4" : "translate-x-0.5"}`} />
          </button>
        </div>
        <SettingField label="Sandbox Probe Paths" value={config.sandbox_probe_paths} onChange={(v) => setConfig({...config, sandbox_probe_paths: v})} mono />
      </SettingsCard>

      {/* GitHub */}
      <SettingsCard title="GitHub" icon={<GitPullRequest className="h-3.5 w-3.5 text-violet-400" />}>
        <SettingField label="GitHub Token" value={config.github_token} onChange={(v) => setConfig({...config, github_token: v})} password />
        <SettingField label="Webhook Secret (PR Review)" value={config.github_webhook_secret} onChange={(v) => setConfig({...config, github_webhook_secret: v})} password />
      </SettingsCard>

      {/* Docker Hub */}
      <SettingsCard title="Docker Hub" icon={<Server className="h-3.5 w-3.5 text-blue-400" />}>
        <SettingField label="Username" value={config.docker_hub_username} onChange={(v) => setConfig({...config, docker_hub_username: v})} mono />
        <SettingField label="Token" value={config.docker_hub_token} onChange={(v) => setConfig({...config, docker_hub_token: v})} password />
      </SettingsCard>

      {/* Slack */}
      <SettingsCard title="Slack Bot" icon={<Bell className="h-3.5 w-3.5 text-yellow-400" />}>
        <SettingField label="Signing Secret" value={config.slack_signing_secret} onChange={(v) => setConfig({...config, slack_signing_secret: v})} password />
        <SettingField label="Bot Token (xoxb-...)" value={config.slack_bot_token} onChange={(v) => setConfig({...config, slack_bot_token: v})} password />
        <SettingField label="Bot User ID" value={config.slack_bot_user_id} onChange={(v) => setConfig({...config, slack_bot_user_id: v})} mono />
        <SettingField label="Webhook URL" value={config.slack_webhook} onChange={(v) => setConfig({...config, slack_webhook: v})} password />
      </SettingsCard>

      {/* Observability */}
      <SettingsCard title="Observability" icon={<Gauge className="h-3.5 w-3.5 text-emerald-400" />}>
        <SettingField label="Prometheus URL" value={config.prometheus_url} onChange={(v) => setConfig({...config, prometheus_url: v})} mono placeholder="http://prometheus:9090" />
        <SettingField label="Loki URL" value={config.loki_url} onChange={(v) => setConfig({...config, loki_url: v})} mono placeholder="http://loki:3100" />
        <SettingField label="Jaeger URL" value={config.jaeger_url} onChange={(v) => setConfig({...config, jaeger_url: v})} mono placeholder="http://jaeger:16686" />
      </SettingsCard>

      {/* RBAC */}
      <SettingsCard title="RBAC & Namespaces" icon={<Shield className="h-3.5 w-3.5 text-red-400" />}>
        <SettingField label="Write Namespaces (CSV)" value={config.write_namespaces} onChange={(v) => setConfig({...config, write_namespaces: v})} mono />
        <SettingField label="Production Namespaces (CSV)" value={config.production_namespaces} onChange={(v) => setConfig({...config, production_namespaces: v})} mono />
      </SettingsCard>

      {/* Logging */}
      <SettingsCard title="Logging" icon={<Terminal className="h-3.5 w-3.5 text-zinc-400" />}>
        <SettingField label="Log Level" value={config.log_level} onChange={(v) => setConfig({...config, log_level: v})}
          options={["DEBUG", "INFO", "WARN", "ERROR"]} />
      </SettingsCard>

      {/* Autonomous Mode */}
      <SettingsCard title="Autonomous Mode" icon={<Pause className="h-3.5 w-3.5 text-amber-400" />}>
        <div className="flex items-center gap-4 pt-2">
          <Button variant={paused ? "default" : "outline"} size="sm"
            className={paused ? "bg-emerald-600 hover:bg-emerald-700" : "border-zinc-700 text-zinc-300"}
            onClick={() => setPaused(false)}>
            <Play className="h-3.5 w-3.5 mr-1.5" /> Resume
          </Button>
          <Button variant={paused ? "outline" : "default"} size="sm"
            className={!paused ? "bg-amber-600 hover:bg-amber-700" : "border-zinc-700 text-zinc-300"}
            onClick={() => setPaused(true)}>
            <Pause className="h-3.5 w-3.5 mr-1.5" /> Pause
          </Button>
          <span className="text-xs text-zinc-500">
            {paused ? "Autonomous mode is paused." : "Sentinel is actively monitoring."}
          </span>
        </div>
      </SettingsCard>

      <div className="flex justify-end gap-3 pb-6">
        <Button variant="outline" size="sm" className="border-zinc-700 text-zinc-400">Reset</Button>
        <Button size="sm" className="bg-cyan-600 hover:bg-cyan-700">Save Changes</Button>
      </div>
    </div>
  );
}

// ─── Full-Page Incident Detail ──────────────────────────────────────────────

function IncidentFullPage({ incident, onBack }: { incident: Incident; onBack: () => void }) {
  const inc = incident;
  const rc = inc.state?.root_cause;
  const fp = inc.state?.fix_proposal;
  const er = inc.state?.execution_result;
  const trace = inc.state?.agent_trace || [];
  const hypotheses = inc.state?.hypotheses || [];

  const pipelineSteps = [
    { label: "Detection", icon: Zap, done: true, detail: inc.cluster.summary, time: fmtTime(inc.created_at) },
    { label: "Investigation", icon: Search, done: hypotheses.length > 0, detail: hypotheses.length > 0 ? `${hypotheses.length} hypotheses formed` : "—", time: hypotheses.length > 0 ? fmtTime(inc.created_at) : "" },
    { label: "Analysis", icon: Cpu, done: !!rc, detail: rc ? `${Math.round(rc.confidence * 100)}% confidence` : "—", time: rc ? fmtTime(inc.updated_at) : "" },
    { label: "Fix Proposed", icon: Code2, done: !!fp, detail: fp?.fix_type || "—", time: fp ? fmtTime(inc.updated_at) : "" },
    { label: "Execution", icon: GitPullRequest, done: !!er, detail: er?.pr_url ? `PR #${er.pr_number}` : "—", time: er ? fmtTime(inc.updated_at) : "" },
  ];

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100">
      <header className="sticky top-0 z-40 bg-zinc-950/80 backdrop-blur-sm border-b border-zinc-800/80 h-14 flex items-center justify-between px-6">
        <div className="flex items-center gap-3">
          <button onClick={onBack} className="flex items-center gap-1.5 text-xs text-zinc-400 hover:text-zinc-100 transition-colors">
            <ChevronRight className="h-3.5 w-3.5 rotate-180" /> Back
          </button>
          <Separator orientation="vertical" className="h-4 bg-zinc-800" />
          <span className="font-mono text-sm font-bold">{inc.incident_id.slice(0, 8)}</span>
          <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>{inc.status}</span>
          {inc.cluster.severity === "critical" && (
            <span className="inline-flex items-center rounded-full border border-red-500/20 bg-red-500/10 px-2 py-0.5 text-[10px] font-medium text-red-400">CRITICAL</span>
          )}
        </div>
        <div className="flex items-center gap-3 text-xs text-zinc-500">
          <span className="font-medium text-zinc-300">{inc.cluster.service}</span>
          <span>·</span>
          <span className="font-mono">{inc.cluster.namespace}</span>
          <span>·</span>
          <span>{timeAgo(inc.created_at)}</span>
        </div>
      </header>

      <div className="max-w-5xl mx-auto p-6 space-y-6">
        {/* Pipeline Trace — Visual Flow */}
        <div className="space-y-3">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Pipeline Trace</h3>
          <div className="flex items-center gap-1 px-2">
            {pipelineSteps.map((step, i) => {
              const Icon = step.icon;
              return (
                <div key={i} className="flex items-center flex-1">
                  <div className="flex flex-col items-center gap-1.5">
                    <div className={`flex h-10 w-10 items-center justify-center rounded-lg border ${
                      step.done ? "bg-cyan-500/10 text-cyan-400 border-cyan-500/20" : "bg-zinc-900 text-zinc-600 border-zinc-800"
                    }`}>
                      <Icon className="h-4 w-4" />
                    </div>
                    <span className={`text-[9px] font-medium ${step.done ? "text-zinc-300" : "text-zinc-600"}`}>{step.label}</span>
                    {step.time && <span className="text-[8px] text-zinc-600 font-mono">{step.time}</span>}
                  </div>
                  {i < pipelineSteps.length - 1 && (
                    <div className={`flex-1 h-px mx-2 ${step.done ? "bg-cyan-500/30" : "bg-zinc-800"}`} />
                  )}
                </div>
              );
            })}
          </div>
          {er?.sandbox_verified !== undefined && (
            <div className="flex items-center gap-2 ml-2">
              <div className={`flex h-7 w-7 items-center justify-center rounded-lg border ${
                er.sandbox_verified ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/20" : "bg-amber-500/10 text-amber-400 border-amber-500/20"
              }`}>
                <Shield className="h-3.5 w-3.5" />
              </div>
              <span className={`text-xs ${er.sandbox_verified ? "text-emerald-400" : "text-amber-400"}`}>
                {er.sandbox_verified ? "Sandbox Verified" : "Needs Review"}
              </span>
              <span className="text-xs text-zinc-600 font-mono">{er.sandbox_summary}</span>
            </div>
          )}
        </div>

        <Separator className="bg-zinc-800" />

        {/* Two column: Root cause + Hypotheses */}
        <div className="grid lg:grid-cols-2 gap-6">
          {rc && (
            <div className="space-y-2">
              <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Root Cause</h3>
              <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4">
                <p className="text-sm text-zinc-300 leading-relaxed">{rc.statement}</p>
                <div className="flex items-center gap-4 mt-3">
                  <div className="flex items-center gap-1.5">
                    <span className="text-[10px] text-zinc-500">Confidence</span>
                    <div className="w-24 h-1.5 rounded-full bg-zinc-800 overflow-hidden">
                      <div className={`h-full ${rc.confidence >= 0.7 ? "bg-emerald-500" : rc.confidence >= 0.4 ? "bg-amber-500" : "bg-red-500"}`} style={{ width: `${rc.confidence * 100}%` }} />
                    </div>
                    <span className={`font-mono text-xs ${confColor(rc.confidence)}`}>{Math.round(rc.confidence * 100)}%</span>
                  </div>
                  <span className="text-[10px] text-zinc-500">Category: <span className="font-mono text-zinc-400">{rc.category}</span></span>
                </div>
                {rc.source_files?.length > 0 && (
                  <div className="mt-3 flex flex-wrap gap-1">
                    {rc.source_files.map((f) => (
                      <code key={f} className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-cyan-400 font-mono">{f}</code>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {hypotheses.length > 0 && (
            <div className="space-y-2">
              <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Hypotheses ({hypotheses.length})</h3>
              <div className="space-y-2">
                {hypotheses.map((h) => (
                  <div key={h.h_id} className={`rounded-lg border p-3 ${h.falsified ? "border-red-500/20 bg-red-500/5" : h.confidence >= 0.5 ? "border-cyan-500/20 bg-zinc-900/40" : "border-zinc-800 bg-zinc-900/20"}`}>
                    <div className="flex items-start gap-2">
                      <span className={`font-mono text-xs font-bold ${h.falsified ? "text-red-400" : h.confidence >= 0.5 ? "text-cyan-400" : "text-zinc-500"}`}>{h.h_id}</span>
                      <p className={`text-xs leading-relaxed flex-1 ${h.falsified ? "text-zinc-600 line-through" : "text-zinc-300"}`}>{h.statement}</p>
                      {!h.falsified && <span className={`font-mono text-xs ${confColor(h.confidence)}`}>{Math.round(h.confidence * 100)}%</span>}
                    </div>
                    {!h.falsified && (
                      <div className="mt-2 h-1 rounded-full bg-zinc-800 overflow-hidden">
                        <div className={`h-full ${h.confidence >= 0.7 ? "bg-emerald-500" : h.confidence >= 0.4 ? "bg-amber-500" : "bg-red-500"}`} style={{ width: `${h.confidence * 100}%` }} />
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* Fix Proposal with Code Diff */}
        {fp && (
          <div className="space-y-2">
            <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Fix Proposal</h3>
            <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4 space-y-3">
              <div className="flex items-center gap-2 flex-wrap">
                <Badge variant="outline" className="text-[10px] border-cyan-500/30 text-cyan-400 bg-cyan-500/5">{fp.fix_type}</Badge>
                <span className="text-[10px] text-zinc-500 font-mono">{fp.repo}</span>
                <span className="text-[10px] text-zinc-600">→</span>
                <span className="text-[10px] text-zinc-400 font-mono">{fp.branch_name}</span>
                <span className="ml-auto text-[10px] text-zinc-500">Confidence: <span className={confColor(fp.proposed_confidence)}>{Math.round(fp.proposed_confidence * 100)}%</span></span>
              </div>
              <p className="text-xs text-zinc-300 leading-relaxed">{fp.description}</p>
              {fp.files_to_change.map((file) => (
                <div key={file.path} className="rounded-md border border-zinc-800 overflow-hidden">
                  <div className="flex items-center gap-2 bg-zinc-900 px-3 py-2 border-b border-zinc-800">
                    <Code2 className="h-3 w-3 text-zinc-500" />
                    <span className="text-[10px] font-mono text-zinc-400">{file.path}</span>
                  </div>
                  <pre className="p-3 text-[10px] font-mono leading-relaxed overflow-x-auto bg-zinc-950">
                    {file.changes && file.changes.length > 0 ? (
                      file.changes.map((ch, i) => (
                        <div key={i}>
                          <code className="text-red-400 whitespace-pre-wrap">- {ch.search.split("\n").join("\n- ")}</code>
                          <br />
                          <code className="text-emerald-400 whitespace-pre-wrap">+ {ch.replace.split("\n").join("\n+ ")}</code>
                          {i < file.changes!.length - 1 && <hr className="border-zinc-800 my-2" />}
                        </div>
                      ))
                    ) : file.new_content ? (
                      <code className="text-emerald-400 whitespace-pre-wrap">+ {file.new_content.split("\n").join("\n+ ")}</code>
                    ) : (
                      <code className="text-zinc-500">No content</code>
                    )}
                  </pre>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Execution + Agent Trace */}
        <div className="grid lg:grid-cols-2 gap-6">
          {er && (
            <div className="space-y-2">
              <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Execution</h3>
              <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4 space-y-2">
                <div className="flex items-center gap-2">
                  <GitPullRequest className="h-3.5 w-3.5 text-violet-400" />
                  <a href={er.pr_url} target="_blank" rel="noopener noreferrer" className="text-xs text-violet-400 hover:text-violet-300 font-mono">PR #{er.pr_number}</a>
                  <ExternalLink className="h-2.5 w-2.5 text-violet-400/50" />
                </div>
                <div className="flex items-center gap-2">
                  {er.sandbox_verified ? <><CheckCircle2 className="h-3 w-3 text-emerald-400" /><span className="text-[10px] text-emerald-400">Sandbox Verified</span></> : <><AlertTriangle className="h-3 w-3 text-amber-400" /><span className="text-[10px] text-amber-400">Needs Review</span></>}
                  <span className="text-[10px] text-zinc-500 font-mono">{er.sandbox_summary}</span>
                </div>
              </div>
            </div>
          )}

          {trace.length > 0 && (
            <div className="space-y-2">
              <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Agent Trace</h3>
              <div className="space-y-1">
                {trace.map((t, i) => {
                  const agent = String(t.agent || "unknown");
                  const skipped = t.skipped as boolean;
                  return (
                    <div key={i} className={`flex items-center gap-2 rounded border px-2.5 py-1.5 ${skipped ? "border-zinc-800/50 bg-zinc-900/10 opacity-60" : "border-zinc-800/50 bg-zinc-900/20"}`}>
                      <div className={`flex h-5 w-5 items-center justify-center rounded text-[9px] ${skipped ? "bg-zinc-800 text-zinc-600" : "bg-cyan-500/10 text-cyan-400"}`}>{i + 1}</div>
                      <span className="text-xs text-zinc-300 font-medium">{agent}</span>
                      {skipped && <span className="text-[10px] text-zinc-600 italic">skipped ({String(t.reason || "")})</span>}
                      <span className="text-[10px] text-zinc-600 font-mono ml-auto">
                        {Object.entries(t).filter(([k]) => k !== "agent" && k !== "skipped" && k !== "reason").map(([k, v]) => `${k}=${String(v)}`).join(" · ")}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>

        {/* Error + Beta Prior */}
        {inc.state?.error && (
          <div className="rounded-lg border border-red-500/20 bg-red-500/5 p-4">
            <div className="flex items-center gap-2"><AlertTriangle className="h-3.5 w-3.5 text-red-400" /><span className="text-xs text-red-400 font-medium">Investigation Error</span></div>
            <p className="text-xs text-zinc-400 mt-1 font-mono">{inc.state.error}</p>
          </div>
        )}
        <div className="flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/30 px-4 py-2.5">
          <Cpu className="h-3.5 w-3.5 text-zinc-500" />
          <span className="text-xs text-zinc-500">Beta Prior</span>
          <span className="font-mono text-xs text-zinc-300">α={inc.beta_alpha} β={inc.beta_beta}</span>
          <span className="text-xs text-zinc-500">(success rate: {Math.round(inc.beta_alpha / (inc.beta_alpha + inc.beta_beta) * 100)}%)</span>
        </div>
      </div>
    </div>
  );
}

// ─── Reusable Components ────────────────────────────────────────────────────

function MetricCard({ icon, label, value, sublabel, color }: {
  icon: React.ReactNode; label: string; value: string | number; sublabel: string; color: string;
}) {
  return (
    <Card className="border-zinc-800 bg-zinc-900/30">
      <CardContent className="p-4">
        <div className="flex items-center justify-between mb-2">
          <span className="text-[10px] uppercase tracking-wider text-zinc-500">{label}</span>
          <span className={color}>{icon}</span>
        </div>
        <p className="font-mono text-2xl font-bold text-zinc-100">{value}</p>
        <p className="text-[10px] text-zinc-600 mt-1">{sublabel}</p>
      </CardContent>
    </Card>
  );
}

function IncidentsTable({ incidents, onSelect, loading, compact }: {
  incidents: Incident[]; onSelect: (inc: Incident) => void; loading: boolean; compact?: boolean;
}) {
  if (loading) return <div className="flex items-center justify-center py-12"><RefreshCw className="h-4 w-4 animate-spin text-zinc-600" /><span className="ml-2 text-sm text-zinc-500">Loading...</span></div>;
  if (incidents.length === 0) return <div className="flex flex-col items-center justify-center py-12"><CheckCircle2 className="h-8 w-8 text-emerald-400/50 mb-2" /><p className="text-sm text-zinc-500">No incidents detected.</p></div>;
  return (
    <Table>
      <TableHeader><TableRow className="border-zinc-800 hover:bg-transparent">
        <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500 font-mono">ID</TableHead>
        <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Service</TableHead>
        {!compact && <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Summary</TableHead>}
        <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Status</TableHead>
        <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Conf.</TableHead>
        {!compact && <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">PR</TableHead>}
        <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Time</TableHead>
      </TableRow></TableHeader>
      <TableBody>
        {incidents.map((inc) => {
          const conf = inc.state?.root_cause?.confidence;
          const prNum = inc.state?.execution_result?.pr_number;
          const sandbox = inc.state?.execution_result?.sandbox_verified;
          return (
            <TableRow key={inc.incident_id} className="border-zinc-800/50 cursor-pointer hover:bg-zinc-800/30 transition-colors" onClick={() => onSelect(inc)}>
              <TableCell className="font-mono text-xs text-zinc-400">{inc.incident_id.slice(0, 8)}</TableCell>
              <TableCell><div className="flex flex-col"><span className="text-xs font-medium text-zinc-200">{inc.cluster.service}</span><span className="text-[10px] text-zinc-600 font-mono">{inc.cluster.namespace}</span></div></TableCell>
              {!compact && <TableCell className="text-xs text-zinc-400 max-w-xs truncate">{inc.cluster.summary}</TableCell>}
              <TableCell><span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>{inc.status}</span></TableCell>
              <TableCell>{conf !== undefined && conf !== null ? <span className={`font-mono text-xs ${confColor(conf)}`}>{Math.round(conf * 100)}%</span> : <span className="text-xs text-zinc-600">—</span>}</TableCell>
              {!compact && <TableCell>{prNum ? <div className="flex items-center gap-1"><span className="font-mono text-xs text-violet-400">#{prNum}</span>{sandbox !== undefined && (sandbox ? <CheckCircle2 className="h-2.5 w-2.5 text-emerald-400" /> : <AlertTriangle className="h-2.5 w-2.5 text-amber-400" />)}</div> : <span className="text-xs text-zinc-600">—</span>}</TableCell>}
              <TableCell className="text-xs text-zinc-500">{timeAgo(inc.created_at)}</TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function StatusRow({ icon, label, status, detail }: { icon: React.ReactNode; label: string; status: string; detail: string; }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-zinc-500">{icon}</span>
      <span className="text-xs text-zinc-400">{label}</span>
      <div className="ml-auto flex items-center gap-2">
        <span className="text-[10px] text-zinc-600 font-mono truncate max-w-[140px]">{detail}</span>
        <div className={`h-1.5 w-1.5 rounded-full ${status === "active" ? "bg-emerald-400" : "bg-zinc-600"}`} />
      </div>
    </div>
  );
}

function SettingsCard({ title, icon, children }: { title: string; icon: React.ReactNode; children: React.ReactNode; }) {
  return (
    <Card className="border-zinc-800 bg-zinc-900/30">
      <CardHeader className="pb-3"><CardTitle className="text-sm font-medium text-zinc-300 flex items-center gap-2">{icon}{title}</CardTitle></CardHeader>
      <CardContent className="space-y-3">{children}</CardContent>
    </Card>
  );
}

function SettingField({ label, value, onChange, mono, password, options, placeholder }: {
  label: string; value: string; onChange: (v: string) => void; mono?: boolean; password?: boolean; options?: string[]; placeholder?: string;
}) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-zinc-800/50 pb-2">
      <span className="text-xs text-zinc-500 shrink-0">{label}</span>
      {options ? (
        <select value={value} onChange={(e) => onChange(e.target.value)}
          className="h-7 text-xs bg-zinc-900 border border-zinc-800 rounded px-2 text-zinc-200 font-mono focus:outline-none focus:border-cyan-500/50">
          {options.map(o => <option key={o} value={o}>{o}</option>)}
        </select>
      ) : (
        <input type={password ? "password" : "text"} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder}
          className={`h-7 flex-1 max-w-xs text-xs bg-zinc-900 border border-zinc-800 rounded px-2 text-zinc-200 focus:outline-none focus:border-cyan-500/50 ${mono ? "font-mono" : ""}`} />
      )}
    </div>
  );
}
