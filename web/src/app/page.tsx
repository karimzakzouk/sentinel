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
  CircleDot, ArrowRight, Code2, Database, Gauge, Bell
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
    anomalies: Array<{
      detector: string;
      score: number;
      explanation: string;
    }>;
  };
  state: {
    hypotheses: Array<{
      h_id: string;
      statement: string;
      confidence: number;
      falsified: boolean;
      source_files: string[];
    }>;
    root_cause: {
      statement: string;
      confidence: number;
      category: string;
      source_files: string[];
    } | null;
    fix_proposal: {
      fix_type: string;
      description: string;
      repo: string;
      branch_name: string;
      files_to_change: Array<{ path: string; new_content: string }>;
      proposed_confidence: number;
    } | null;
    execution_result: {
      success: boolean;
      pr_url: string;
      pr_number: number;
      sandbox_verified: boolean;
      sandbox_summary: string;
    } | null;
    agent_trace: Array<Record<string, unknown>>;
    error?: string;
  };
}

interface MetricsSummary {
  active: number;
  resolved_24h: number;
  failed: number;
  total: number;
}

interface Settings {
  llm_provider: string;
  llm_fast_model: string;
  llm_frontier_model: string;
  github_token_set: boolean;
  write_namespaces: string[];
}

interface LogEntry {
  time: string;
  level: string;
  msg: string;
}

// ─── Mock Data ──────────────────────────────────────────────────────────────

const mockIncidents: Incident[] = [
  {
    incident_id: "91cce5a5-d9d5-42b1-8ee3-38a5dd2bc327",
    created_at: new Date(Date.now() - 3600000).toISOString(),
    updated_at: new Date(Date.now() - 3000000).toISOString(),
    status: "resolved",
    beta_alpha: 2,
    beta_beta: 1,
    cluster: {
      service: "demo-app",
      namespace: "sim",
      summary: "KeyError: 'DATABASE_URLL' — CrashLoopBackOff",
      severity: "warning",
      anomalies: [{
        detector: "auto_detection",
        score: 0.9,
        explanation: 'KeyError: \'DATABASE_URLL\'\n  File "/app/app/main.py", line 11\n    DATABASE_URL = os.environ["DATABASE_URLL"]',
      }],
    },
    state: {
      hypotheses: [
        { h_id: "H1", statement: "Environment variable 'DATABASE_URLL' has a typo (extra L). Should be 'DATABASE_URL'.", confidence: 0.7, falsified: false, source_files: ["app/main.py"] },
        { h_id: "H2", statement: "The value 'DATABASE_URLL' is missing from the environment.", confidence: 0.3, falsified: false, source_files: [] },
      ],
      root_cause: {
        statement: "Environment variable 'DATABASE_URLL' has a typo (extra L). Should be 'DATABASE_URL'.",
        confidence: 0.58,
        category: "config",
        source_files: ["app/main.py"],
      },
      fix_proposal: {
        fix_type: "code_fix",
        description: "Fix typo: DATABASE_URLL → DATABASE_URL, add fallback error handling.",
        repo: "karimzakzouk/demo-app",
        branch_name: "sentinel/demo-app-91cce5a5",
        files_to_change: [{
          path: "app/main.py",
          new_content: 'DATABASE_URL = os.getenv("DATABASE_URL")\nif not DATABASE_URL:\n    raise RuntimeError("DATABASE_URL not set")',
        }],
        proposed_confidence: 0.58,
      },
      execution_result: {
        success: true,
        pr_url: "https://github.com/karimzakzouk/demo-app/pull/4",
        pr_number: 4,
        sandbox_verified: false,
        sandbox_summary: "skipped (non-production namespace — fast mode)",
      },
      agent_trace: [
        { agent: "investigator", hypothesis_count: 2, search_queries: 3, search_results: 1, duration: "12s" },
        { agent: "evidence_gatherer", skipped: true, reason: "confidence >= 0.7 (fast path)" },
        { agent: "analyst", survivor: "H1", final_confidence: 0.58, duration: "0.1s" },
        { agent: "fix_proposer", fix_type: "code_fix", files_changed: 1, duration: "18s" },
      ],
    },
  },
  {
    incident_id: "a3f2b8c1-1234-5678-9abc-def012345678",
    created_at: new Date(Date.now() - 14400000).toISOString(),
    updated_at: new Date(Date.now() - 13800000).toISOString(),
    status: "resolved",
    beta_alpha: 3,
    beta_beta: 1,
    cluster: {
      service: "payment-api",
      namespace: "sim",
      summary: "NullPointerException in PaymentProcessor",
      severity: "critical",
      anomalies: [{
        detector: "auto_detection",
        score: 0.95,
        explanation: "java.lang.NullPointerException at com.acme.PaymentProcessor.process(PaymentProcessor.java:42)",
      }],
    },
    state: {
      hypotheses: [
        { h_id: "H1", statement: "PaymentProcessor receives null Payment from upstream — missing null check.", confidence: 0.75, falsified: false, source_files: ["src/PaymentProcessor.java"] },
      ],
      root_cause: {
        statement: "Missing null check on Payment object before field access in PaymentProcessor.process().",
        confidence: 0.72,
        category: "null_pointer",
        source_files: ["src/PaymentProcessor.java"],
      },
      fix_proposal: {
        fix_type: "code_fix",
        description: "Add null check for Payment parameter, return 400 Bad Request if null.",
        repo: "acme/payment-service",
        branch_name: "sentinel/payment-api-a3f2b8c1",
        files_to_change: [{ path: "src/PaymentProcessor.java", new_content: "if (payment == null) throw new IllegalArgumentException(\"payment cannot be null\");" }],
        proposed_confidence: 0.72,
      },
      execution_result: {
        success: true,
        pr_url: "https://github.com/acme/payment-service/pull/12",
        pr_number: 12,
        sandbox_verified: true,
        sandbox_summary: "All 3 levels passed (pod ready, endpoints respond, error gone)",
      },
      agent_trace: [
        { agent: "investigator", hypothesis_count: 1, search_queries: 5, search_results: 3, duration: "22s" },
        { agent: "evidence_gatherer", evidence_count: 4, duration: "15s" },
        { agent: "analyst", survivor: "H1", final_confidence: 0.72, duration: "0.1s" },
        { agent: "fix_proposer", fix_type: "code_fix", files_changed: 1, duration: "14s" },
      ],
    },
  },
  {
    incident_id: "b7e4d9f2-9876-5432-10fe-fedcba987654",
    created_at: new Date(Date.now() - 32400000).toISOString(),
    updated_at: new Date(Date.now() - 31800000).toISOString(),
    status: "failed",
    beta_alpha: 1,
    beta_beta: 2,
    cluster: {
      service: "checkout-web",
      namespace: "staging",
      summary: "OOMKilled — container exceeded memory limit",
      severity: "critical",
      anomalies: [{
        detector: "auto_detection",
        score: 0.95,
        explanation: "OOMKilled — container exited with code 137, memory limit 256Mi exceeded",
      }],
    },
    state: {
      hypotheses: [],
      root_cause: null,
      fix_proposal: null,
      execution_result: null,
      agent_trace: [],
      error: "investigation_timeout: could not identify root cause within 10 minutes",
    },
  },
];

