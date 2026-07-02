"use client";

import { useMemo } from "react";
import {
  Panel,
  PanelHeader,
  Mono,
  EmptyState,
  formatCost,
} from "@/components/sentinel/common";
import {
  BarChart,
  Bar,
  LineChart,
  Line,
  PieChart,
  Pie,
  Cell,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { useSentinelStore } from "@/lib/store";
import { Activity, DollarSign, Clock, CheckCircle2, Inbox, TrendingUp } from "lucide-react";

const tooltipStyle = {
  background: "#18181B",
  border: "1px solid #27272A",
  borderRadius: 6,
  fontSize: 11,
};
const labelStyle = { color: "#FAFAFA" };

function StatTile({
  label,
  value,
  sub,
  icon: Icon,
  color,
}: {
  label: string;
  value: string;
  sub?: string;
  icon: React.ComponentType<{ className?: string }>;
  color: string;
}) {
  return (
    <Panel>
      <div className="px-4 py-3">
        <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
          <Icon className={`h-3 w-3 ${color}`} /> {label}
        </div>
        <Mono className="mt-1 block text-xl text-zinc-100">{value}</Mono>
        {sub && <div className="text-[10px] text-zinc-500">{sub}</div>}
      </div>
    </Panel>
  );
}

export function MetricsSection() {
  const metrics = useSentinelStore((s) => s.metrics);
  const refreshMetrics = useSentinelStore((s) => s.refreshMetrics);

  // Incidents by status (from metrics.incidents_total: status → count)
  const incidentsByStatus = useMemo(() => {
    if (!metrics?.incidents_total) return [];
    const colors: Record<string, string> = {
      investigating: "#EAB308",
      verified: "#22C55E",
      failed: "#EF4444",
      escalated: "#F97316",
      resolved: "#06B6D4",
      root_caused: "#06B6D4",
      fix_proposed: "#06B6D4",
      approved: "#22C55E",
      executing: "#06B6D4",
      rejected: "#EF4444",
    };
    return Object.entries(metrics.incidents_total).map(([status, count]) => ({
      status,
      count,
      fill: colors[status] ?? "#71717A",
    }));
  }, [metrics]);

  const totalIncidents = incidentsByStatus.reduce((a, b) => a + b.count, 0);

  // Investigation duration histogram
  const durationBuckets = useMemo(() => {
    if (!metrics?.investigation_duration_seconds) return [];
    const out: Array<{ bucket: string; count: number }> = [];
    for (const [status, buckets] of Object.entries(metrics.investigation_duration_seconds)) {
      if (!Array.isArray(buckets)) continue;
      buckets.forEach((sec, i) => {
        const bucket = `${status}-${i}`;
        out.push({ bucket, count: sec });
      });
    }
    return out;
  }, [metrics]);

  // LLM calls — by status (success/error) split by model
  const llmCallsByModel = useMemo(() => {
    if (!metrics?.llm_calls_total) return [];
    const byModel: Record<string, { success: number; error: number }> = {};
    for (const [key, count] of Object.entries(metrics.llm_calls_total)) {
      const [model, status] = key.split("|");
      if (!byModel[model]) byModel[model] = { success: 0, error: 0 };
      const s = (status ?? "").toLowerCase();
      if (s === "error" || s === "fail" || s === "failed") {
        byModel[model].error += count;
      } else {
        byModel[model].success += count;
      }
    }
    return Object.entries(byModel).map(([model, v]) => ({
      model,
      success: v.success,
      error: v.error,
    }));
  }, [metrics]);

  // LLM cost by model
  const llmCostByModel = useMemo(() => {
    if (!metrics?.llm_total_cost_usd) return [];
    return Object.entries(metrics.llm_total_cost_usd).map(([model, cost]) => ({
      model,
      cost,
    }));
  }, [metrics]);

  // Sandbox level failures
  const sandboxLevelFailures = useMemo(() => {
    if (!metrics?.sandbox_level_failures) return [];
    return Object.entries(metrics.sandbox_level_failures).map(([level, failures]) => ({
      level,
      failures,
    }));
  }, [metrics]);

  // Auto-merge by state
  const autoMergeByState = useMemo(() => {
    if (!metrics?.auto_merge_total) return [];
    const colors: Record<string, string> = {
      merged: "#22C55E",
      queued: "#06B6D4",
      blocked: "#EAB308",
      reverted: "#EF4444",
      rejected: "#EF4444",
      skipped: "#71717A",
    };
    return Object.entries(metrics.auto_merge_total).map(([name, value]) => ({
      name,
      value,
      color: colors[name] ?? "#71717A",
    }));
  }, [metrics]);

  // Aggregate stats
  const totalCost = llmCostByModel.reduce((a, b) => a + b.cost, 0);
  const totalMerged = autoMergeByState.find((s) => s.name === "merged")?.value ?? 0;
  const sandboxVerifiedTotal = useMemo(() => {
    if (!metrics?.sandbox_verifications_total) return { passed: 0, total: 0 };
    const entries = Object.entries(metrics.sandbox_verifications_total);
    const passed = entries
      .filter(([k]) => ["true", "pass", "passed", "1"].includes(k))
      .reduce((a, [, v]) => a + v, 0);
    const total = entries.reduce((a, [, v]) => a + v, 0);
    return { passed, total };
  }, [metrics]);

  if (!metrics) {
    return (
      <div className="p-6">
        <EmptyState
          title="No metrics data yet"
          description="Metrics will populate here after the first incident is detected and processed."
          icon={Inbox}
        />
        <div className="mt-4 flex justify-end">
          <button
            onClick={() => refreshMetrics()}
            className="text-[11px] text-cyan-400 hover:text-cyan-300"
          >
            Refresh metrics →
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      {/* Top stats */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile
          label="Incidents (total)"
          value={String(totalIncidents)}
          sub="all statuses"
          icon={Activity}
          color="text-amber-400"
        />
        <StatTile
          label="Auto-merges"
          value={String(totalMerged)}
          sub={`${autoMergeByState.find((s) => s.name === "reverted")?.value ?? 0} reverted`}
          icon={CheckCircle2}
          color="text-emerald-400"
        />
        <StatTile
          label="LLM cost (cumulative)"
          value={formatCost(totalCost)}
          sub={`${llmCostByModel.length} models`}
          icon={DollarSign}
          color="text-cyan-400"
        />
        <StatTile
          label="Sandbox pass rate"
          value={
            sandboxVerifiedTotal.total > 0
              ? `${Math.round((sandboxVerifiedTotal.passed / sandboxVerifiedTotal.total) * 100)}%`
              : "—"
          }
          sub={`${sandboxVerifiedTotal.passed}/${sandboxVerifiedTotal.total} verified`}
          icon={Clock}
          color="text-cyan-400"
        />
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        {/* Incidents by status */}
        <Panel>
          <PanelHeader title="Incidents by Status" sub="From metrics.incidents_total" />
          <div className="px-4 py-3">
            {incidentsByStatus.length === 0 ? (
              <EmptyState
                title="No incidents recorded"
                description="The incidents_total metric will populate after the first detection."
                icon={Inbox}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={incidentsByStatus} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="status" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Bar dataKey="count" radius={[3, 3, 0, 0]}>
                      {incidentsByStatus.map((entry, i) => (
                        <Cell key={i} fill={entry.fill} />
                      ))}
                    </Bar>
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        {/* Investigation duration */}
        <Panel>
          <PanelHeader title="Investigation Duration" sub="From metrics.investigation_duration_seconds" />
          <div className="px-4 py-3">
            {durationBuckets.length === 0 ? (
              <EmptyState
                title="No duration data yet"
                description="Duration histograms populate after investigations complete."
                icon={Clock}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={durationBuckets} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="bucket" tick={{ fill: "#71717A", fontSize: 9 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Bar dataKey="count" fill="#06B6D4" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        {/* LLM calls by model */}
        <Panel>
          <PanelHeader title="LLM Calls by Model" sub="Success vs error (cumulative)" />
          <div className="px-4 py-3">
            {llmCallsByModel.length === 0 ? (
              <EmptyState
                title="No LLM call data yet"
                description="LLM call counts populate after the first agent invocation."
                icon={Activity}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={llmCallsByModel} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="model" tick={{ fill: "#71717A", fontSize: 9 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Legend wrapperStyle={{ fontSize: 10 }} />
                    <Bar dataKey="success" stackId="a" fill="#22C55E" name="success" />
                    <Bar dataKey="error" stackId="a" fill="#EF4444" name="error" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        {/* LLM cost by model */}
        <Panel>
          <PanelHeader title="LLM Cost by Model" sub="USD, cumulative" />
          <div className="px-4 py-3">
            {llmCostByModel.length === 0 ? (
              <EmptyState
                title="No cost data yet"
                description="Cost data populates after the first billable LLM call."
                icon={DollarSign}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={llmCostByModel} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="model" tick={{ fill: "#71717A", fontSize: 9 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Bar dataKey="cost" fill="#06B6D4" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        {/* Sandbox level failures */}
        <Panel>
          <PanelHeader title="Sandbox Level Failures" sub="From metrics.sandbox_level_failures" />
          <div className="px-4 py-3">
            {sandboxLevelFailures.length === 0 ? (
              <EmptyState
                title="No sandbox failures recorded"
                description="Once a sandbox verification fails, the failure will be counted here."
                icon={CheckCircle2}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={sandboxLevelFailures} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="level" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Bar dataKey="failures" fill="#EF4444" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        {/* Auto-merge by state */}
        <Panel>
          <PanelHeader title="Auto-Merge by State" sub="From metrics.auto_merge_total" />
          <div className="px-4 py-3">
            {autoMergeByState.length === 0 ? (
              <EmptyState
                title="No auto-merge data yet"
                description="Auto-merge state counts populate after the first PR is queued."
                icon={CheckCircle2}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <PieChart>
                    <Pie
                      data={autoMergeByState}
                      cx="50%"
                      cy="50%"
                      innerRadius={50}
                      outerRadius={80}
                      paddingAngle={2}
                      dataKey="value"
                      nameKey="name"
                    >
                      {autoMergeByState.map((e, i) => (
                        <Cell key={i} fill={e.color} stroke="#09090B" strokeWidth={2} />
                      ))}
                    </Pie>
                    <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                    <Legend
                      wrapperStyle={{ fontSize: 10 }}
                      formatter={(v) => <span className="text-zinc-300">{v}</span>}
                    />
                  </PieChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>
      </div>

      {/* Raw metrics dump — useful for debugging the real API response */}
      <Panel>
        <PanelHeader
          title="Raw Metrics Summary"
          sub="GET /metrics-summary — full JSON"
          right={
            <button
              onClick={() => refreshMetrics()}
              className="flex items-center gap-1 text-[10px] text-cyan-400 hover:text-cyan-300"
            >
              <TrendingUp className="h-3 w-3" /> Refresh
            </button>
          }
        />
        <pre className="max-h-80 overflow-y-auto bg-zinc-950 p-4 font-mono text-[11px] leading-relaxed text-zinc-400 scrollbar-thin">
{JSON.stringify(metrics, null, 2)}
        </pre>
      </Panel>
    </div>
  );
}
