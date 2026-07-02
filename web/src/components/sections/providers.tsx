"use client";

import { useMemo } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Panel,
  PanelHeader,
  Mono,
  formatTokens,
  formatCost,
  EmptyState,
  ErrorState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { Cpu, Zap, AlertTriangle, DollarSign, Activity, Inbox, Clock } from "lucide-react";
import { cn } from "@/lib/utils";

const statusDot: Record<string, string> = {
  healthy: "bg-emerald-400",
  tripped: "bg-rose-400 animate-pulse-dot",
  cooling: "bg-amber-400",
  degraded: "bg-amber-400",
};

const statusText: Record<string, string> = {
  healthy: "text-emerald-400",
  tripped: "text-rose-400",
  cooling: "text-amber-400",
  degraded: "text-amber-400",
};

const tooltipStyle = {
  background: "#18181B",
  border: "1px solid #27272A",
  borderRadius: 6,
  fontSize: 11,
};

export function ProvidersSection() {
  const providers = useSentinelStore((s) => s.providers);
  const metrics = useSentinelStore((s) => s.metrics);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const refreshProviders = useSentinelStore((s) => s.refreshProviders);

  // Cost-by-model data for the bar chart, built from the real
  // metrics.llm_total_cost_usd map.
  const costBarData = useMemo(() => {
    if (!metrics?.llm_total_cost_usd) return [];
    return Object.entries(metrics.llm_total_cost_usd).map(([model, cost]) => ({
      name: model,
      cost,
    }));
  }, [metrics]);

  // Tokens-by-model data from metrics.llm_tokens_total (key format: "model|direction")
  const tokensBarData = useMemo(() => {
    if (!metrics?.llm_tokens_total) return [];
    const byModel: Record<string, { input: number; output: number }> = {};
    for (const [key, count] of Object.entries(metrics.llm_tokens_total)) {
      const [model, direction] = key.split("|");
      if (!byModel[model]) byModel[model] = { input: 0, output: 0 };
      if (direction === "input" || direction === "prompt") {
        byModel[model].input += count;
      } else {
        byModel[model].output += count;
      }
    }
    return Object.entries(byModel).map(([model, v]) => ({
      name: model,
      input: v.input,
      output: v.output,
    }));
  }, [metrics]);

  // LLM calls by status (success/error) from metrics.llm_calls_total
  const llmCallsSummary = useMemo(() => {
    if (!metrics?.llm_calls_total) return { total: 0, success: 0, error: 0 };
    let total = 0;
    let success = 0;
    let error = 0;
    for (const [key, count] of Object.entries(metrics.llm_calls_total)) {
      total += count;
      const status = key.split("|")[1]?.toLowerCase() ?? "";
      if (status === "success" || status === "ok" || status === "200") success += count;
      if (status === "error" || status === "fail" || status === "failed") error += count;
    }
    return { total, success, error };
  }, [metrics]);

  const totalCost = useMemo(() => {
    if (!metrics?.llm_total_cost_usd) return 0;
    return Object.values(metrics.llm_total_cost_usd).reduce((a, b) => a + b, 0);
  }, [metrics]);

  const trippedCount = providers.filter((p) => p.status !== "healthy").length;

  if (loading && providers.length === 0) {
    return (
      <div className="space-y-3 p-6">
        <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
      </div>
    );
  }

  if (error && providers.length === 0) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={() => refreshProviders()} />
      </div>
    );
  }

  if (providers.length === 0) {
    return (
      <div className="p-6">
        <EmptyState
          title="No providers configured"
          description="Configure LLM providers in Settings → LLM. Provider health, circuit breaker state, and per-model cost will appear here."
          icon={Cpu}
        />
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      {/* Pool status table */}
      <Panel>
        <PanelHeader
          title="Provider Pool"
          sub="LLM providers with circuit breaker states"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {providers.length} providers
            </Badge>
          }
        />
        <div className="overflow-x-auto scrollbar-thin">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Provider</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Priority</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Model</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Fast Model</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Frontier Model</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Trips</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Last Trip</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Trip Reason</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((p) => (
                <TableRow key={p.name} className="border-zinc-800/60 hover:bg-zinc-800/30">
                  <TableCell className="px-3 py-2">
                    <div className="flex items-center gap-2">
                      <Cpu className="h-3.5 w-3.5 text-zinc-500" />
                      <span className="text-xs font-medium text-zinc-200">{p.name}</span>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                      P{p.priority}
                    </Badge>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <div className="flex items-center gap-1.5">
                      <span className={cn("h-1.5 w-1.5 rounded-full", statusDot[p.status] ?? "bg-zinc-500")} />
                      <span className={cn("font-mono text-[11px]", statusText[p.status] ?? "text-zinc-400")}>
                        {p.status}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-300">{p.model || "—"}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-400">{p.fast_model || "—"}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-400">{p.frontier_model || "—"}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className={cn("text-[11px]", p.trips_total > 0 ? "text-rose-400" : "text-zinc-400")}>
                      {p.trips_total}
                    </Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    {p.last_trip ? (
                      <Mono className="text-[11px] text-zinc-400">
                        {new Date(p.last_trip).toLocaleString("en-US", { hour12: false })}
                      </Mono>
                    ) : (
                      <span className="text-[11px] text-zinc-600">—</span>
                    )}
                  </TableCell>
                  <TableCell className="px-3 py-2 max-w-[200px]">
                    {p.last_trip_reason ? (
                      <span className="block truncate text-[11px] text-amber-400" title={p.last_trip_reason}>
                        {p.last_trip_reason}
                      </span>
                    ) : (
                      <span className="text-[11px] text-zinc-600">—</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <div className="border-t border-zinc-800 px-4 py-2 text-[11px] text-zinc-500">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span className="flex items-center gap-1">
              <Zap className="h-3 w-3 text-cyan-400" />
              LLM calls (total):{" "}
              <Mono className="text-zinc-300">{llmCallsSummary.total.toLocaleString()}</Mono>
            </span>
            <span className="flex items-center gap-1">
              <AlertTriangle className="h-3 w-3 text-rose-400" />
              Active trips:{" "}
              <Mono className="text-rose-300">{trippedCount}</Mono>
            </span>
            <span className="flex items-center gap-1">
              <DollarSign className="h-3 w-3 text-cyan-400" />
              Total cost:{" "}
              <Mono className="text-cyan-300">{formatCost(totalCost)}</Mono>
            </span>
          </div>
        </div>
      </Panel>

      {/* Cost + tokens charts */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Panel>
          <PanelHeader title="LLM Cost by Model" sub="USD, cumulative" />
          <div className="px-4 py-3">
            {costBarData.length === 0 ? (
              <EmptyState
                title="No cost data yet"
                description="LLM cost will populate here after the first LLM call."
                icon={DollarSign}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={costBarData} margin={{ top: 5, right: 5, left: -10, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="name" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={{ color: "#FAFAFA" }} />
                    <Bar dataKey="cost" fill="#06B6D4" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>

        <Panel>
          <PanelHeader title="LLM Tokens by Model" sub="Input vs output (cumulative)" />
          <div className="px-4 py-3">
            {tokensBarData.length === 0 ? (
              <EmptyState
                title="No token data yet"
                description="Token usage will populate here after the first LLM call."
                icon={Activity}
                className="border-0 bg-transparent py-8"
              />
            ) : (
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={tokensBarData} margin={{ top: 5, right: 5, left: -10, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                    <XAxis dataKey="name" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={tooltipStyle} labelStyle={{ color: "#FAFAFA" }} />
                    <Legend wrapperStyle={{ fontSize: 10 }} />
                    <Bar dataKey="input" stackId="a" fill="#06B6D4" name="input" />
                    <Bar dataKey="output" stackId="a" fill="#22C55E" name="output" radius={[3, 3, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
        </Panel>
      </div>

      {/* Per-provider detail cards */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {providers.map((p) => (
          <Panel key={p.name}>
            <PanelHeader
              title={p.name}
              sub={p.model || "—"}
              right={
                <span className="flex items-center gap-1.5 text-[10px]">
                  <span className={cn("h-1.5 w-1.5 rounded-full", statusDot[p.status] ?? "bg-zinc-500")} />
                  <span className={cn(statusText[p.status] ?? "text-zinc-400")}>{p.status}</span>
                </span>
              }
            />
            <div className="grid grid-cols-2 gap-px bg-zinc-800">
              {[
                { label: "Priority", value: `P${p.priority}`, icon: Zap },
                { label: "Trips total", value: String(p.trips_total), icon: AlertTriangle },
                { label: "Fast model", value: p.fast_model || "—", icon: Activity },
                { label: "Frontier model", value: p.frontier_model || "—", icon: Activity },
                { label: "Last trip", value: p.last_trip ? new Date(p.last_trip).toLocaleDateString() : "—", icon: Clock },
                { label: "CB until", value: p.circuit_breaker_until ? new Date(p.circuit_breaker_until).toLocaleString("en-US", { hour12: false }) : "—", icon: Clock },
              ].map((s) => {
                const Icon = s.icon;
                return (
                  <div key={s.label} className="bg-zinc-900/60 px-3 py-2">
                    <div className="flex items-center gap-1 text-[9px] uppercase tracking-wider text-zinc-500">
                      <Icon className="h-2.5 w-2.5" /> {s.label}
                    </div>
                    <Mono className="mt-0.5 block truncate text-[12px] text-zinc-200" title={s.value}>
                      {s.value}
                    </Mono>
                  </div>
                );
              })}
            </div>
            {p.last_trip_reason && (
              <div className="border-t border-zinc-800 px-3 py-2">
                <div className="text-[9px] uppercase tracking-wider text-zinc-500">Last trip reason</div>
                <p className="mt-0.5 font-mono text-[11px] text-amber-300">{p.last_trip_reason}</p>
              </div>
            )}
          </Panel>
        ))}
      </div>

      {/* Hint when metrics has no LLM data */}
      {metrics && costBarData.length === 0 && tokensBarData.length === 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-cyan-500/20 bg-cyan-500/5 px-4 py-3">
          <Inbox className="h-4 w-4 shrink-0 text-cyan-400" />
          <p className="text-xs text-zinc-300">
            Provider pool is live, but no LLM calls have been recorded yet. Cost and token charts will populate after the first investigation.
          </p>
        </div>
      )}
    </div>
  );
}
