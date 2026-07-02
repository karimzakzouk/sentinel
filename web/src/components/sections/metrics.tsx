"use client";

import { Panel, PanelHeader, Mono } from "@/components/sentinel/common";
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
import {
  metrics7d,
  metrics30d,
  investigationDuration,
  llmCallsByModel,
  llmCostByModel,
  sandboxLevelFailures,
  autoMergeByState,
} from "@/lib/mock-data";
import { TrendingUp, TrendingDown, Activity, DollarSign, Clock, CheckCircle2, XCircle } from "lucide-react";

function StatTile({
  label,
  value,
  delta,
  deltaUp,
  icon: Icon,
  color,
}: {
  label: string;
  value: string;
  delta?: string;
  deltaUp?: boolean;
  icon: React.ComponentType<{ className?: string }>;
  color: string;
}) {
  return (
    <Panel>
      <div className="px-4 py-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
            <Icon className={`h-3 w-3 ${color}`} /> {label}
          </div>
          {delta && (
            <span className={`flex items-center gap-0.5 font-mono text-[10px] ${deltaUp ? "text-emerald-400" : "text-rose-400"}`}>
              {deltaUp ? <TrendingUp className="h-2.5 w-2.5" /> : <TrendingDown className="h-2.5 w-2.5" />}
              {delta}
            </span>
          )}
        </div>
        <Mono className="mt-1 block text-xl text-zinc-100">{value}</Mono>
      </div>
    </Panel>
  );
}

const tooltipStyle = {
  background: "#18181B",
  border: "1px solid #27272A",
  borderRadius: 6,
  fontSize: 11,
};
const labelStyle = { color: "#FAFAFA" };

export function MetricsSection() {
  return (
    <div className="space-y-3 p-6">
      {/* Top stats */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="Incidents (7d)" value="142" delta="+8%" deltaUp={false} icon={Activity} color="text-amber-400" />
        <StatTile label="Auto-merges (7d)" value="38" delta="+12%" deltaUp icon={CheckCircle2} color="text-emerald-400" />
        <StatTile label="LLM cost (7d)" value="$92.44" delta="-4%" deltaUp icon={DollarSign} color="text-cyan-400" />
        <StatTile label="Mean TTR" value="12m 04s" delta="-2m" deltaUp icon={Clock} color="text-cyan-400" />
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        {/* Incidents by status — stacked bar 7d */}
        <Panel>
          <PanelHeader title="Incidents by Status" sub="Stacked daily, last 7 days" />
          <div className="px-4 py-3">
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={metrics7d} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                  <XAxis dataKey="day" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                  <Legend wrapperStyle={{ fontSize: 10 }} />
                  <Bar dataKey="investigating" stackId="a" fill="#EAB308" name="investigating" />
                  <Bar dataKey="verified" stackId="a" fill="#22C55E" name="verified" />
                  <Bar dataKey="failed" stackId="a" fill="#EF4444" name="failed" />
                  <Bar dataKey="escalated" stackId="a" fill="#F97316" name="escalated" />
                  <Bar dataKey="resolved" stackId="a" fill="#06B6D4" name="resolved" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>

        {/* Investigation duration histogram */}
        <Panel>
          <PanelHeader title="Investigation Duration" sub="Histogram, last 7 days" />
          <div className="px-4 py-3">
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={investigationDuration} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                  <XAxis dataKey="bucket" tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                  <Bar dataKey="count" fill="#06B6D4" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>

        {/* LLM calls by model — stacked bar */}
        <Panel>
          <PanelHeader title="LLM Calls by Model" sub="Stacked by agent role" />
          <div className="px-4 py-3">
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={llmCallsByModel} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                  <XAxis dataKey="model" tick={{ fill: "#71717A", fontSize: 9 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                  <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                  <Legend wrapperStyle={{ fontSize: 10 }} />
                  <Bar dataKey="investigator" stackId="a" fill="#06B6D4" name="investigator" />
                  <Bar dataKey="evidence" stackId="a" fill="#22C55E" name="evidence" />
                  <Bar dataKey="analyst" stackId="a" fill="#EAB308" name="analyst" />
                  <Bar dataKey="fixer" stackId="a" fill="#A855F7" name="fixer" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        </Panel>

        {/* LLM cost by model — bar */}
        <Panel>
          <PanelHeader title="LLM Cost by Model" sub="USD, last 24h" />
          <div className="px-4 py-3">
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
          </div>
        </Panel>

        {/* Sandbox level failures — bar */}
        <Panel>
          <PanelHeader title="Sandbox Level Failures" sub="Failure count per level, last 7d" />
          <div className="px-4 py-3">
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
          </div>
        </Panel>

        {/* Auto-merge by state — donut */}
        <Panel>
          <PanelHeader title="Auto-Merge by State" sub="Distribution, last 7d" />
          <div className="px-4 py-3">
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
          </div>
        </Panel>
      </div>

      {/* Cost trend 30 days */}
      <Panel>
        <PanelHeader title="Cost Trend (30 days)" sub="USD per day" />
        <div className="px-4 py-3">
          <div className="h-72">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={metrics30d} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#27272A" vertical={false} />
                <XAxis dataKey="day" tick={{ fill: "#71717A", fontSize: 9 }} axisLine={false} tickLine={false} interval={3} />
                <YAxis tick={{ fill: "#71717A", fontSize: 10 }} axisLine={false} tickLine={false} />
                <Tooltip contentStyle={tooltipStyle} labelStyle={labelStyle} />
                <Line type="monotone" dataKey="cost" stroke="#06B6D4" strokeWidth={2} dot={false} name="cost ($)" />
              </LineChart>
            </ResponsiveContainer>
          </div>
        </div>
      </Panel>
    </div>
  );
}
