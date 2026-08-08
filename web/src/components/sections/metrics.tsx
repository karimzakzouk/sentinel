"use client";

import { useMemo } from "react";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from "recharts";
import { Panel, PanelHeader, Mono, EmptyState } from "@/components/sentinel/common";
import { useSentinelStore } from "@/lib/store";
import { Activity, CheckCircle2, AlertCircle, Inbox } from "lucide-react";

const tooltipStyle = {
  background: "#18181B",
  border: "1px solid #27272A",
  borderRadius: 6,
  fontSize: 11,
};

interface StatusBucket {
  status: string;
  count: number;
}

export function MetricsSection() {
  const metrics = useSentinelStore((s) => s.metrics);
  const refreshMetrics = useSentinelStore((s) => s.refreshMetrics);

  // /metrics-summary returns {active, resolved_24h, failed, total} (a small
  // flat object). v5 expected a richer shape with incidents_total,
  // sandbox_verifications_total, llm_total_cost_usd, etc. — none of which
  // the API serves. v1 only renders what's actually in the response.
  const buckets: StatusBucket[] = useMemo(() => {
    if (!metrics) return [];
    const out: StatusBucket[] = [];
    if (metrics.active) out.push({ status: "active", count: metrics.active });
    if (metrics.resolved_24h) out.push({ status: "resolved_24h", count: metrics.resolved_24h });
    if (metrics.failed) out.push({ status: "failed", count: metrics.failed });
    return out;
  }, [metrics]);

  const total = metrics?.total ?? 0;

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Activity className="h-3 w-3 text-amber-400" /> Active
            </div>
            <Mono className="mt-1 block text-xl text-amber-400">{metrics?.active ?? 0}</Mono>
            <div className="text-[10px] text-zinc-500">currently investigating</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <CheckCircle2 className="h-3 w-3 text-emerald-400" /> Resolved (24h)
            </div>
            <Mono className="mt-1 block text-xl text-emerald-400">{metrics?.resolved_24h ?? 0}</Mono>
            <div className="text-[10px] text-zinc-500">last 24h</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <AlertCircle className="h-3 w-3 text-rose-400" /> Failed
            </div>
            <Mono className="mt-1 block text-xl text-rose-400">{metrics?.failed ?? 0}</Mono>
            <div className="text-[10px] text-zinc-500">rejected / failed</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Activity className="h-3 w-3 text-cyan-400" /> Total
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{total}</Mono>
            <div className="text-[10px] text-zinc-500">last 100 incidents</div>
          </div>
        </Panel>
      </div>

      <Panel>
        <PanelHeader
          title="Incidents by Lifecycle"
          sub="From /metrics-summary (last 100 incidents)"
          right={
            <button onClick={() => refreshMetrics()} className="text-[11px] text-cyan-400 hover:text-cyan-300">
              Refresh →
            </button>
          }
        />
        <div className="p-4">
          {!metrics || buckets.length === 0 ? (
            <EmptyState
              title="No metrics data"
              description="The /metrics-summary endpoint returned no counts. Once Sentinel processes incidents, the breakdown will appear here."
              icon={Inbox}
              className="border-0 bg-transparent py-8"
            />
          ) : (
            <div className="h-72">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={buckets} margin={{ top: 10, right: 20, left: 0, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" />
                  <XAxis dataKey="status" stroke="#71717A" tick={{ fill: "#71717A", fontSize: 11 }} />
                  <YAxis stroke="#71717A" tick={{ fill: "#71717A", fontSize: 11 }} allowDecimals={false} />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    labelStyle={{ color: "#FAFAFA" }}
                    cursor={{ fill: "#27272A", opacity: 0.3 }}
                  />
                  <Bar dataKey="count" fill="#06B6D4" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>
      </Panel>

      <div className="flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/30 px-4 py-3 text-[11px] text-zinc-500">
        <AlertCircle className="h-4 w-4 shrink-0 text-zinc-500" />
        <p>
          The /metrics-summary endpoint returns a small flat object (active, resolved_24h, failed, total). For
          the raw Prometheus exposition, hit <Mono className="text-cyan-400">/metrics</Mono> directly — see API Explorer.
        </p>
      </div>
    </div>
  );
}