const mockLogs: LogEntry[] = [
  { time: "19:03:37", level: "INFO", msg: "Sentinel v3.6.0 starting — watching for incidents" },
  { time: "19:03:37", level: "INFO", msg: "[detection] found annotated pod: sim/demo-app (repo=karimzakzouk/demo-app)" },
  { time: "19:03:37", level: "INFO", msg: "[detection] CrashLoopBackOff: sim/demo-app-566f66bbd8-rqnzp" },
  { time: "19:03:40", level: "INFO", msg: "investigation started — service=demo-app namespace=sim" },
  { time: "19:03:41", level: "INFO", msg: "[investigator] cloning repo karimzakzouk/demo-app@main" },
  { time: "19:03:42", level: "INFO", msg: "[investigator] repo cloned — 7 files" },
  { time: "19:03:54", level: "INFO", msg: "[investigator] extracted queries: [KeyError DATABASE_URLL click/core.py]" },
  { time: "19:04:20", level: "INFO", msg: "[investigator] generated 2 hypotheses (lang=python, type=KeyError)" },
  { time: "19:04:20", level: "INFO", msg: "[pipeline] confidence 0.70 >= 0.7 — skipping evidence gathering (fast path)" },
  { time: "19:05:22", level: "INFO", msg: "execution Phase 1: preparing fix branch" },
  { time: "19:05:24", level: "INFO", msg: "skipping sandbox for non-production namespace (fast mode)" },
  { time: "19:05:24", level: "INFO", msg: "execution Phase 4: opening PR" },
  { time: "19:05:25", level: "INFO", msg: "PR opened: https://github.com/karimzakzouk/demo-app/pull/4" },
  { time: "19:05:25", level: "INFO", msg: "investigation resolved — status=resolved confidence=58% time=1m48s" },
];

const mockSettings: Settings = {
  llm_provider: "custom (OpenRouter)",
  llm_fast_model: "openai/gpt-oss-120b:free",
  llm_frontier_model: "openai/gpt-oss-120b:free",
  github_token_set: true,
  write_namespaces: ["sim"],
};

// ─── API ────────────────────────────────────────────────────────────────────

const API_BASE = typeof window !== "undefined" ? (window as unknown as { __SENTINEL_API__?: string }).__SENTINEL_API__ || "" : "";

