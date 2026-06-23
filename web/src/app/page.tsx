"use client";

import { useState, useEffect, useCallback } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Activity, AlertTriangle, CheckCircle2, Clock, GitPullRequest,
  Shield, Zap, Server, Cpu, TrendingUp, Pause, Play, RefreshCw, ExternalLink
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
      files_to_change: Array<{ path: string }>;
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
  };
}

interface Metrics {
  incidents_total: Record<string, number>;
  investigations_started: number;
  prs_opened: number;
  sandbox_verifications: Record<string, number>;
  llm_calls: Record<string, number>;
  investigation_duration_sum: number;
  investigation_duration_count: number;
  active_incidents: number;
}

interface Settings {
  llm_provider: string;
  llm_fast_model: string;
  llm_frontier_model: string;
  github_token_set: boolean;
  write_namespaces: string[];
}

// ─── Mock Data (matches real Sentinel API structure) ────────────────────────

const mockIncidents: Incident[] = [
  {
    incident_id: "91cce5a5-d9d5-42b1-8ee3-38a5dd2bc327",
    created_at: "2026-06-23T19:03:40Z",
    updated_at: "2026-06-23T19:13:18Z",
    status: "resolved",
    beta_alpha: 2,
    beta_beta: 1,
    cluster: {
      service: "demo-app",
      namespace: "sim",
      summary: "CrashLoopBackOff: KeyError 'DATABASE_URLL'",
      severity: "warning",
      anomalies: [{
        detector: "auto_detection",
        score: 0.9,
        explanation: "KeyError: 'DATABASE_URLL'\n  File \"/app/app/main.py\", line 11",
      }],
    },
    state: {
      hypotheses: [
        {
          h_id: "H1",
          statement: "The application crashes with KeyError ('DATABASE_URLL') — the environment variable name has a typo (extra L).",
          confidence: 0.7,
          falsified: false,
          source_files: ["app/main.py"],
        },
        {
          h_id: "H2",
          statement: "The value 'DATABASE_URLL' is missing, misconfigured, or not provided",
          confidence: 0.3,
          falsified: false,
          source_files: [],
        },
      ],
      root_cause: {
        statement: "The application crashes with KeyError ('DATABASE_URLL') — the environment variable name has a typo (extra L).",
        confidence: 0.58,
        category: "other",
        source_files: ["app/main.py"],
      },
      fix_proposal: {
        fix_type: "code_fix",
        description: "Correct typo in environment variable name and add fallback handling for missing DATABASE_URL.",
        repo: "karimzakzouk/demo-app",
        branch_name: "sentinel/demo-app-91cce5a5",
        files_to_change: [{ path: "app/main.py" }],
        proposed_confidence: 0.58,
      },
      execution_result: {
        success: true,
        pr_url: "https://github.com/karimzakzouk/demo-app/pull/4",
        pr_number: 4,
        sandbox_verified: false,
        sandbox_summary: "canary_pods_not_ready (startup crash not fixed)",
      },
      agent_trace: [
        { agent: "investigator", hypothesis_count: 2, search_queries: 3, search_results: 1, fetched_files: ["app/main.py"] },
        { agent: "evidence_gatherer", evidence_count: 0 },
        { agent: "analyst", survivor: "H1", final_confidence: 0.58, prior_mean: 0.5, posterior_mean: 0.5 },
        { agent: "fix_proposer", fix_type: "code_fix", files_changed: 1, confidence: 0.58 },
      ],
    },
  },
  {
    incident_id: "a3f2b8c1-1234-5678-9abc-def012345678",
    created_at: "2026-06-23T15:22:10Z",
    updated_at: "2026-06-23T15:31:45Z",
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
        {
          h_id: "H1",
          statement: "PaymentProcessor.process() receives a null Payment object from the upstream service.",
          confidence: 0.75,
          falsified: false,
          source_files: ["src/main/java/com/acme/PaymentProcessor.java"],
        },
      ],
      root_cause: {
        statement: "PaymentProcessor.process() receives a null Payment object — missing null check before field access.",
        confidence: 0.72,
        category: "other",
        source_files: ["src/main/java/com/acme/PaymentProcessor.java"],
      },
      fix_proposal: {
        fix_type: "code_fix",
        description: "Add null check for Payment object before accessing fields, return 400 Bad Request if null.",
        repo: "acme/payment-service",
        branch_name: "sentinel/payment-api-a3f2b8c1",
        files_to_change: [{ path: "src/main/java/com/acme/PaymentProcessor.java" }],
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
        { agent: "investigator", hypothesis_count: 1, search_queries: 5, search_results: 3 },
        { agent: "evidence_gatherer", evidence_count: 4 },
        { agent: "analyst", survivor: "H1", final_confidence: 0.72 },
        { agent: "fix_proposer", fix_type: "code_fix", files_changed: 1, confidence: 0.72 },
      ],
    },
  },
  {
    incident_id: "b7e4d9f2-9876-5432-10fe-fedcba987654",
    created_at: "2026-06-23T10:15:30Z",
    updated_at: "2026-06-23T10:22:18Z",
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
        explanation: "OOMKilled — container exited with code 137",
      }],
    },
    state: {
      hypotheses: [],
      root_cause: null,
      fix_proposal: null,
      execution_result: null,
      agent_trace: [],
    },
  },
];

