"use client";

import { useMemo } from "react";
import { BarChart3, Clock, Cpu, DollarSign, FlaskConical, AlertOctagon, GitMerge } from "lucide-react";
import { getMetricsSummary } from "@/lib/api";
import type { MetricsSummary } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import { formatCost } from "@/lib/utils";
import {
  Panel,
  Skeleton,
  ErrorState,
  EmptyState,
  SectionHeader,
} from "@/components/ui-helpers";
import {
  BarChartH,
  BarChartV,
  StackedBar,
  ChartLegend,
  type BarDatum,
} from "@/components/Chart";

const COLOR = {
  cyan: "bg-cyan-500",
  emerald: "bg-emerald-500",
  amber: "bg-amber-500",
  rose: "bg-rose-500",
  violet: "bg-violet-500",
  blue: "bg-blue-500",
  orange: "bg-orange-500",
};

export function Metrics({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();
  const { data, loading, error, refetch } = useFetch<MetricsSummary>(
    () => getMetricsSummary(),
    [refreshKey, events.length],
    { intervalMs: 30000 },
  );

  const incidentsByStatus = useMemo<BarDatum[]>(() => {
    const m = data?.incidents_total || {};
    return Object.entries(m)
      .map(([label, value]) => ({ label, value }))
      .sort((a, b) => b.value - a.value);
  }, [data]);

  const llmCalls = useMemo(() => {
    const m = data?.llm_calls_total || {};
    const byModel: Record<string, { success: number; error: number; total: number }> = {};
    for (const [key, count] of Object.entries(m)) {
      const [model, statusRaw] = key.split("|");
      const status = (statusRaw || "").toLowerCase();
      const modelKey = model || "unknown";
      if (!byModel[modelKey]) byModel[modelKey] = { success: 0, error: 0, total: 0 };
      byModel[modelKey].total += count;
      if (status.includes("error") || status.includes("fail")) {
        byModel[modelKey].error += count;
      } else {
        byModel[modelKey].success += count;
      }
    }
    return Object.entries(byModel)
      .map(([model, v]) => ({ model, ...v }))
      .sort((a, b) => b.total - a.total);
  }, [data]);

  const llmCost = useMemo<BarDatum[]>(() => {
    const m = data?.llm_total_cost_usd || {};
    return Object.entries(m)
      .map(([label, value]) => ({ label, value }))
      .sort((a, b) => b.value - a.value);
  }, [data]);

  const sandboxFailures = useMemo<BarDatum[]>(() => {
    const m = data?.sandbox_level_failures || {};
    return Object.entries(m)
      .map(([label, value]) => ({ label: `L${label}`, value }))
      .sort((a, b) => parseInt(a.label.slice(1)) - parseInt(b.label.slice(1)));
  }, [data]);

  const durationHistogram = useMemo(() => {
    const m = data?.investigation_duration_seconds || {};
    return Object.entries(m).map(([status, buckets]) => ({
      status,
      buckets: Array.isArray(buckets) ? buckets : [],
    }));
  }, [data]);

  const automergeByState = useMemo<BarDatum[]>(() => {
    const m = data?.auto_merge_total || {};
    return Object.entries(m)
      .map(([label, value]) => ({ label, value }))
      .sort((a, b) => b.value - a.value);
  }, [data]);

  const loadingState = loading && !data;

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Metrics"
        description="Prometheus-derived aggregates from the Sentinel pipeline"
      />

      {error && !data ? (
        <ErrorState message={error} onRetry={refetch} />
      ) : !data && !loading ? (
        <EmptyState
          icon={BarChart3}
          title="No metrics available"
          description="Metrics will populate once Sentinel starts processing incidents."
        />
      ) : (
        <>
          {/* Top-line counters */}
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <CounterCard icon={AlertOctagon} label="Investigations Started" value={data?.investigations_started_total ?? 0} />
            <CounterCard icon={GitMerge} label="PRs Opened" value={data?.prs_opened_total ?? 0} />
            <CounterCard icon={Cpu} label="LLM Models Used" value={llmCalls.length} />
            <CounterCard
              icon={DollarSign}
              label="Total LLM Cost"
              value={formatCost(llmCost.reduce((a, b) => a + b.value, 0))}
            />
          </div>

          <div className="grid gap-6 lg:grid-cols-2">
            {/* Incidents by status */}
            <Panel title="Incidents by Status" description="incidents_total">
              {loadingState ? (
                <Skeleton className="h-40 w-full" />
              ) : incidentsByStatus.length === 0 ? (
                <EmptyChart />
              ) : (
                <BarChartH data={incidentsByStatus} />
              )}
            </Panel>

            {/* LLM cost by model */}
            <Panel title="LLM Cost by Model" description="llm_total_cost_usd">
              {loadingState ? (
                <Skeleton className="h-40 w-full" />
              ) : llmCost.length === 0 ? (
                <EmptyChart />
              ) : (
                <BarChartH
                  data={llmCost}
                  valueFormatter={(v) => formatCost(v)}
                />
              )}
            </Panel>

            {/* LLM calls by model/status (stacked) */}
            <Panel title="LLM Calls by Model" description="success vs error (stacked)">
              {loadingState ? (
                <Skeleton className="h-40 w-full" />
              ) : llmCalls.length === 0 ? (
                <EmptyChart />
              ) : (
                <div className="space-y-3">
                  {llmCalls.map((m) => (
                    <div key={m.model} className="space-y-1">
                      <div className="flex items-center justify-between">
                        <span className="truncate font-mono text-xs text-zinc-200">
                          {m.model}
                        </span>
                        <span className="font-mono text-xs tabular-nums text-muted-foreground">
                          {m.total}
                        </span>
                      </div>
                      <StackedBar
                        segments={[
                          { label: "success", value: m.success, color: COLOR.emerald },
                          { label: "error", value: m.error, color: COLOR.rose },
                        ]}
                        total={m.total}
                      />
                    </div>
                  ))}
                  <ChartLegend
                    items={[
                      { label: "success", value: 0, color: COLOR.emerald },
                      { label: "error", value: 0, color: COLOR.rose },
                    ]}
                  />
                </div>
              )}
            </Panel>

            {/* Sandbox level failures */}
            <Panel title="Sandbox Level Failures" description="sandbox_level_failures">
              {loadingState ? (
                <Skeleton className="h-40 w-full" />
              ) : sandboxFailures.length === 0 ? (
                <EmptyChart />
              ) : (
                <BarChartV
                  data={sandboxFailures.map((d) => ({ ...d, color: COLOR.rose }))}
                  height={140}
                />
              )}
            </Panel>
          </div>

          {/* Investigation duration histogram */}
          <Panel
            title="Investigation Duration"
            description="investigation_duration_seconds (bucket counts by outcome)"
          >
            {loadingState ? (
              <Skeleton className="h-48 w-full" />
            ) : durationHistogram.length === 0 ? (
              <EmptyChart />
            ) : (
              <div className="space-y-4">
                {durationHistogram.map(({ status, buckets }) => {
                  const data: BarDatum[] = buckets.map((v, i) => ({
                    label: `b${i}`,
                    value: v,
                  }));
                  return (
                    <div key={status} className="space-y-1.5">
                      <div className="flex items-center gap-2">
                        <Clock className="size-3 text-cyan-400" />
                        <span className="font-mono text-xs text-zinc-200">{status}</span>
                        <span className="font-mono text-[10px] text-muted-foreground">
                          {buckets.length} buckets · {buckets.reduce((a, b) => a + b, 0)} samples
                        </span>
                      </div>
                      <BarChartV data={data} height={100} />
                    </div>
                  );
                })}
              </div>
            )}
          </Panel>

          {/* Auto-merge by state */}
          <Panel title="Auto-merge by State" description="auto_merge_total">
            {loadingState ? (
              <Skeleton className="h-32 w-full" />
            ) : automergeByState.length === 0 ? (
              <EmptyChart />
            ) : (
              <BarChartH data={automergeByState} />
            )}
          </Panel>
        </>
      )}
    </div>
  );
}

function CounterCard({
  icon: Icon,
  label,
  value,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string | number;
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl border border-zinc-800 bg-card p-4">
      <Icon className="size-5 text-cyan-400" />
      <div>
        <div className="font-mono text-xl font-bold tabular-nums text-zinc-50">
          {value}
        </div>
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
      </div>
    </div>
  );
}

function EmptyChart() {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
      <FlaskConical className="size-3.5" />
      No data
    </div>
  );
}