async function fetchAPI<T>(path: string): Promise<T> {
  try {
    const resp = await fetch(`${API_BASE}${path}`);
    if (!resp.ok) throw new Error(`API ${resp.status}`);
    return await resp.json() as T;
  } catch {
    if (path.includes("/incidents")) return mockIncidents as unknown as T;
    if (path.includes("/metrics-summary")) return { active: 0, resolved_24h: 2, failed: 1, total: 3 } as unknown as T;
    if (path.includes("/settings")) return mockSettings as unknown as T;
    return {} as T;
  }
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

function statusColor(status: string): string {
  switch (status) {
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
  const [metrics, setMetrics] = useState<MetricsSummary>({ active: 0, resolved_24h: 0, failed: 0, total: 0 });
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loading, setLoading] = useState(true);
  const [selectedIncident, setSelectedIncident] = useState<Incident | null>(null);
  const [paused, setPaused] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [logs, setLogs] = useState<LogEntry[]>(mockLogs);
  const [logFilter, setLogFilter] = useState("");
  const [searchQuery, setSearchQuery] = useState("");

  const loadData = useCallback(async () => {
    const [inc, stats, cfg] = await Promise.all([
      fetchAPI<Incident[]>("/incidents?limit=50"),
      fetchAPI<MetricsSummary>("/metrics-summary"),
      fetchAPI<Settings>("/settings"),
    ]);
    setIncidents(inc || []);
    setMetrics(stats || { active: 0, resolved_24h: 0, failed: 0, total: 0 });
    setSettings(cfg || null);
    setLoading(false);
  }, []);

  useEffect(() => {
    loadData();
    if (!autoRefresh) return;
    const interval = setInterval(loadData, 5000);
    return () => clearInterval(interval);
  }, [loadData, autoRefresh]);

  // Simulate live log streaming
  useEffect(() => {
    if (!autoRefresh) return;
    const interval = setInterval(() => {
      // In production, this would be a WebSocket or SSE connection to Sentinel's API
      // For now, we just keep the mock logs static
    }, 3000);
    return () => clearInterval(interval);
  }, [autoRefresh]);

  const filteredLogs = logs.filter(l =>
    !logFilter ||
    l.msg.toLowerCase().includes(logFilter.toLowerCase()) ||
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
    { id: "incidents", label: "Incidents", icon: AlertTriangle, badge: metrics.total },
    { id: "logs", label: "Live Logs", icon: Terminal },
    { id: "memory", label: "Memory", icon: Brain },
    { id: "settings", label: "Settings", icon: Settings },
  ];

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
            <span className="ml-1.5 text-[9px] text-zinc-600 font-mono">v3.6.0</span>
          </div>
        </div>

        <nav className="flex-1 p-2 space-y-0.5">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = view === item.id;
            return (
              <button
                key={item.id}
                onClick={() => setView(item.id)}
                className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-md text-xs font-medium transition-colors ${
                  active ? "bg-zinc-800/80 text-cyan-400" : "text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900/50"
                }`}
              >
                <Icon className="h-3.5 w-3.5" />
                {item.label}
                {item.badge ? (
                  <span className="ml-auto text-[10px] font-mono bg-zinc-800 px-1.5 py-0.5 rounded">{item.badge}</span>
                ) : null}
              </button>
            );
          })}
        </nav>

        {/* Status indicator */}
        <div className="p-3 border-t border-zinc-800/80">
          <div className="flex items-center gap-2 text-[10px]">
            <div className={`h-2 w-2 rounded-full ${paused ? "bg-amber-400" : "bg-emerald-400 animate-pulse"}`} />
            <span className="text-zinc-500">{paused ? "PAUSED" : "ACTIVE"}</span>
            <span className="text-zinc-700">·</span>
            <span className="text-zinc-600 font-mono">{settings?.llm_frontier_model?.split("/").pop() || "—"}</span>
          </div>
        </div>
      </aside>

      {/* Main */}
      <main className="flex-1 overflow-y-auto h-screen">
        {/* Top bar */}
        <header className="sticky top-0 z-40 bg-zinc-950/80 backdrop-blur-sm border-b border-zinc-800/80 h-14 flex items-center justify-between px-6">
          <div className="flex items-center gap-3">
            <h2 className="text-sm font-medium text-zinc-200 capitalize">{view}</h2>
            {view === "incidents" && (
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-zinc-600" />
                <Input
                  placeholder="Search incidents..."
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  className="h-7 w-64 pl-7 text-xs bg-zinc-900 border-zinc-800 placeholder:text-zinc-600"
                />
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs text-zinc-400 hover:text-zinc-100"
              onClick={() => setAutoRefresh(!autoRefresh)}
            >
              <RefreshCw className={`h-3 w-3 mr-1 ${autoRefresh ? "animate-spin" : ""}`} />
              {autoRefresh ? "Live" : "Paused"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() => setPaused(!paused)}
            >
              {paused ? <Play className="h-3 w-3 mr-1" /> : <Pause className="h-3 w-3 mr-1" />}
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
        </header>

        <div className="p-6">
          {view === "dashboard" && <DashboardView incidents={incidents} metrics={metrics} settings={settings} loading={loading} onSelect={setSelectedIncident} />}
          {view === "incidents" && <IncidentsView incidents={filteredIncidents} loading={loading} onSelect={setSelectedIncident} />}
          {view === "logs" && <LogsView logs={filteredLogs} filter={logFilter} setFilter={setLogFilter} />}
          {view === "memory" && <MemoryView incidents={incidents} />}
          {view === "settings" && <SettingsView settings={settings} paused={paused} setPaused={setPaused} />}
        </div>
      </main>

      {/* Incident Detail Drawer */}
      <Sheet open={!!selectedIncident} onOpenChange={(open) => !open && setSelectedIncident(null)}>
        <SheetContent className="w-full sm:max-w-2xl bg-zinc-950 border-zinc-800 overflow-y-auto">
          {selectedIncident && <IncidentDetail incident={selectedIncident} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}

// ─── Dashboard View ─────────────────────────────────────────────────────────

function DashboardView({ incidents, metrics, settings, loading, onSelect }: {
  incidents: Incident[];
  metrics: MetricsSummary;
  settings: Settings | null;
  loading: boolean;
  onSelect: (inc: Incident) => void;
}) {
  const resolved = incidents.filter(i => i.status === "resolved").length;
  const failed = incidents.filter(i => i.status === "failed").length;
  const prsOpened = incidents.filter(i => i.state?.execution_result?.pr_url).length;
  const sandboxPass = incidents.filter(i => i.state?.execution_result?.sandbox_verified).length;
  const avgConfidence = incidents.filter(i => i.state?.root_cause).reduce((a, i) => a + (i.state.root_cause!.confidence || 0), 0) / Math.max(incidents.filter(i => i.state?.root_cause).length, 1);

  return (
    <div className="space-y-6">
      {/* Metric Cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <MetricCard icon={<AlertTriangle className="h-4 w-4" />} label="Total Incidents" value={metrics.total || incidents.length} sublabel={`${metrics.active || 0} active`} color="text-cyan-400" />
        <MetricCard icon={<GitPullRequest className="h-4 w-4" />} label="PRs Opened" value={prsOpened} sublabel="autonomous" color="text-violet-400" />
        <MetricCard icon={<CheckCircle2 className="h-4 w-4" />} label="Sandbox Pass" value={`${sandboxPass}/${prsOpened}`} sublabel={prsOpened ? `${Math.round(sandboxPass / Math.max(prsOpened, 1) * 100)}% pass rate` : "no PRs yet"} color="text-emerald-400" />
        <MetricCard icon={<TrendingUp className="h-4 w-4" />} label="Avg Confidence" value={`${Math.round(avgConfidence * 100)}%`} sublabel="across all incidents" color="text-amber-400" />
      </div>

      {/* Two column layout */}
      <div className="grid lg:grid-cols-3 gap-6">
        {/* Recent Incidents */}
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

        {/* System Status */}
        <div className="space-y-4">
          <Card className="border-zinc-800 bg-zinc-900/30">
            <CardHeader className="pb-3">
              <CardTitle className="text-sm font-medium text-zinc-300">System Status</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <StatusRow icon={<Server className="h-3 w-3" />} label="Detection" status="active" detail="Live streaming" />
              <StatusRow icon={<Brain className="h-3 w-3" />} label="LLM Router" status="active" detail={settings?.llm_frontier_model || "—"} />
              <StatusRow icon={<Cpu className="h-3 w-3" />} label="Memory (HNSW)" status="active" detail={`${incidents.length} vectors`} />
              <StatusRow icon={<Shield className="h-3 w-3" />} label="Safety Policy" status="active" detail="5 rules active" />
              <StatusRow icon={<Database className="h-3 w-3" />} label="SQLite" status="active" detail="/data/sentinel.db" />
            </CardContent>
          </Card>

          <Card className="border-zinc-800 bg-zinc-900/30">
            <CardHeader className="pb-3">
              <CardTitle className="text-sm font-medium text-zinc-300">Resolution Rate</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="flex items-center gap-4">
                <div className="relative h-20 w-20">
                  <svg className="h-20 w-20 -rotate-90" viewBox="0 0 80 80">
                    <circle cx="40" cy="40" r="32" fill="none" stroke="#27272A" strokeWidth="6" />
                    <circle cx="40" cy="40" r="32" fill="none" stroke="#10B981" strokeWidth="6"
                      strokeDasharray={`${(resolved / Math.max(incidents.length, 1)) * 201} 201`}
                      strokeLinecap="round" />
                  </svg>
                  <div className="absolute inset-0 flex items-center justify-center">
                    <span className="font-mono text-lg font-bold text-zinc-100">
                      {Math.round(resolved / Math.max(incidents.length, 1) * 100)}%
                    </span>
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
    </div>
  );
}

// ─── Incidents View ─────────────────────────────────────────────────────────

function IncidentsView({ incidents, loading, onSelect }: {
  incidents: Incident[];
  loading: boolean;
  onSelect: (inc: Incident) => void;
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

function LogsView({ logs, filter, setFilter }: {
  logs: LogEntry[];
  filter: string;
  setFilter: (s: string) => void;
}) {
  const logEndRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [logs]);

  const levelColor = (level: string) => {
    switch (level) {
      case "ERROR": return "text-red-400 bg-red-500/5";
      case "WARN": return "text-amber-400 bg-amber-500/5";
      case "INFO": return "text-zinc-400";
      default: return "text-zinc-500";
    }
  };

  return (
    <Card className="border-zinc-800 bg-zinc-950">
      <CardHeader className="flex flex-row items-center justify-between pb-3">
        <CardTitle className="text-sm font-medium text-zinc-300 flex items-center gap-2">
          <Terminal className="h-3.5 w-3.5 text-cyan-400" />
          Live Logs
        </CardTitle>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-zinc-600" />
            <Input
              placeholder="Filter logs..."
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="h-7 w-48 pl-7 text-xs bg-zinc-900 border-zinc-800 font-mono placeholder:text-zinc-600"
            />
          </div>
          <Badge variant="outline" className="text-[10px] border-zinc-700 text-zinc-500 font-mono">
            {logs.length} lines
          </Badge>
        </div>
      </CardHeader>
      <CardContent>
        <div className="rounded-md bg-zinc-950 border border-zinc-800/50 font-mono text-xs max-h-[60vh] overflow-y-auto">
          {logs.length === 0 ? (
            <div className="p-8 text-center text-zinc-600">No logs matching filter.</div>
          ) : (
            logs.map((log, i) => (
              <div key={i} className={`flex items-start gap-3 px-3 py-1 border-b border-zinc-900/50 hover:bg-zinc-900/30 ${levelColor(log.level)}`}>
                <span className="text-zinc-600 shrink-0">{log.time}</span>
                <span className={`shrink-0 w-10 ${levelColor(log.level)}`}>{log.level}</span>
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
            <Brain className="h-3.5 w-3.5 text-cyan-400" />
            Incident Memory (HNSW Vector Store)
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-zinc-500 mb-4">
            Sentinel learns from every incident. When a new error is detected, it queries this memory
            for similar past incidents. If a high-similarity match is found with a prior success,
            the known fix is applied directly — skipping the full investigation pipeline.
          </p>
          {learned.length === 0 ? (
            <p className="text-sm text-zinc-600 text-center py-8">No incidents in memory yet.</p>
          ) : (
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

function SettingsView({ settings, paused, setPaused }: {
  settings: Settings | null;
  paused: boolean;
  setPaused: (p: boolean) => void;
}) {
  return (
    <div className="space-y-6 max-w-2xl">
      <Card className="border-zinc-800 bg-zinc-900/30">
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-zinc-300">Configuration</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {settings ? (
            <>
              <ConfigRow label="LLM Provider" value={settings.llm_provider} />
              <ConfigRow label="Frontier Model" value={settings.llm_frontier_model} mono />
              <ConfigRow label="Fast Model" value={settings.llm_fast_model} mono />
              <ConfigRow label="GitHub Token" value={settings.github_token_set ? "✓ configured" : "✗ not set"} />
              <ConfigRow label="Write Namespaces" value={settings.write_namespaces.join(", ") || "none"} mono />
            </>
          ) : (
            <p className="text-sm text-zinc-500">Loading...</p>
          )}
        </CardContent>
      </Card>

      <Card className="border-zinc-800 bg-zinc-900/30">
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-zinc-300">Autonomous Mode</CardTitle>
        </CardHeader>
        <CardContent className="flex items-center gap-4">
          <Button
            variant={paused ? "default" : "outline"}
            size="sm"
            className={paused ? "bg-emerald-600 hover:bg-emerald-700" : "border-zinc-700 text-zinc-300"}
            onClick={() => setPaused(false)}
          >
            <Play className="h-3.5 w-3.5 mr-1.5" />
            Resume
          </Button>
          <Button
            variant={paused ? "outline" : "default"}
            size="sm"
            className={!paused ? "bg-amber-600 hover:bg-amber-700" : "border-zinc-700 text-zinc-300"}
            onClick={() => setPaused(true)}
          >
            <Pause className="h-3.5 w-3.5 mr-1.5" />
            Pause
          </Button>
          <span className="text-xs text-zinc-500">
            {paused ? "Autonomous mode is paused — new incidents will not be investigated." : "Sentinel is actively monitoring for incidents."}
          </span>
        </CardContent>
      </Card>

      <Card className="border-zinc-800 bg-zinc-900/30">
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-zinc-300">Pipeline Configuration</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <ConfigRow label="Investigator Timeout" value="120s" mono />
          <ConfigRow label="Evidence Gatherer Timeout" value="90s" mono />
          <ConfigRow label="Fix Proposer Timeout" value="120s" mono />
          <ConfigRow label="Confidence Loop-Back Threshold" value="0.30" mono />
          <ConfigRow label="Max Pipeline Iterations" value="5" mono />
          <ConfigRow label="Skip Sandbox (non-prod)" value="true" mono />
          <ConfigRow label="LLM Max Retries" value="3" mono />
          <ConfigRow label="LLM Retry Base Delay" value="500ms" mono />
        </CardContent>
      </Card>
    </div>
  );
}

// ─── Incident Detail ────────────────────────────────────────────────────────

function IncidentDetail({ incident }: { incident: Incident }) {
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
    <div className="space-y-5 pt-6">
      <SheetHeader>
        <div className="flex items-center gap-2">
          <SheetTitle className="font-mono text-base">{inc.incident_id.slice(0, 8)}</SheetTitle>
          <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>
            {inc.status}
          </span>
          {inc.cluster.severity === "critical" && (
            <span className="inline-flex items-center rounded-full border border-red-500/20 bg-red-500/10 px-2 py-0.5 text-[10px] font-medium text-red-400">
              CRITICAL
            </span>
          )}
        </div>
        <div className="flex items-center gap-3 text-xs text-zinc-500">
          <span className="font-medium text-zinc-300">{inc.cluster.service}</span>
          <span>·</span>
          <span className="font-mono">{inc.cluster.namespace}</span>
          <span>·</span>
          <span>{timeAgo(inc.created_at)}</span>
        </div>
      </SheetHeader>

      {/* Pipeline Trace — Visual Flow */}
      <div className="space-y-2">
        <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Pipeline Trace</h3>
        <div className="flex items-center gap-1">
          {pipelineSteps.map((step, i) => {
            const Icon = step.icon;
            return (
              <div key={i} className="flex items-center flex-1">
                <div className={`flex flex-col items-center gap-1.5 ${i > 0 ? "ml-1" : ""}`}>
                  <div className={`flex h-9 w-9 items-center justify-center rounded-lg border ${
                    step.done ? "bg-cyan-500/10 text-cyan-400 border-cyan-500/20" : "bg-zinc-900 text-zinc-600 border-zinc-800"
                  }`}>
                    <Icon className="h-4 w-4" />
                  </div>
                  <span className={`text-[9px] font-medium ${step.done ? "text-zinc-300" : "text-zinc-600"}`}>{step.label}</span>
                </div>
                {i < pipelineSteps.length - 1 && (
                  <div className={`flex-1 h-px mx-1 ${step.done ? "bg-cyan-500/30" : "bg-zinc-800"}`} />
                )}
              </div>
            );
          })}
        </div>
        {er?.sandbox_verified !== undefined && (
          <div className="flex items-center gap-2 mt-2 ml-1">
            <div className={`flex h-6 w-6 items-center justify-center rounded border ${
              er.sandbox_verified ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/20" : "bg-amber-500/10 text-amber-400 border-amber-500/20"
            }`}>
              <Shield className="h-3 w-3" />
            </div>
            <span className={`text-[10px] ${er.sandbox_verified ? "text-emerald-400" : "text-amber-400"}`}>
              {er.sandbox_verified ? "Sandbox Verified" : "Needs Review"}
            </span>
            <span className="text-[10px] text-zinc-600 font-mono">{er.sandbox_summary}</span>
          </div>
        )}
      </div>

      <Separator className="bg-zinc-800" />

      {/* Root Cause */}
      {rc && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Root Cause</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
            <p className="text-xs text-zinc-300 leading-relaxed">{rc.statement}</p>
            <div className="flex items-center gap-4 mt-3">
              <div className="flex items-center gap-1.5">
                <span className="text-[10px] text-zinc-500">Confidence</span>
                <div className="w-20 h-1.5 rounded-full bg-zinc-800 overflow-hidden">
                  <div className={`h-full ${rc.confidence >= 0.7 ? "bg-emerald-500" : rc.confidence >= 0.4 ? "bg-amber-500" : "bg-red-500"}`} style={{ width: `${rc.confidence * 100}%` }} />
                </div>
                <span className={`font-mono text-xs ${confColor(rc.confidence)}`}>{Math.round(rc.confidence * 100)}%</span>
              </div>
              <div className="flex items-center gap-1.5">
                <span className="text-[10px] text-zinc-500">Category</span>
                <span className="font-mono text-xs text-zinc-400">{rc.category}</span>
              </div>
            </div>
            {rc.source_files?.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {rc.source_files.map((f) => (
                  <code key={f} className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-cyan-400 font-mono">
                    {f}
                  </code>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Hypotheses */}
      {hypotheses.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Hypotheses ({hypotheses.length})</h3>
          {hypotheses.map((h) => (
            <div key={h.h_id} className={`rounded-lg border p-3 ${h.falsified ? "border-red-500/20 bg-red-500/5" : h.confidence >= 0.5 ? "border-cyan-500/20 bg-zinc-900/40" : "border-zinc-800 bg-zinc-900/20"}`}>
              <div className="flex items-start gap-2">
                <span className={`font-mono text-xs font-bold ${h.falsified ? "text-red-400" : h.confidence >= 0.5 ? "text-cyan-400" : "text-zinc-500"}`}>
                  {h.h_id}
                </span>
                <p className={`text-xs leading-relaxed flex-1 ${h.falsified ? "text-zinc-600 line-through" : "text-zinc-300"}`}>
                  {h.statement}
                </p>
                {!h.falsified && (
                  <span className={`font-mono text-xs ${confColor(h.confidence)}`}>{Math.round(h.confidence * 100)}%</span>
                )}
              </div>
              {!h.falsified && (
                <div className="mt-2 h-1 rounded-full bg-zinc-800 overflow-hidden">
                  <div className={`h-full ${h.confidence >= 0.7 ? "bg-emerald-500" : h.confidence >= 0.4 ? "bg-amber-500" : "bg-red-500"}`} style={{ width: `${h.confidence * 100}%` }} />
                </div>
              )}
              {h.falsified && (
                <span className="text-[10px] text-red-400/70 mt-1 block">FALSIFIED by contradicting evidence</span>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Fix Proposal with Code Diff */}
      {fp && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Fix Proposal</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3 space-y-3">
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="text-[10px] border-cyan-500/30 text-cyan-400 bg-cyan-500/5">
                {fp.fix_type}
              </Badge>
              <span className="text-[10px] text-zinc-500 font-mono">{fp.repo}</span>
              <span className="text-[10px] text-zinc-600">→</span>
              <span className="text-[10px] text-zinc-400 font-mono">{fp.branch_name}</span>
            </div>
            <p className="text-xs text-zinc-300 leading-relaxed">{fp.description}</p>

            {/* Code Diff View */}
            {fp.files_to_change.map((file) => (
              <div key={file.path} className="rounded-md border border-zinc-800 overflow-hidden">
                <div className="flex items-center gap-2 bg-zinc-900 px-3 py-1.5 border-b border-zinc-800">
                  <Code2 className="h-3 w-3 text-zinc-500" />
                  <span className="text-[10px] font-mono text-zinc-400">{file.path}</span>
                </div>
                <pre className="p-3 text-[10px] font-mono leading-relaxed overflow-x-auto">
                  <code className="text-emerald-400">+ {file.new_content.split("\n").join("\n+ ")}</code>
                </pre>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Execution Result */}
      {er && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Execution</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3 space-y-2">
            <div className="flex items-center gap-2">
              <GitPullRequest className="h-3.5 w-3.5 text-violet-400" />
              <a href={er.pr_url} target="_blank" rel="noopener noreferrer" className="text-xs text-violet-400 hover:text-violet-300 font-mono">
                PR #{er.pr_number}
              </a>
              <ExternalLink className="h-2.5 w-2.5 text-violet-400/50" />
            </div>
            <div className="flex items-center gap-3">
              <div className="flex items-center gap-1.5">
                {er.sandbox_verified ? (
                  <><CheckCircle2 className="h-3 w-3 text-emerald-400" /><span className="text-[10px] text-emerald-400">Sandbox Verified</span></>
                ) : (
                  <><AlertTriangle className="h-3 w-3 text-amber-400" /><span className="text-[10px] text-amber-400">Needs Review</span></>
                )}
              </div>
              <span className="text-[10px] text-zinc-500 font-mono">{er.sandbox_summary}</span>
            </div>
          </div>
        </div>
      )}

      {/* Agent Trace */}
      {trace.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Agent Trace</h3>
          <div className="space-y-1">
            {trace.map((t, i) => {
              const agent = String(t.agent || "unknown");
              const skipped = t.skipped as boolean;
              return (
                <div key={i} className={`flex items-center gap-2 rounded border px-2.5 py-1.5 ${skipped ? "border-zinc-800/50 bg-zinc-900/10 opacity-60" : "border-zinc-800/50 bg-zinc-900/20"}`}>
                  <div className={`flex h-5 w-5 items-center justify-center rounded text-[9px] ${skipped ? "bg-zinc-800 text-zinc-600" : "bg-cyan-500/10 text-cyan-400"}`}>
                    {i + 1}
                  </div>
                  <span className="text-xs text-zinc-300 font-medium">{agent}</span>
                  {skipped && <span className="text-[10px] text-zinc-600 italic">skipped ({t.reason})</span>}
                  <span className="text-[10px] text-zinc-600 font-mono ml-auto">
                    {Object.entries(t)
                      .filter(([k]) => k !== "agent" && k !== "skipped" && k !== "reason")
                      .map(([k, v]) => `${k}=${String(v)}`)
                      .join(" · ")}
                  </span>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Error */}
      {inc.state?.error && (
        <div className="rounded-lg border border-red-500/20 bg-red-500/5 p-3">
          <div className="flex items-center gap-2">
            <AlertTriangle className="h-3.5 w-3.5 text-red-400" />
            <span className="text-xs text-red-400 font-medium">Investigation Error</span>
          </div>
          <p className="text-xs text-zinc-400 mt-1 font-mono">{inc.state.error}</p>
        </div>
      )}

      {/* Beta Prior */}
      <div className="flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/30 px-3 py-2">
        <Cpu className="h-3.5 w-3.5 text-zinc-500" />
        <span className="text-[10px] text-zinc-500">Beta Prior</span>
        <span className="font-mono text-xs text-zinc-300">α={inc.beta_alpha} β={inc.beta_beta}</span>
        <span className="text-[10px] text-zinc-500">
          (success rate: {Math.round(inc.beta_alpha / (inc.beta_alpha + inc.beta_beta) * 100)}%)
        </span>
      </div>
    </div>
  );
}

// ─── Reusable Components ────────────────────────────────────────────────────

function MetricCard({ icon, label, value, sublabel, color }: {
  icon: React.ReactNode;
  label: string;
  value: string | number;
  sublabel: string;
  color: string;
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
  incidents: Incident[];
  onSelect: (inc: Incident) => void;
  loading: boolean;
  compact?: boolean;
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <RefreshCw className="h-4 w-4 animate-spin text-zinc-600" />
        <span className="ml-2 text-sm text-zinc-500">Loading incidents...</span>
      </div>
    );
  }

  if (incidents.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center">
        <CheckCircle2 className="h-8 w-8 text-emerald-400/50 mb-2" />
        <p className="text-sm text-zinc-500">No incidents detected.</p>
        <p className="text-xs text-zinc-600 mt-1">Deploy a service with sentinel.io/repo annotation to begin.</p>
      </div>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow className="border-zinc-800 hover:bg-transparent">
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500 font-mono">ID</TableHead>
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Service</TableHead>
          {!compact && <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Summary</TableHead>}
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Status</TableHead>
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Conf.</TableHead>
          {!compact && <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">PR</TableHead>}
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Time</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {incidents.map((inc) => {
          const conf = inc.state?.root_cause?.confidence;
          const prNum = inc.state?.execution_result?.pr_number;
          const sandbox = inc.state?.execution_result?.sandbox_verified;
          return (
            <TableRow key={inc.incident_id} className="border-zinc-800/50 cursor-pointer hover:bg-zinc-800/30 transition-colors" onClick={() => onSelect(inc)}>
              <TableCell className="font-mono text-xs text-zinc-400">{inc.incident_id.slice(0, 8)}</TableCell>
              <TableCell>
                <div className="flex flex-col">
                  <span className="text-xs font-medium text-zinc-200">{inc.cluster.service}</span>
                  <span className="text-[10px] text-zinc-600 font-mono">{inc.cluster.namespace}</span>
                </div>
              </TableCell>
              {!compact && (
                <TableCell className="text-xs text-zinc-400 max-w-xs truncate">{inc.cluster.summary}</TableCell>
              )}
              <TableCell>
                <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>{inc.status}</span>
              </TableCell>
              <TableCell>
                {conf !== undefined && conf !== null ? (
                  <span className={`font-mono text-xs ${confColor(conf)}`}>{Math.round(conf * 100)}%</span>
                ) : <span className="text-xs text-zinc-600">—</span>}
              </TableCell>
              {!compact && (
                <TableCell>
                  {prNum ? (
                    <div className="flex items-center gap-1">
                      <span className="font-mono text-xs text-violet-400">#{prNum}</span>
                      {sandbox !== undefined && (
                        sandbox ? <CheckCircle2 className="h-2.5 w-2.5 text-emerald-400" /> : <AlertTriangle className="h-2.5 w-2.5 text-amber-400" />
                      )}
                    </div>
                  ) : <span className="text-xs text-zinc-600">—</span>}
                </TableCell>
              )}
              <TableCell className="text-xs text-zinc-500">{timeAgo(inc.created_at)}</TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function StatusRow({ icon, label, status, detail }: {
  icon: React.ReactNode;
  label: string;
  status: string;
  detail: string;
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-zinc-500">{icon}</span>
      <span className="text-xs text-zinc-400">{label}</span>
      <div className="ml-auto flex items-center gap-2">
        <span className="text-[10px] text-zinc-600 font-mono truncate max-w-[120px]">{detail}</span>
        <div className={`h-1.5 w-1.5 rounded-full ${status === "active" ? "bg-emerald-400" : "bg-zinc-600"}`} />
      </div>
    </div>
  );
}

function ConfigRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between border-b border-zinc-800/50 pb-2">
      <span className="text-xs text-zinc-500">{label}</span>
      <span className={`text-xs text-zinc-200 ${mono ? "font-mono" : ""}`}>{value}</span>
    </div>
  );
}