const mockMetrics: Metrics = {
  incidents_total: { resolved: 2, failed: 1 },
  investigations_started: 3,
  prs_opened: 2,
  sandbox_verifications: { true: 1, false: 1 },
  llm_calls: { "openai/gpt-oss-120b:free:success": 12, "openai/gpt-oss-120b:free:rate_limited": 2 },
  investigation_duration_sum: 812.5,
  investigation_duration_count: 3,
  active_incidents: 0,
};

const mockSettings: Settings = {
  llm_provider: "custom",
  llm_fast_model: "openai/gpt-oss-120b:free",
  llm_frontier_model: "openai/gpt-oss-120b:free",
  github_token_set: true,
  write_namespaces: ["sim"],
};

// ─── API Client ─────────────────────────────────────────────────────────────

const API_BASE = typeof window !== "undefined" ? (window as unknown as { __SENTINEL_API__?: string }).__SENTINEL_API__ || "" : "";

async function fetchAPI<T>(path: string): Promise<T> {
  try {
    const resp = await fetch(`${API_BASE}${path}`);
    if (!resp.ok) throw new Error(`API ${resp.status}`);
    return await resp.json() as T;
  } catch {
    // Fallback to mock data when API isn't available (dev/preview mode)
    return getMockData<T>(path);
  }
}

