"use client";

import { Card, CardContent } from "@/components/ui/card";
import { Panel, PanelHeader, PulseDot, Mono, timeAgo } from "@/components/sentinel/common";
import { useSentinel } from "@/lib/store";
import { incidentHeatmap, heatmapDays, metrics7d } from "@/lib/mock-data";
import {
  AlertTriangle,
  GitPullRequest,
  FlaskConical,
  DollarSign,
  Cpu,
  Merge,
  Radio,
  FlaskConical as Flask,
  Eye,
  ArrowUpRight,
  ArrowDownRight,
} from "lucide-react";
import {
  RadialBarChart,
  RadialBar,
  ResponsiveContainer,
  PolarAngleAxis,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
} from "recharts";
import * as Icons from "lucide-react";

function KpiCard({
  label,
  value,
  unit,
  icon: Icon,
  trend,
  trendUp,
  accent,
  pulse,
  sub,
}: {
  label: string;
  value: string | number;
  unit?: string;
  icon: React.ComponentType<{ className?: string }>;
  trend?: string;
  trendUp?: boolean;
  accent: string;
  pulse?: boolean;
  sub?: string;
}) {
  return (
    <Card className="gap-0 border-zinc-800 bg-zinc-900/50 py-0">
      <CardContent className="px-4 py-3.5">
        <div className="flex items-start justify-between">
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <Icon className={`h-3.5 w-3.5 ${accent}`} />
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
              className={`flex items-center gap-0.5 font-mono text-[11px] ${
                trendUp ? "text-emerald-400" : "text-rose-400"
              }`}
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

function heatColor(v: number): string {
  // 0..8 intensity
  if (v === 0) return "bg-zinc-900";
  if (v <= 1) return "bg-cyan-500/10";
  if (v <= 2) return "bg-cyan-500/25";
  if (v <= 3) return "bg-cyan-500/40";
  if (v <= 4) return "bg-cyan-500/55";
  if (v <= 5) return "bg-amber-500/60";
  if (v <= 6) return "bg-amber-500/75";
  return "bg-rose-500/80";
}

export function DashboardSection() {
  const activityFeed = useSentinel((s) => s.activityFeed);
  const tick = useSentinel((s) => s.tick);

  const successData = [{ name: "success", value: 89.2, fill: "#06B6D4" }];

  return (
    <div className="p-6">
      {/* KPI grid */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <KpiCard
          label="Active Incidents"
          value={2}
          icon={AlertTriangle}
          accent="text-amber-400"
          pulse
          trend="+1 (1h)"
          trendUp={false}
          sub="1 investigating · 1 escalated"
        />
        <KpiCard
          label="PRs Opened Today"
          value={7}
          icon={GitPullRequest}
          accent="text-cyan-400"
          trend="+3 vs avg"
          trendUp
          sub="5 merged · 1 queued · 1 closed"
        />
        <KpiCard
          label="Sandbox Verifications"
          value="5/7"
          icon={FlaskConical}
          accent="text-emerald-400"
          trend="71% pass"
          trendUp
          sub="2 in progress"
        />
        <KpiCard
          label="LLM Cost Today"
          value="$0.14"
          icon={DollarSign}
          accent="text-cyan-400"
          trend="-$0.04"
          trendUp
          sub="94.2k tokens consumed"
        />
        <KpiCard
          label="Provider Health"
          value="2/3"
          icon={Cpu}
          accent="text-amber-400"
          sub="Groq circuit open"
        />
        <KpiCard
          label="Auto-Merge Queue"
          value={1}
          icon={Merge}
          accent="text-cyan-400"
          sub="1 pending · rate 2/3hr"
        />
      </div>

      {/* Second row: success gauge + heatmap + activity */}
      <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-12">
        {/* Success rate gauge */}
        <Panel className="lg:col-span-3">
          <PanelHeader title="7-day Success Rate" sub="Rolling auto-merge success" />
          <div className="px-4 py-3">
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
                <span className="font-mono text-3xl font-semibold text-zinc-50">89.2%</span>
                <span className="mt-0.5 text-[11px] text-zinc-500">38 merged · 2 reverted</span>
              </div>
            </div>
            <div className="mt-2 flex items-center justify-between border-t border-zinc-800 pt-2 text-[11px]">
              <span className="text-zinc-500">Target</span>
              <span className="font-mono text-emerald-400">≥ 85.0%</span>
            </div>
          </div>
        </Panel>

        {/* Heatmap */}
        <Panel className="lg:col-span-6">
          <PanelHeader
            title="Incident Heatmap"
            sub="7 days × 24 hours · color = incident volume"
            right={
              <div className="flex items-center gap-1 text-[10px] text-zinc-500">
                <span>low</span>
                <div className="flex gap-0.5">
                  <div className="h-2.5 w-3 bg-zinc-900" />
                  <div className="h-2.5 w-3 bg-cyan-500/25" />
                  <div className="h-2.5 w-3 bg-cyan-500/55" />
                  <div className="h-2.5 w-3 bg-amber-500/60" />
                  <div className="h-2.5 w-3 bg-rose-500/80" />
                </div>
                <span>high</span>
              </div>
            }
          />
          <div className="px-4 py-3">
            <div className="flex">
              {/* Day labels */}
              <div className="mr-1 flex flex-col gap-1">
                <div className="h-3 text-[9px] text-zinc-600" />
                {heatmapDays.map((d) => (
                  <div key={d} className="flex h-3 items-center text-[9px] text-zinc-500">
                    {d}
                  </div>
                ))}
              </div>
              <div className="flex-1">
                {/* Hour labels */}
                <div className="mb-1 flex gap-0.5 text-[9px] text-zinc-600">
                  {[0, 4, 8, 12, 16, 20, 23].map((h) => (
                    <div key={h} className="w-3.5 text-center">
                      {String(h).padStart(2, "0")}
                    </div>
                  ))}
                </div>
                {incidentHeatmap.map((day, di) => (
                  <div key={di} className="mb-0.5 flex gap-0.5">
                    {day.map((v, hi) => (
                      <div
                        key={hi}
                        className={`h-3 w-3.5 rounded-sm ${heatColor(v)} ${v >= 5 ? "ring-1 ring-rose-500/40" : ""}`}
                        title={`${heatmapDays[di]} ${String(hi).padStart(2, "0")}:00 — ${v} incidents`}
                      />
                    ))}
                  </div>
                ))}
              </div>
            </div>
            <div className="mt-3 grid grid-cols-3 gap-2 border-t border-zinc-800 pt-2 text-[11px]">
              <div>
                <div className="text-zinc-500">Peak hour</div>
                <div className="font-mono text-zinc-300">14:00–15:00 UTC</div>
              </div>
              <div>
                <div className="text-zinc-500">Total (7d)</div>
                <div className="font-mono text-zinc-300">142 incidents</div>
              </div>
              <div>
                <div className="text-zinc-500">Mean/day</div>
                <div className="font-mono text-zinc-300">20.3</div>
              </div>
            </div>
          </div>
        </Panel>

        {/* Activity feed */}
        <Panel className="lg:col-span-3">
          <PanelHeader
            title="Activity Feed"
            sub="Live events"
            right={
              <span className="flex items-center gap-1 text-[10px] text-emerald-400">
                <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
                streaming
              </span>
            }
          />
          <div className="max-h-80 overflow-y-auto scrollbar-thin">
            {activityFeed.slice(0, 12).map((e) => {
              const Icon = (Icons as unknown as Record<string, React.ComponentType<{ className?: string }>>)[e.icon] ?? Radio;
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
            })}
          </div>
        </Panel>
      </div>

      {/* Third row: 7-day activity sparkline + provider mini-status */}
      <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-12">
        <Panel className="lg:col-span-8">
          <PanelHeader
            title="Daily Incident Volume"
            sub="By status · last 7 days"
            right={
              <div className="flex items-center gap-3 text-[10px]">
                <span className="flex items-center gap-1 text-amber-400"><span className="h-2 w-2 rounded-sm bg-amber-400" />investigating</span>
                <span className="flex items-center gap-1 text-emerald-400"><span className="h-2 w-2 rounded-sm bg-emerald-400" />verified</span>
                <span className="flex items-center gap-1 text-rose-400"><span className="h-2 w-2 rounded-sm bg-rose-400" />failed</span>
                <span className="flex items-center gap-1 text-cyan-400"><span className="h-2 w-2 rounded-sm bg-cyan-400" />resolved</span>
              </div>
            }
          />
          <div className="px-4 py-3" data-tick={tick}>
            <div className="h-44">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={metrics7d} margin={{ top: 5, right: 5, left: -20, bottom: 0 }}>
                  <defs>
                    <linearGradient id="gResolved" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#06B6D4" stopOpacity={0.5} />
                      <stop offset="100%" stopColor="#06B6D4" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="gVerified" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#22C55E" stopOpacity={0.4} />
                      <stop offset="100%" stopColor="#22C55E" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <XAxis dataKey="day" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip
                    contentStyle={{
                      background: "#18181B",
                      border: "1px solid #27272A",
                      borderRadius: 6,
                      fontSize: 11,
                    }}
                    labelStyle={{ color: "#FAFAFA" }}
                  />
                  <Area type="monotone" dataKey="resolved" stackId="1" stroke="#06B6D4" strokeWidth={1.5} fill="url(#gResolved)" />
                  <Area type="monotone" dataKey="verified" stackId="1" stroke="#22C55E" strokeWidth={1.5} fill="url(#gVerified)" />
                  <Area type="monotone" dataKey="investigating" stackId="1" stroke="#EAB308" strokeWidth={1.5} fill="#EAB30820" />
                  <Area type="monotone" dataKey="failed" stackId="1" stroke="#EF4444" strokeWidth={1.5} fill="#EF444420" />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>

        <Panel className="lg:col-span-4">
          <PanelHeader title="Provider Pool" sub="Live circuit breaker states" />
          <div className="divide-y divide-zinc-800/60">
            {[
              { name: "OpenRouter", model: "claude-sonnet-4.5", state: "closed", color: "emerald" },
              { name: "Groq", model: "llama-3.3-70b", state: "open", color: "rose" },
              { name: "Nemotron", model: "nemotron-70b", state: "closed", color: "emerald" },
            ].map((p) => (
              <div key={p.name} className="flex items-center justify-between px-4 py-2.5">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-medium text-zinc-200">{p.name}</span>
                    <span className={`h-1.5 w-1.5 rounded-full ${p.color === "emerald" ? "bg-emerald-400" : "bg-rose-400 animate-pulse-dot"}`} />
                  </div>
                  <Mono className="text-[10px] text-zinc-500">{p.model}</Mono>
                </div>
                <div className="text-right">
                  <Mono className={`text-[11px] ${p.color === "emerald" ? "text-emerald-400" : "text-rose-400"}`}>
                    {p.state}
                  </Mono>
                  <div className="text-[10px] text-zinc-600">circuit</div>
                </div>
              </div>
            ))}
          </div>
          <div className="border-t border-zinc-800 px-4 py-2">
            <button className="text-[11px] text-cyan-400 hover:text-cyan-300">View all providers →</button>
          </div>
        </Panel>
      </div>
    </div>
  );
}
