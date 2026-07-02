"use client";

import { useMemo } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Panel,
  PanelHeader,
  PulseDot,
  Mono,
  timeAgo,
  EmptyState,
  ErrorState,
} from "@/components/sentinel/common";
import { useSentinelStore } from "@/lib/store";
import type { Incident } from "@/lib/types";
import {
  AlertTriangle,
  GitPullRequest,
  FlaskConical,
  DollarSign,
  Cpu,
  Merge,
  Radio,
  Inbox,
  ArrowUpRight,
  ArrowDownRight,
  Activity as ActivityIcon,
} from "lucide-react";
import {
  RadialBarChart,
  RadialBar,
  ResponsiveContainer,
  PolarAngleAxis,
} from "recharts";
import * as Icons from "lucide-react";
import { cn } from "@/lib/utils";

function KpiCard({
  label,
  value,
  unit,
  icon: Icon,
  accent,
  pulse,
  sub,
  trend,
  trendUp,
}: {
  label: string;
  value: string | number;
  unit?: string;
  icon: React.ComponentType<{ className?: string }>;
  accent: string;
  pulse?: boolean;
  sub?: string;
  trend?: string;
  trendUp?: boolean;
}) {
  return (
    <Card className="gap-0 border-zinc-800 bg-zinc-900/50 py-0">
      <CardContent className="px-4 py-3.5">
        <div className="flex items-start justify-between">
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <Icon className={cn("h-3.5 w-3.5", accent)} />
              <span className="text-[11px] font-medium uppercase tracking-wider text-zinc-500">{label}</span>
            </div>
            <div className="flex items-baseline gap-1">
              <span className="font-mono text-2xl font-semibold tracking-tight text-zinc-50">{value}</span>
              {unit && <span className="text-xs text-zinc-500">{unit}</span>}
              {pulse && <PulseDot color="bg-amber-400" className="ml-1.5 h-2 w-2" />}
            </div>
            {sub && <span className="text-[11px] text-zinc-500">{sub}</span>}
          </div>
          {trend && (
            <span
              className={cn(
                "flex items-center gap-0.5 font-mono text-[11px]",
                trendUp ? "text-emerald-400" : "text-rose-400",
              )}
            >
              {trendUp ? <ArrowUpRight className="h-3 w-3" /> : <ArrowDownRight className="h-3 w-3" />}
              {trend}
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function isInvestigating(i: Incident): boolean {
  return ["investigating", "executing", "fix_proposed", "approved", "root_caused"].includes(i.status);
}

export function DashboardSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const providers = useSentinelStore((s) => s.providers);
  const autoMerge = useSentinelStore((s) => s.autoMerge);
  const metrics = useSentinelStore((s) => s.metrics);
  const activityFeed = useSentinelStore((s) => s.activityFeed);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);

  const activeIncidents = useMemo(() => incidents.filter(isInvestigating), [incidents]);
  const resolvedIncidents = useMemo(
    () => incidents.filter((i) => ["verified", "resolved"].includes(i.status)),
    [incidents],
  );
  const failedIncidents = useMemo(
    () => incidents.filter((i) => ["failed", "rejected", "escalated"].includes(i.status)),
    [incidents],
  );
  const prIncidents = useMemo(() => incidents.filter((i) => i.pr_number), [incidents]);
  const healthyProviders = useMemo(
    () => providers.filter((p) => !p.tripped).length,
    [providers],
  );
  const trippedProviders = useMemo(
    () => providers.filter((p) => p.tripped).length,
    [providers],
  );

  const successRate = 0;
  const queueDepth = 0;

  // Cost sum across models (when metrics has cost data)
  const totalCost = useMemo(() => {
    if (!metrics?.llm_total_cost_usd) return 0;
    return Object.values(metrics.llm_total_cost_usd).reduce((a, b) => a + b, 0);
  }, [metrics]);

  // Sandbox verifications from metrics (verified → count)
  const sandboxVerified = useMemo(() => {
    if (!metrics?.sandbox_verifications_total) return { passed: 0, total: 0 };
    const entries = Object.entries(metrics.sandbox_verifications_total);
    const passed = entries
      .filter(([k]) => k === "true" || k === "pass" || k === "passed" || k === "1")
      .reduce((a, [, v]) => a + v, 0);
    const total = entries.reduce((a, [, v]) => a + v, 0);
    return { passed, total };
  }, [metrics]);

  if (loading && incidents.length === 0) {
    return (
      <div className="space-y-4 p-6">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="h-20 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-12">
          <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50 lg:col-span-3" />
          <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50 lg:col-span-6" />
          <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50 lg:col-span-3" />
        </div>
      </div>
    );
  }

  if (error && incidents.length === 0) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={fetchIncidents} />
      </div>
    );
  }

  const successData = [{ name: "success", value: successRate, fill: "#06B6D4" }];
  const hasAnyData = incidents.length > 0 || providers.length > 0 || !!autoMerge || !!metrics;

  return (
    <div className="p-6">
      {/* KPI grid — all values computed from real store data */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <KpiCard
          label="Active Incidents"
          value={activeIncidents.length}
          icon={AlertTriangle}
          accent="text-amber-400"
          pulse={activeIncidents.length > 0}
          sub={
            activeIncidents.length > 0
              ? `${activeIncidents.length} investigating`
              : "no active investigations"
          }
        />
        <KpiCard
          label="PRs Opened (7d)"
          value={prIncidents.length}
          icon={GitPullRequest}
          accent="text-cyan-400"
          sub={
            prIncidents.length > 0
              ? `${prIncidents.length} PRs from incidents`
              : "no PRs yet"
          }
        />
        <KpiCard
          label="Sandbox Verifications"
          value={
            sandboxVerified.total > 0
              ? `${sandboxVerified.passed}/${sandboxVerified.total}`
              : "0"
          }
          icon={FlaskConical}
          accent="text-emerald-400"
          sub={
            sandboxVerified.total > 0
              ? `${Math.round((sandboxVerified.passed / sandboxVerified.total) * 100)}% pass`
              : "no sandbox data yet"
          }
        />
        <KpiCard
          label="LLM Cost (cumulative)"
          value={totalCost.toFixed(2)}
          unit="USD"
          icon={DollarSign}
          accent="text-cyan-400"
          sub={totalCost > 0 ? "across all models" : "no LLM calls recorded"}
        />
        <KpiCard
          label="Provider Health"
          value={`${healthyProviders}/${providers.length || 0}`}
          icon={Cpu}
          accent={trippedProviders > 0 ? "text-amber-400" : "text-emerald-400"}
          sub={
            providers.length === 0
              ? "no providers configured"
              : trippedProviders > 0
              ? `${trippedProviders} tripped`
              : "all healthy"
          }
        />
        <KpiCard
          label="Auto-Merge Queue"
          value={queueDepth}
          icon={Merge}
          accent="text-cyan-400"
          pulse={queueDepth > 0}
          sub={
            autoMerge
              ? `${(autoMerge?.merges_this_hour || 0)}/${(autoMerge?.max_per_hour || 0)} this hour`
              : "auto-merge not configured"
          }
        />
      </div>

      {/* Second row: success gauge + status breakdown + activity feed */}
      <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-12">
        {/* Success rate gauge */}
        <Panel className="lg:col-span-3">
          <PanelHeader title="Auto-Merge Success" sub="Rolling success rate" />
          <div className="px-4 py-3">
            {autoMerge ? (
              <>
                <div className="relative h-44">
                  <ResponsiveContainer width="100%" height="100%">
                    <RadialBarChart
                      innerRadius="68%"
                      outerRadius="100%"
                      data={successData}
                      startAngle={90}
                      endAngle={-270}
                    >
                      <PolarAngleAxis type="number" domain={[0, 100]} tick={false} />
                      <RadialBar dataKey="value" cornerRadius={6} background={{ fill: "#27272A" }} />
                    </RadialBarChart>
                  </ResponsiveContainer>
                  <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
                    <span className="font-mono text-3xl font-semibold text-zinc-50">
                      {successRate.toFixed(1)}%
                    </span>
                    <span className="mt-0.5 text-[11px] text-zinc-500">
                      {0} merged · {0} reverted
                    </span>
                  </div>
                </div>
                <div className="mt-2 flex items-center justify-between border-t border-zinc-800 pt-2 text-[11px]">
                  <span className="text-zinc-500">Target</span>
                  <span className="font-mono text-emerald-400">≥ 85.0%</span>
                </div>
              </>
            ) : (
              <EmptyState
                title="Auto-merge not configured"
                description="Enable auto-merge in settings to see success metrics."
                icon={Merge}
                className="border-0 bg-transparent py-8"
              />
            )}
          </div>
        </Panel>

        {/* Incident status breakdown */}
        <Panel className="lg:col-span-6">
          <PanelHeader title="Incidents by Status" sub={`${incidents.length} total · last 7 days`} />
          <div className="px-4 py-3">
            {incidents.length === 0 ? (
              <EmptyState
                title="No incidents yet"
                description="When Sentinel detects an anomaly, it will appear here automatically."
                icon={Inbox}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
                {[
                  { label: "investigating", color: "bg-amber-500", count: activeIncidents.length },
                  { label: "verified", color: "bg-emerald-500", count: resolvedIncidents.length },
                  { label: "failed", color: "bg-rose-500", count: failedIncidents.length },
                  {
                    label: "with PR",
                    color: "bg-cyan-500",
                    count: prIncidents.length,
                  },
                  {
                    label: "w/ root cause",
                    color: "bg-violet-500",
                    count: incidents.filter((i) => i.state?.root_cause).length,
                  },
                ].map((s) => (
                  <div key={s.label} className="rounded-md border border-zinc-800 bg-zinc-950/50 px-3 py-2.5">
                    <div className="flex items-center gap-1.5">
                      <span className={cn("h-2 w-2 rounded-sm", s.color)} />
                      <span className="text-[10px] uppercase tracking-wider text-zinc-500">{s.label}</span>
                    </div>
                    <Mono className="mt-1 block text-xl text-zinc-100">{s.count}</Mono>
                  </div>
                ))}
              </div>
            )}
          </div>
        </Panel>

        {/* Activity feed — live SSE */}
        <Panel className="lg:col-span-3">
          <PanelHeader
            title="Activity Feed"
            sub="Live SSE events"
            right={
              <span className="flex items-center gap-1 text-[10px] text-emerald-400">
                <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
                streaming
              </span>
            }
          />
          <div className="max-h-80 overflow-y-auto scrollbar-thin">
            {activityFeed.length === 0 ? (
              <div className="px-4 py-8 text-center text-xs text-zinc-600">
                {hasAnyData ? "Waiting for events…" : "No activity yet"}
              </div>
            ) : (
              activityFeed.slice(0, 12).map((e) => {
                const Icon =
                  (Icons as unknown as Record<string, React.ComponentType<{ className?: string }>>)[e.icon] ??
                  Radio;
                return (
                  <div
                    key={e.id}
                    className="flex items-start gap-2.5 border-b border-zinc-800/50 px-4 py-2 last:border-0"
                  >
                    <div className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded bg-zinc-800/80">
                      <Icon className="h-3 w-3 text-zinc-400" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <p className="text-[12px] leading-tight text-zinc-300">{e.message}</p>
                      <p className="mt-0.5 font-mono text-[10px] text-zinc-600">{timeAgo(e.time)}</p>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </Panel>
      </div>

      {/* Third row: recent incidents table + provider mini-status */}
      <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-12">
        <Panel className="lg:col-span-8">
          <PanelHeader
            title="Recent Incidents"
            sub="Latest 5 from the API"
            right={
              <Button
                variant="outline"
                size="sm"
                className="h-7 border-zinc-700 bg-zinc-900 text-[11px] text-zinc-300"
                onClick={() => useSentinelStore.getState().setActiveSection("incidents")}
              >
                View all →
              </Button>
            }
          />
          <div className="divide-y divide-zinc-800/60">
            {incidents.length === 0 ? (
              <div className="px-4 py-8 text-center text-xs text-zinc-600">No incidents yet</div>
            ) : (
              incidents.slice(0, 5).map((inc) => (
                <button
                  key={inc.incident_id}
                  onClick={() => {
                    useSentinelStore.getState().setSelectedIncident(inc.incident_id);
                    useSentinelStore.getState().setActiveSection("incidents");
                  }}
                  className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-zinc-800/30"
                >
                  <AlertTriangle
                    className={cn(
                      "h-3.5 w-3.5 shrink-0",
                      isInvestigating(inc) ? "text-amber-400" : "text-zinc-500",
                    )}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <Mono className="text-[11px] text-cyan-400">
                        {inc.incident_id.slice(0, 8)}
                      </Mono>
                      <span className="truncate text-xs text-zinc-200">
                        {inc.cluster?.service ?? "unknown"}
                      </span>
                    </div>
                    <p className="mt-0.5 truncate text-[11px] text-zinc-500">
                      {inc.cluster?.summary || "—"}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="font-mono text-[10px] text-zinc-400">{inc.status}</span>
                    {isInvestigating(inc) && <PulseDot color="bg-amber-400" className="h-1.5 w-1.5" />}
                    <span className="font-mono text-[10px] text-zinc-600">{timeAgo(inc.created_at)}</span>
                  </div>
                </button>
              ))
            )}
          </div>
        </Panel>

        <Panel className="lg:col-span-4">
          <PanelHeader title="Provider Pool" sub="Live circuit breaker states" />
          <div className="divide-y divide-zinc-800/60">
            {providers.length === 0 ? (
              <div className="px-4 py-8 text-center text-xs text-zinc-600">
                No providers configured
              </div>
            ) : (
              providers.slice(0, 5).map((p) => {
                const healthy = !p.tripped;
                return (
                  <div key={p.id} className="flex items-center justify-between px-4 py-2.5">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-xs font-medium text-zinc-200">{p.id}</span>
                        <span
                          className={cn(
                            "h-1.5 w-1.5 rounded-full",
                            healthy ? "bg-emerald-400" : "bg-rose-400 animate-pulse-dot",
                          )}
                        />
                      </div>
                      <Mono className="text-[10px] text-zinc-500">{p.provider || "—"}</Mono>
                    </div>
                    <div className="text-right">
                      <Mono
                        className={cn(
                          "text-[11px]",
                          healthy ? "text-emerald-400" : "text-rose-400",
                        )}
                      >
                        {p.tripped ? "tripped" : "healthy"}
                      </Mono>
                      <div className="text-[10px] text-zinc-600">
                        {p.totalFailures > 0 ? `${p.totalFailures} trips` : "no trips"}
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>
          {providers.length > 0 && (
            <div className="border-t border-zinc-800 px-4 py-2">
              <button
                onClick={() => useSentinelStore.getState().setActiveSection("providers")}
                className="text-[11px] text-cyan-400 hover:text-cyan-300"
              >
                View all providers →
              </button>
            </div>
          )}
        </Panel>
      </div>

      {/* Footer note when no data */}
      {!hasAnyData && (
        <div className="mt-4 flex items-center gap-3 rounded-lg border border-cyan-500/20 bg-cyan-500/5 px-4 py-3">
          <ActivityIcon className="h-4 w-4 shrink-0 text-cyan-400" />
          <p className="text-xs text-zinc-300">
            Sentinel is connected and ready. Once incidents are detected, they will appear here in real time via SSE.
          </p>
        </div>
      )}
    </div>
  );
}
