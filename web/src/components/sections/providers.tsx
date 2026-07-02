"use client";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Panel, PanelHeader, Mono, formatTokens } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { providers, providerCostTrend } from "@/lib/mock-data";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  LineChart,
  Line,
  Legend,
} from "recharts";
import { Cpu, Zap, AlertTriangle, DollarSign, Clock, Activity } from "lucide-react";
import { cn } from "@/lib/utils";

const stateColor: Record<string, string> = {
  closed: "text-emerald-400",
  open: "text-rose-400",
  "half-open": "text-amber-400",
};

const statusDot: Record<string, string> = {
  healthy: "bg-emerald-400",
  tripped: "bg-rose-400 animate-pulse-dot",
  degraded: "bg-amber-400",
};

const costBarData = providers.map((p) => ({
  name: p.name,
  "Input tokens": p.inputTokens / 1_000_000,
  "Output tokens": p.outputTokens / 1_000_000,
  cost: p.cost,
}));

export function ProvidersSection() {
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
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Circuit</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Calls (24h)</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Success</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Avg Latency</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Trips</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Input tok</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Output tok</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Cost</TableHead>
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
                      <span className={cn("h-1.5 w-1.5 rounded-full", statusDot[p.status])} />
                      <span className={cn("font-mono text-[11px]", p.status === "healthy" ? "text-emerald-400" : p.status === "tripped" ? "text-rose-400" : "text-amber-400")}>
                        {p.status}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-300">{p.model}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className={cn("text-[11px]", stateColor[p.circuitState])}>{p.circuitState}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-300">{p.calls.toLocaleString()}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <div className="flex items-center gap-1.5">
                      <div className="h-1.5 w-12 overflow-hidden rounded-full bg-zinc-800">
                        <div
                          className={cn("h-full rounded-full", p.successRate >= 95 ? "bg-emerald-500" : p.successRate >= 80 ? "bg-amber-500" : "bg-rose-500")}
                          style={{ width: `${p.successRate}%` }}
                        />
                      </div>
                      <Mono className="text-[10px] text-zinc-400">{p.successRate}%</Mono>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-300">{p.avgLatency}ms</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className={cn("text-[11px]", p.trips > 0 ? "text-rose-400" : "text-zinc-400")}>{p.trips}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2"><Mono className="text-[11px] text-zinc-300">{formatTokens(p.inputTokens)}</Mono></TableCell>
                  <TableCell className="px-3 py-2"><Mono className="text-[11px] text-zinc-300">{formatTokens(p.outputTokens)}</Mono></TableCell>
                  <TableCell className="px-3 py-2"><Mono className="text-[11px] text-cyan-400">${p.cost.toFixed(2)}</Mono></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <div className="border-t border-zinc-800 px-4 py-2 text-[11px] text-zinc-500">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span className="flex items-center gap-1"><Zap className="h-3 w-3 text-cyan-400" />Total calls (24h): <Mono className="text-zinc-300">2,338</Mono></span>
            <span className="flex items-center gap-1"><AlertTriangle className="h-3 w-3 text-rose-400" />Active trips: <Mono className="text-rose-300">1</Mono></span>
            <span className="flex items-center gap-1"><DollarSign className="h-3 w-3 text-cyan-400" />Total cost (24h): <Mono className="text-cyan-300">$92.44</Mono></span>
          </div>
        </div>
      </Panel>

      {/* Cost breakdown charts */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Panel>
          <PanelHeader title="Cost Breakdown by Model" sub="Tokens (M) and USD cost" />
          <div className="px-4 py-3">
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={costBarData} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                  <XAxis dataKey="name" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis yAxisId="left" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis yAxisId="right" orientation="right" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip
                    contentStyle={{ background: "#18181B", border: "1px solid #27272A", borderRadius: 6, fontSize: 11 }}
                    labelStyle={{ color: "#FAFAFA" }}
                  />
                  <Legend wrapperStyle={{ fontSize: 10 }} />
                  <Bar yAxisId="left" dataKey="Input tokens" fill="#06B6D4" radius={[3, 3, 0, 0]} />
                  <Bar yAxisId="left" dataKey="Output tokens" fill="#22C55E" radius={[3, 3, 0, 0]} />
                  <Bar yAxisId="right" dataKey="cost" fill="#EAB308" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>

        <Panel>
          <PanelHeader title="Cost Trend (7 days)" sub="USD per provider per day" />
          <div className="px-4 py-3">
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={providerCostTrend} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                  <XAxis dataKey="day" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip
                    contentStyle={{ background: "#18181B", border: "1px solid #27272A", borderRadius: 6, fontSize: 11 }}
                    labelStyle={{ color: "#FAFAFA" }}
                  />
                  <Legend wrapperStyle={{ fontSize: 10 }} />
                  <Line type="monotone" dataKey="openrouter" stroke="#06B6D4" strokeWidth={2} dot={{ r: 2, fill: "#06B6D4" }} name="OpenRouter" />
                  <Line type="monotone" dataKey="groq" stroke="#EF4444" strokeWidth={2} dot={{ r: 2, fill: "#EF4444" }} name="Groq" />
                  <Line type="monotone" dataKey="nemotron" stroke="#A855F7" strokeWidth={2} dot={{ r: 2, fill: "#A855F7" }} name="Nemotron" />
                </LineChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>
      </div>

      {/* Per-provider detailed cards */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {providers.map((p) => (
          <Panel key={p.name}>
            <PanelHeader
              title={p.name}
              sub={p.model}
              right={
                <span className="flex items-center gap-1.5 text-[10px]">
                  <span className={cn("h-1.5 w-1.5 rounded-full", statusDot[p.status])} />
                  <span className={cn(stateColor[p.circuitState])}>{p.circuitState}</span>
                </span>
              }
            />
            <div className="grid grid-cols-2 gap-px bg-zinc-800">
              {[
                { label: "Calls", value: p.calls.toLocaleString(), icon: Activity },
                { label: "Success", value: `${p.successRate}%`, icon: Zap },
                { label: "p50 latency", value: `${Math.round(p.avgLatency * 0.7)}ms`, icon: Clock },
                { label: "p99 latency", value: `${Math.round(p.avgLatency * 2.1)}ms`, icon: Clock },
                { label: "Input tok", value: formatTokens(p.inputTokens), icon: Activity },
                { label: "Output tok", value: formatTokens(p.outputTokens), icon: Activity },
                { label: "Trips", value: String(p.trips), icon: AlertTriangle },
                { label: "Cost", value: `$${p.cost.toFixed(2)}`, icon: DollarSign },
              ].map((s) => {
                const Icon = s.icon;
                return (
                  <div key={s.label} className="bg-zinc-900/60 px-3 py-2">
                    <div className="flex items-center gap-1 text-[9px] uppercase tracking-wider text-zinc-500">
                      <Icon className="h-2.5 w-2.5" /> {s.label}
                    </div>
                    <Mono className="mt-0.5 block text-[12px] text-zinc-200">{s.value}</Mono>
                  </div>
                );
              })}
            </div>
          </Panel>
        ))}
      </div>
    </div>
  );
}