function getMockData<T>(path: string): T {
  if (path.includes("/incidents")) return mockIncidents as unknown as T;
  if (path.includes("/metrics-summary")) return { active: 0, resolved_24h: 2, failed: 1, total: 3 } as unknown as T;
  if (path.includes("/settings")) return mockSettings as unknown as T;
  return {} as T;
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

function statusColor(status: string): string {
  switch (status) {
    case "resolved": case "verified": return "text-emerald-400 bg-emerald-500/10 border-emerald-500/20";
    case "failed": case "escalated": return "text-red-400 bg-red-500/10 border-red-500/20";
    case "investigating": case "executing": return "text-blue-400 bg-blue-500/10 border-blue-500/20";
    default: return "text-amber-400 bg-amber-500/10 border-amber-500/20";
  }
}

function confidenceColor(conf: number): string {
  if (conf >= 0.7) return "text-emerald-400";
  if (conf >= 0.4) return "text-amber-400";
  return "text-red-400";
}

// ─── Main Page ──────────────────────────────────────────────────────────────

export default function Page() {
  const [tab, setTab] = useState("dashboard");
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [metrics, setMetrics] = useState({ active: 0, resolved_24h: 0, failed: 0, total: 0 });
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loading, setLoading] = useState(true);
  const [selectedIncident, setSelectedIncident] = useState<Incident | null>(null);
  const [paused, setPaused] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);

  const loadData = useCallback(async () => {
    const [inc, stats, cfg] = await Promise.all([
      fetchAPI<Incident[]>("/incidents?limit=50"),
      fetchAPI<{ active: number; resolved_24h: number; failed: number; total: number }>("/metrics-summary"),
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

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100">
      {/* Header */}
      <header className="sticky top-0 z-50 border-b border-zinc-800 bg-zinc-950/80 backdrop-blur-sm">
        <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-3">
          <div className="flex items-center gap-3">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-cyan-500/10 border border-cyan-500/20">
              <Shield className="h-4 w-4 text-cyan-400" />
            </div>
            <div>
              <h1 className="font-mono text-sm font-bold tracking-tight">SENTINEL</h1>
              <p className="text-[10px] text-zinc-500 -mt-0.5">Autonomous AI SRE</p>
            </div>
            <Badge variant="outline" className="ml-2 font-mono text-[10px] border-zinc-700 text-zinc-400">
              v3.6.0
            </Badge>
          </div>
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-1.5 text-xs">
              <div className={`h-2 w-2 rounded-full ${paused ? "bg-amber-400" : "bg-emerald-400 animate-pulse"}`} />
              <span className="text-zinc-400">{paused ? "PAUSED" : "ACTIVE"}</span>
            </div>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs text-zinc-400 hover:text-zinc-100"
              onClick={() => setAutoRefresh(!autoRefresh)}
            >
              <RefreshCw className={`h-3 w-3 mr-1 ${autoRefresh ? "animate-spin" : ""}`} />
              {autoRefresh ? "Live" : "Paused"}
            </Button>
          </div>
        </div>
      </header>

      {/* Main Content */}
      <main className="mx-auto max-w-7xl px-6 py-6">
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList className="bg-zinc-900 border border-zinc-800">
            <TabsTrigger value="dashboard" className="data-[state=active]:bg-zinc-800 data-[state=active]:text-cyan-400">
              <Activity className="h-3.5 w-3.5 mr-1.5" />
              Dashboard
            </TabsTrigger>
            <TabsTrigger value="incidents" className="data-[state=active]:bg-zinc-800 data-[state=active]:text-cyan-400">
              <AlertTriangle className="h-3.5 w-3.5 mr-1.5" />
              Incidents
              {metrics.total > 0 && (
                <span className="ml-1.5 rounded-full bg-zinc-800 px-1.5 py-0.5 text-[10px] font-mono">
                  {metrics.total}
                </span>
              )}
            </TabsTrigger>
            <TabsTrigger value="settings" className="data-[state=active]:bg-zinc-800 data-[state=active]:text-cyan-400">
              <Server className="h-3.5 w-3.5 mr-1.5" />
              Settings
            </TabsTrigger>
          </TabsList>

          {/* Dashboard Tab */}
          <TabsContent value="dashboard" className="mt-6 space-y-6">
            {/* Metric Cards */}
            <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
              <MetricCard
                icon={<AlertTriangle className="h-4 w-4" />}
                label="Total Incidents"
                value={metrics.total}
                sublabel={`${metrics.active} active`}
                color="text-cyan-400"
              />
              <MetricCard
                icon={<GitPullRequest className="h-4 w-4" />}
                label="PRs Opened"
                value={mockMetrics.prs_opened}
                sublabel="autonomous"
                color="text-violet-400"
              />
              <MetricCard
                icon={<CheckCircle2 className="h-4 w-4" />}
                label="Sandbox Pass Rate"
                value={`${Math.round((mockMetrics.sandbox_verifications.true || 0) / Math.max(Object.values(mockMetrics.sandbox_verifications).reduce((a, b) => a + b, 0), 1) * 100)}%`}
                sublabel={`${mockMetrics.sandbox_verifications.true || 0}/${Object.values(mockMetrics.sandbox_verifications).reduce((a, b) => a + b, 0)} verified`}
                color="text-emerald-400"
              />
              <MetricCard
                icon={<Clock className="h-4 w-4" />}
                label="Mean Time to PR"
                value={`${Math.round(mockMetrics.investigation_duration_sum / Math.max(mockMetrics.investigation_duration_count, 1) / 60)}m`}
                sublabel="avg investigation"
                color="text-amber-400"
              />
            </div>

            {/* Recent Incidents */}
            <Card className="border-zinc-800 bg-zinc-900/50">
              <CardHeader className="flex flex-row items-center justify-between pb-3">
                <CardTitle className="text-sm font-medium text-zinc-300">Recent Incidents</CardTitle>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 text-xs text-zinc-500 hover:text-zinc-300"
                  onClick={() => setTab("incidents")}
                >
                  View all →
                </Button>
              </CardHeader>
              <CardContent className="p-0">
                <IncidentsTable
                  incidents={incidents.slice(0, 5)}
                  onSelect={setSelectedIncident}
                  loading={loading}
                />
              </CardContent>
            </Card>

            {/* LLM Stats */}
            <Card className="border-zinc-800 bg-zinc-900/50">
              <CardHeader className="pb-3">
                <CardTitle className="text-sm font-medium text-zinc-300">LLM Activity</CardTitle>
              </CardHeader>
              <CardContent className="grid grid-cols-3 gap-4">
                <div className="space-y-1">
                  <p className="text-[10px] uppercase tracking-wider text-zinc-500">Total Calls</p>
                  <p className="font-mono text-lg text-zinc-200">
                    {Object.values(mockMetrics.llm_calls).reduce((a, b) => a + b, 0)}
                  </p>
                </div>
                <div className="space-y-1">
                  <p className="text-[10px] uppercase tracking-wider text-zinc-500">Success Rate</p>
                  <p className="font-mono text-lg text-emerald-400">
                    {Math.round((mockMetrics.llm_calls["openai/gpt-oss-120b:free:success"] || 0) / Object.values(mockMetrics.llm_calls).reduce((a, b) => a + b, 1) * 100)}%
                  </p>
                </div>
                <div className="space-y-1">
                  <p className="text-[10px] uppercase tracking-wider text-zinc-500">Model</p>
                  <p className="font-mono text-xs text-zinc-400 truncate">
                    {settings?.llm_frontier_model || "—"}
                  </p>
                </div>
              </CardContent>
            </Card>
          </TabsContent>

          {/* Incidents Tab */}
          <TabsContent value="incidents" className="mt-6">
            <Card className="border-zinc-800 bg-zinc-900/50">
              <CardHeader className="pb-3">
                <CardTitle className="text-sm font-medium text-zinc-300">All Incidents</CardTitle>
              </CardHeader>
              <CardContent className="p-0">
                <IncidentsTable
                  incidents={incidents}
                  onSelect={setSelectedIncident}
                  loading={loading}
                />
              </CardContent>
            </Card>
          </TabsContent>

          {/* Settings Tab */}
          <TabsContent value="settings" className="mt-6 space-y-6">
            <Card className="border-zinc-800 bg-zinc-900/50">
              <CardHeader className="pb-3">
                <CardTitle className="text-sm font-medium text-zinc-300">Configuration</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                {settings ? (
                  Object.entries(settings).map(([key, value]) => (
                    <div key={key} className="flex items-center justify-between border-b border-zinc-800/50 pb-2">
                      <span className="text-xs text-zinc-500 font-mono">{key}</span>
                      <span className="text-xs text-zinc-200 font-mono text-right">
                        {Array.isArray(value) ? value.join(", ") : String(value)}
                      </span>
                    </div>
                  ))
                ) : (
                  <p className="text-sm text-zinc-500">Loading...</p>
                )}
              </CardContent>
            </Card>

            <Card className="border-zinc-800 bg-zinc-900/50">
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
          </TabsContent>
        </Tabs>
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

// ─── Components ─────────────────────────────────────────────────────────────

function MetricCard({ icon, label, value, sublabel, color }: {
  icon: React.ReactNode;
  label: string;
  value: string | number;
  sublabel: string;
  color: string;
}) {
  return (
    <Card className="border-zinc-800 bg-zinc-900/50">
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

function IncidentsTable({ incidents, onSelect, loading }: {
  incidents: Incident[];
  onSelect: (inc: Incident) => void;
  loading: boolean;
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
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Status</TableHead>
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</TableHead>
          <TableHead className="text-[10px] uppercase tracking-wider text-zinc-500">Time</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {incidents.map((inc) => {
          const conf = inc.state?.root_cause?.confidence;
          return (
            <TableRow
              key={inc.incident_id}
              className="border-zinc-800/50 cursor-pointer hover:bg-zinc-800/30 transition-colors"
              onClick={() => onSelect(inc)}
            >
              <TableCell className="font-mono text-xs text-zinc-400">
                {inc.incident_id.slice(0, 8)}
              </TableCell>
              <TableCell>
                <div className="flex flex-col">
                  <span className="text-xs font-medium text-zinc-200">{inc.cluster.service}</span>
                  <span className="text-[10px] text-zinc-600 font-mono">{inc.cluster.namespace}</span>
                </div>
              </TableCell>
              <TableCell>
                <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>
                  {inc.status}
                </span>
              </TableCell>
              <TableCell>
                {conf !== undefined && conf !== null ? (
                  <span className={`font-mono text-xs ${confidenceColor(conf)}`}>
                    {Math.round(conf * 100)}%
                  </span>
                ) : (
                  <span className="text-xs text-zinc-600">—</span>
                )}
              </TableCell>
              <TableCell className="text-xs text-zinc-500">
                {timeAgo(inc.created_at)}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function IncidentDetail({ incident }: { incident: Incident }) {
  const inc = incident;
  const rc = inc.state?.root_cause;
  const fp = inc.state?.fix_proposal;
  const er = inc.state?.execution_result;
  const trace = inc.state?.agent_trace || [];

  const pipelineSteps = [
    { label: "Detection", done: true, detail: inc.cluster.summary },
    { label: "Investigation", done: inc.state?.hypotheses?.length > 0, detail: `${inc.state?.hypotheses?.length || 0} hypotheses` },
    { label: "Analysis", done: !!rc, detail: rc ? `${Math.round(rc.confidence * 100)}% confidence` : "—" },
    { label: "Fix Proposed", done: !!fp, detail: fp?.fix_type || "—" },
    { label: "Execution", done: !!er, detail: er?.pr_url ? `PR #${er.pr_number}` : "—" },
    { label: "Sandbox", done: !!er, detail: er?.sandbox_summary || "skipped" },
  ];

  return (
    <div className="space-y-6 pt-6">
      <SheetHeader>
        <div className="flex items-center gap-2">
          <SheetTitle className="font-mono text-base">
            {inc.incident_id.slice(0, 8)}
          </SheetTitle>
          <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium ${statusColor(inc.status)}`}>
            {inc.status}
          </span>
        </div>
        <div className="flex items-center gap-3 text-xs text-zinc-500">
          <span>{inc.cluster.service}</span>
          <span>·</span>
          <span className="font-mono">{inc.cluster.namespace}</span>
          <span>·</span>
          <span>{timeAgo(inc.created_at)}</span>
        </div>
      </SheetHeader>

      {/* Pipeline Trace */}
      <div className="space-y-2">
        <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Pipeline Trace</h3>
        <div className="space-y-1">
          {pipelineSteps.map((step, i) => (
            <div key={i} className="flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/30 px-3 py-2">
              <div className={`flex h-6 w-6 items-center justify-center rounded-full text-[10px] ${
                step.done ? "bg-emerald-500/10 text-emerald-400 border border-emerald-500/20" : "bg-zinc-800 text-zinc-600"
              }`}>
                {step.done ? <CheckCircle2 className="h-3 w-3" /> : i + 1}
              </div>
              <div className="flex-1 min-w-0">
                <p className={`text-xs font-medium ${step.done ? "text-zinc-200" : "text-zinc-600"}`}>{step.label}</p>
                <p className="text-[10px] text-zinc-500 truncate font-mono">{step.detail}</p>
              </div>
            </div>
          ))}
        </div>
      </div>

      <Separator className="bg-zinc-800" />

      {/* Root Cause */}
      {rc && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Root Cause</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/30 p-3">
            <p className="text-xs text-zinc-300 leading-relaxed">{rc.statement}</p>
            <div className="flex items-center gap-4 mt-2">
              <div className="flex items-center gap-1.5">
                <span className="text-[10px] text-zinc-500">Confidence</span>
                <span className={`font-mono text-xs ${confidenceColor(rc.confidence)}`}>
                  {Math.round(rc.confidence * 100)}%
                </span>
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
      {inc.state?.hypotheses && inc.state.hypotheses.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Hypotheses</h3>
          {inc.state.hypotheses.map((h) => (
            <div key={h.h_id} className="rounded-lg border border-zinc-800 bg-zinc-900/30 p-3">
              <div className="flex items-start gap-2">
                <span className={`font-mono text-xs font-bold ${h.falsified ? "text-red-400 line-through" : h.confidence >= 0.5 ? "text-cyan-400" : "text-zinc-500"}`}>
                  {h.h_id}
                </span>
                <p className={`text-xs leading-relaxed flex-1 ${h.falsified ? "text-zinc-600" : "text-zinc-300"}`}>
                  {h.statement}
                </p>
              </div>
              {!h.falsified && (
                <div className="mt-2">
                  <Progress value={h.confidence * 100} className="h-1 bg-zinc-800" />
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Fix */}
      {fp && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Fix Proposal</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/30 p-3 space-y-2">
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="text-[10px] border-cyan-500/30 text-cyan-400 bg-cyan-500/5">
                {fp.fix_type}
              </Badge>
              <span className="text-[10px] text-zinc-500 font-mono">{fp.repo}</span>
            </div>
            <p className="text-xs text-zinc-300 leading-relaxed">{fp.description}</p>
            <div className="flex flex-wrap gap-1">
              {fp.files_to_change.map((f) => (
                <code key={f.path} className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-emerald-400 font-mono">
                  {f.path}
                </code>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Execution */}
      {er && (
        <div className="space-y-2">
          <h3 className="text-[10px] uppercase tracking-wider text-zinc-500 font-medium">Execution</h3>
          <div className="rounded-lg border border-zinc-800 bg-zinc-900/30 p-3 space-y-2">
            <div className="flex items-center gap-2">
              <GitPullRequest className="h-3.5 w-3.5 text-violet-400" />
              <a
                href={er.pr_url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-xs text-violet-400 hover:text-violet-300 font-mono"
              >
                PR #{er.pr_number} <ExternalLink className="inline h-2.5 w-2.5" />
              </a>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-[10px] text-zinc-500">Sandbox:</span>
              {er.sandbox_verified ? (
                <span className="inline-flex items-center gap-1 text-[10px] text-emerald-400">
                  <CheckCircle2 className="h-3 w-3" /> Verified
                </span>
              ) : (
                <span className="inline-flex items-center gap-1 text-[10px] text-amber-400">
                  <AlertTriangle className="h-3 w-3" /> Needs Review
                </span>
              )}
            </div>
            <p className="text-[10px] text-zinc-500 font-mono">{er.sandbox_summary}</p>
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
              return (
                <div key={i} className="flex items-center gap-2 rounded border border-zinc-800/50 bg-zinc-900/20 px-2 py-1.5">
                  <div className="flex h-5 w-5 items-center justify-center rounded text-[9px] bg-zinc-800 text-zinc-400">
                    {i + 1}
                  </div>
                  <span className="text-xs text-zinc-300 font-medium">{agent}</span>
                  <span className="text-[10px] text-zinc-600 font-mono ml-auto">
                    {Object.entries(t)
                      .filter(([k]) => k !== "agent")
                      .map(([k, v]) => `${k}=${String(v)}`)
                      .join(" · ")}
                  </span>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Beta Prior */}
      <div className="flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/30 px-3 py-2">
        <Cpu className="h-3.5 w-3.5 text-zinc-500" />
        <span className="text-[10px] text-zinc-500">Beta Prior</span>
        <span className="font-mono text-xs text-zinc-300">
          α={inc.beta_alpha} β={inc.beta_beta}
        </span>
        <span className="text-[10px] text-zinc-500">
          (success rate: {Math.round(inc.beta_alpha / (inc.beta_alpha + inc.beta_beta) * 100)}%)
        </span>
      </div>
    </div>
  );
}
