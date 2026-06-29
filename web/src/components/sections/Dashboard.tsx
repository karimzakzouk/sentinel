"use client";

import { useMemo } from "react";
import {
  AlertOctagon,
  GitPullRequest,
  FlaskConical,
  DollarSign,
  Server,
  GitMerge,
  TrendingUp,
  Activity as ActivityIcon,
} from "lucide-react";
import { listIncidents, listProviders, getAutoMergeState, getMetricsSummary } from "@/lib/api";
import type { Incident, Provider, AutoMergeState, MetricsSummary } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import { cn, formatCost, timeAgo, shortId } from "@/lib/utils";
import { Panel, Skeleton, ErrorState } from "@/components/ui-helpers";
import { ActivityFeed } from "@/components/ActivityFeed";
import { StatusBadge } from "@/components/StatusBadge";

export function Dashboard({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();

  const incidents = useFetch<Incident[]>(
    () => listIncidents({ last: "24h" }),
    [refreshKey, events.length],
    { intervalMs: 15000 },
  );
  const providers = useFetch<Provider[]>(
    () => listProviders(),
    [refreshKey, events.length],
    { intervalMs: 30000 },
  );
  const automerge = useFetch<AutoMergeState>(
    () => getAutoMergeState(),
    [refreshKey, events.length],
    { intervalMs: 15000 },
  );
  const metrics = useFetch<MetricsSummary>(
    () => getMetricsSummary(),
    [refreshKey, events.length],
    { intervalMs: 30000 },
  );

  const activeIncidents = useMemo(
    () =>
      (incidents.data || []).filter(
        (i) => !["resolved", "merged", "reverted", "failed"].includes(
          (i.status || "").toLowerCase(),
        ),
      ),
    [incidents.data],
  );

  const providerHealth = useMemo(() => {
    const list = providers.data || [];
    const healthy = list.filter((p) => (p.status || "").toLowerCase() === "healthy").length;
    return { healthy, total: list.length };
  }, [providers.data]);

  const llmCostToday = useMemo(() => {
    const costs = metrics.data?.llm_total_cost_usd || {};
    return Object.values(costs).reduce((a, b) => a + (b || 0), 0);
  }, [metrics.data]);

  const sandboxVerifications = useMemo(() => {
    const v = metrics.data?.sandbox_verifications_total || {};
    return Object.values(v).reduce((a, b) => a + (b || 0), 0);
  }, [metrics.data]);

  const prsOpened = metrics.data?.prs_opened_total ?? 0;
  const successRate = automerge.data?.stats?.success_rate ?? 0;

  return (
    <div className="flex flex-col gap-6">
      {/* KPI cards */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
        <KpiCard
          icon={AlertOctagon}
          label="Active Incidents"
          value={activeIncidents.length}
          loading={incidents.loading && !incidents.data}
          accent="rose"
          sub={`${(incidents.data || []).length} total (24h)`}
        />
        <KpiCard
          icon={GitPullRequest}
          label="PRs Opened"
          value={prsOpened}
          loading={metrics.loading && !metrics.data}
          accent="amber"
          sub="all-time"
        />
        <KpiCard
          icon={FlaskConical}
          label="Sandbox Verifications"
          value={sandboxVerifications}
          loading={metrics.loading && !metrics.data}
          accent="cyan"
          sub="all-time"
        />
        <KpiCard
          icon={DollarSign}
          label="LLM Cost (today)"
          value={formatCost(llmCostToday)}
          loading={metrics.loading && !metrics.data}
          accent="emerald"
          sub="all models"
        />
        <KpiCard
          icon={Server}
          label="Provider Health"
          value={`${providerHealth.healthy}/${providerHealth.total}`}
          loading={providers.loading && !providers.data}
          accent={providerHealth.healthy === providerHealth.total ? "emerald" : "amber"}
          sub={providerHealth.total === 0 ? "none configured" : undefined}
        />
        <KpiCard
          icon={GitMerge}
          label="Auto-merge Queue"
          value={(automerge.data?.queue || []).length}
          loading={automerge.loading && !automerge.data}
          accent="violet"
          sub={automerge.data?.enabled ? "enabled" : "disabled"}
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        {/* Success rate gauge */}
        <Panel title="Auto-merge Success Rate" description="merged & not reverted">
          {automerge.loading && !automerge.data ? (
            <Skeleton className="mx-auto h-40 w-40 rounded-full" />
          ) : automerge.error ? (
            <ErrorState message={automerge.error} onRetry={automerge.refetch} />
          ) : (
            <SuccessGauge
              rate={successRate}
              merged={automerge.data?.stats?.total_merged ?? 0}
              reverted={automerge.data?.stats?.total_reverted ?? 0}
            />
          )}
        </Panel>

        {/* Recent incidents */}
        <Panel
          title="Recent Incidents"
          description="last 24 hours"
          className="lg:col-span-2"
          bodyClassName="p-0"
        >
          {incidents.loading && !incidents.data ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : incidents.error ? (
            <div className="p-4">
              <ErrorState message={incidents.error} onRetry={incidents.refetch} />
            </div>
          ) : (incidents.data || []).length === 0 ? (
            <div className="p-4">
              <EmptyRow hint="No incidents in the last 24h" />
            </div>
          ) : (
            <div className="max-h-80 divide-y divide-zinc-800 overflow-y-auto scrollbar-sentinel">
              {(incidents.data || []).slice(0, 12).map((inc) => (
                <div
                  key={inc.incident_id}
                  className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-zinc-800/30"
                >
                  <StatusBadge status={inc.status} />
                  <span className="font-mono text-xs text-cyan-400">
                    {shortId(inc.incident_id)}
                  </span>
                  <span className="flex-1 truncate text-xs text-zinc-300">
                    {inc.cluster?.service}
                    <span className="text-muted-foreground">
                      {" "}
                      / {inc.cluster?.namespace}
                    </span>
                  </span>
                  <span className="shrink-0 text-[10px] text-muted-foreground">
                    {timeAgo(inc.created_at)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>

      {/* Live activity feed */}
      <Panel
        title="Live Activity"
        description="real-time SSE event stream"
        action={
          <span className="flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-cyan-400">
            <span className="size-1.5 animate-sentinel-pulse rounded-full bg-cyan-500" />
            live
          </span>
        }
      >
        <ActivityFeed events={events.slice(0, 30)} maxHeight="max-h-80" />
      </Panel>
    </div>
  );
}

// ─── KPI card ────────────────────────────────────────────────────────────────

const ACCENTS: Record<string, { ring: string; icon: string; text: string }> = {
  rose: { ring: "border-rose-500/20", icon: "text-rose-400", text: "text-rose-400" },
  amber: { ring: "border-amber-500/20", icon: "text-amber-400", text: "text-amber-400" },
  cyan: { ring: "border-cyan-500/20", icon: "text-cyan-400", text: "text-cyan-400" },
  emerald: { ring: "border-emerald-500/20", icon: "text-emerald-400", text: "text-emerald-400" },
  violet: { ring: "border-violet-500/20", icon: "text-violet-400", text: "text-violet-400" },
};

function KpiCard({
  icon: Icon,
  label,
  value,
  sub,
  accent = "cyan",
  loading,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string | number;
  sub?: string;
  accent?: keyof typeof ACCENTS;
  loading?: boolean;
}) {
  const a = ACCENTS[accent];
  return (
    <div
      className={cn(
        "flex flex-col gap-2 rounded-xl border bg-card p-4",
        a.ring,
      )}
    >
      <div className="flex items-center justify-between">
        <span className="text-[11px] uppercase tracking-wide text-muted-foreground">
          {label}
        </span>
        <Icon className={cn("size-4", a.icon)} />
      </div>
      {loading ? (
        <Skeleton className="h-7 w-16" />
      ) : (
        <span className={cn("font-mono text-2xl font-bold tabular-nums", a.text)}>
          {value}
        </span>
      )}
      {sub && <span className="text-[10px] text-muted-foreground">{sub}</span>}
    </div>
  );
}

// ─── Success rate gauge (CSS semicircle) ─────────────────────────────────────

function SuccessGauge({
  rate,
  merged,
  reverted,
}: {
  rate: number;
  merged: number;
  reverted: number;
}) {
  const pct = Math.round(rate * 100);
  const color =
    rate >= 0.9 ? "#10b981" : rate >= 0.7 ? "#f59e0b" : "#f43f5e";
  return (
    <div className="flex flex-col items-center gap-4 py-2">
      <div className="relative flex items-end justify-center">
        <svg width="180" height="100" viewBox="0 0 180 100">
          <path
            d="M 10 95 A 80 80 0 0 1 170 95"
            fill="none"
            stroke="#27272a"
            strokeWidth="12"
            strokeLinecap="round"
          />
          <path
            d="M 10 95 A 80 80 0 0 1 170 95"
            fill="none"
            stroke={color}
            strokeWidth="12"
            strokeLinecap="round"
            strokeDasharray={`${pct * 2.513} 251.3`}
            style={{ transition: "stroke-dasharray 0.6s ease" }}
          />
        </svg>
        <div className="absolute bottom-0 flex flex-col items-center">
          <span
            className="font-mono text-3xl font-bold tabular-nums"
            style={{ color }}
          >
            {pct}%
          </span>
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
            success
          </span>
        </div>
      </div>
      <div className="flex gap-6">
        <div className="flex items-center gap-1.5">
          <TrendingUp className="size-3.5 text-emerald-400" />
          <span className="font-mono text-sm tabular-nums text-emerald-400">{merged}</span>
          <span className="text-[10px] text-muted-foreground">merged</span>
        </div>
        <div className="flex items-center gap-1.5">
          <ActivityIcon className="size-3.5 text-rose-400" />
          <span className="font-mono text-sm tabular-nums text-rose-400">{reverted}</span>
          <span className="text-[10px] text-muted-foreground">reverted</span>
        </div>
      </div>
    </div>
  );
}

function EmptyRow({ hint }: { hint: string }) {
  return (
    <div className="flex items-center justify-center gap-2 rounded-md border border-dashed border-zinc-800 px-4 py-8 text-xs text-muted-foreground">
      <ActivityIcon className="size-3.5" />
      {hint}
    </div>
  );
}
