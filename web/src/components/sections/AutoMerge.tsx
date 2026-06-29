"use client";

import { GitMerge, Gauge, Snowflake, TrendingUp, TrendingDown, Check, X } from "lucide-react";
import { getAutoMergeState } from "@/lib/api";
import type { AutoMergeState } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import { cn, timeAgo, shortId, formatConfidence } from "@/lib/utils";
import {
  Panel,
  Skeleton,
  ErrorState,
  EmptyState,
  SectionHeader,
} from "@/components/ui-helpers";
import { StatusBadge } from "@/components/StatusBadge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

export function AutoMerge({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();
  const { data, loading, error, refetch } = useFetch<AutoMergeState>(
    () => getAutoMergeState(),
    [refreshKey, events.length],
    { intervalMs: 15000 },
  );

  const queue = data?.queue || [];
  const rl = data?.rate_limit;
  const stats = data?.stats;
  const cooldown = data?.cooldown;

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Auto-merge"
        description="Gated merge queue with rate limiting and cooldown"
      />

      {/* Stats row */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          icon={GitMerge}
          label="Total Merged"
          value={stats?.total_merged ?? 0}
          accent="emerald"
        />
        <StatCard
          icon={TrendingDown}
          label="Total Reverted"
          value={stats?.total_reverted ?? 0}
          accent="rose"
        />
        <StatCard
          icon={TrendingUp}
          label="Success Rate"
          value={formatConfidence(stats?.success_rate ?? 0)}
          accent="cyan"
        />
        <StatCard
          icon={Gauge}
          label="Queue Depth"
          value={queue.length}
          accent="violet"
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        {/* Rate limit gauge */}
        <Panel title="Rate Limit" description="merges per hour">
          {loading && !data ? (
            <Skeleton className="h-32 w-full" />
          ) : error ? (
            <ErrorState message={error} onRetry={refetch} />
          ) : (
            <RateLimitGauge
              used={rl?.merged_last_hour ?? 0}
              max={rl?.max_per_hour ?? 0}
              remaining={rl?.remaining ?? 0}
            />
          )}
        </Panel>

        {/* Cooldown */}
        <Panel title="Cooldown" description="global back-off state">
          {loading && !data ? (
            <Skeleton className="h-32 w-full" />
          ) : (
            <CooldownIndicator cooldown={cooldown} />
          )}
        </Panel>

        {/* Enabled state */}
        <Panel title="Mode" description="autonomous merge control">
          {loading && !data ? (
            <Skeleton className="h-32 w-full" />
          ) : (
            <div className="flex h-32 flex-col items-center justify-center gap-2">
              <span
                className={cn(
                  "flex size-14 items-center justify-center rounded-full border",
                  data?.enabled
                    ? "border-emerald-500/40 bg-emerald-500/10"
                    : "border-zinc-700 bg-zinc-800/50",
                )}
              >
                <GitMerge
                  className={cn(
                    "size-6",
                    data?.enabled ? "text-emerald-400" : "text-muted-foreground",
                  )}
                />
              </span>
              <span
                className={cn(
                  "font-mono text-sm font-bold uppercase tracking-wide",
                  data?.enabled ? "text-emerald-400" : "text-muted-foreground",
                )}
              >
                {data?.enabled ? "Enabled" : "Disabled"}
              </span>
              <span className="text-[10px] text-muted-foreground">
                {data?.enabled
                  ? "PRs auto-merge when gates pass"
                  : "requires manual /sentinel merge"}
              </span>
            </div>
          )}
        </Panel>
      </div>

      {/* Queue table */}
      <Panel title="Merge Queue" description={`${queue.length} pending`} bodyClassName="p-0">
        {loading && !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : error ? (
          <div className="p-4">
            <ErrorState message={error} onRetry={refetch} />
          </div>
        ) : queue.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={GitMerge}
              title="Queue is empty"
              description="No PRs are waiting for auto-merge. New investigations will queue here when fixes are proposed."
            />
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-sentinel">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="text-muted-foreground">Incident</TableHead>
                  <TableHead className="text-muted-foreground">Service</TableHead>
                  <TableHead className="text-muted-foreground">PR</TableHead>
                  <TableHead className="text-muted-foreground">State</TableHead>
                  <TableHead className="text-muted-foreground">Gates</TableHead>
                  <TableHead className="text-muted-foreground">Queued</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {queue.map((q) => {
                  const passed = q.gates_passed?.length || 0;
                  const failed = q.gates_failed?.length || 0;
                  return (
                    <TableRow key={q.incident_id} className="border-zinc-800 align-top">
                      <TableCell className="font-mono text-xs text-cyan-400">
                        {shortId(q.incident_id)}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-zinc-200">
                        {q.service}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-amber-400">
                        {q.pr_repo}#{q.pr_number}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={q.state} />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          {passed > 0 && (
                            <span className="inline-flex items-center gap-0.5 rounded border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 font-mono text-[10px] text-emerald-400">
                              <Check className="size-2.5" />
                              {passed}
                            </span>
                          )}
                          {failed > 0 && (
                            <span className="inline-flex items-center gap-0.5 rounded border border-rose-500/30 bg-rose-500/10 px-1.5 py-0.5 font-mono text-[10px] text-rose-400">
                              <X className="size-2.5" />
                              {failed}
                            </span>
                          )}
                          {passed === 0 && failed === 0 && (
                            <span className="text-[10px] text-muted-foreground">—</span>
                          )}
                        </div>
                        {(q.gates_failed?.length || 0) > 0 && (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {q.gates_failed.map((g, i) => (
                              <span
                                key={i}
                                className="font-mono text-[9px] text-rose-400/80"
                              >
                                {g}
                              </span>
                            ))}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                        {timeAgo(q.queued_at)}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>
    </div>
  );
}

function StatCard({
  icon: Icon,
  label,
  value,
  accent,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string | number;
  accent: "emerald" | "rose" | "cyan" | "violet";
}) {
  const colors = {
    emerald: "text-emerald-400 border-emerald-500/20",
    rose: "text-rose-400 border-rose-500/20",
    cyan: "text-cyan-400 border-cyan-500/20",
    violet: "text-violet-400 border-violet-500/20",
  };
  return (
    <div className={cn("flex items-center gap-3 rounded-xl border bg-card p-4", colors[accent])}>
      <Icon className={cn("size-5", colors[accent].split(" ")[0])} />
      <div>
        <div className={cn("font-mono text-xl font-bold tabular-nums", colors[accent].split(" ")[0])}>
          {value}
        </div>
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
      </div>
    </div>
  );
}

function RateLimitGauge({
  used,
  max,
  remaining,
}: {
  used: number;
  max: number;
  remaining: number;
}) {
  const pct = max > 0 ? Math.min(100, (used / max) * 100) : 0;
  const color =
    pct >= 90 ? "#f43f5e" : pct >= 70 ? "#f59e0b" : "#10b981";
  return (
    <div className="flex h-32 flex-col items-center justify-center gap-3">
      <div className="relative flex size-28 items-center justify-center">
        <svg width="112" height="112" viewBox="0 0 112 112" className="-rotate-90">
          <circle cx="56" cy="56" r="46" fill="none" stroke="#27272a" strokeWidth="10" />
          <circle
            cx="56"
            cy="56"
            r="46"
            fill="none"
            stroke={color}
            strokeWidth="10"
            strokeLinecap="round"
            strokeDasharray={`${(pct / 100) * 289} 289`}
            style={{ transition: "stroke-dasharray 0.6s ease" }}
          />
        </svg>
        <div className="absolute flex flex-col items-center">
          <span className="font-mono text-xl font-bold tabular-nums text-zinc-50">
            {used}
            <span className="text-sm text-muted-foreground">/{max}</span>
          </span>
          <span className="text-[9px] uppercase tracking-wide text-muted-foreground">
            this hour
          </span>
        </div>
      </div>
      <span className="font-mono text-xs text-muted-foreground">
        {remaining} remaining
      </span>
    </div>
  );
}

function CooldownIndicator({
  cooldown,
}: {
  cooldown: AutoMergeState["cooldown"] | undefined;
}) {
  const active = cooldown?.active;
  return (
    <div className="flex h-32 flex-col items-center justify-center gap-3">
      <span
        className={cn(
          "flex size-14 items-center justify-center rounded-full border",
          active
            ? "border-amber-500/40 bg-amber-500/10"
            : "border-emerald-500/40 bg-emerald-500/10",
        )}
      >
        <Snowflake
          className={cn("size-6", active ? "text-amber-400" : "text-emerald-400")}
        />
      </span>
      <div className="text-center">
        <span
          className={cn(
            "font-mono text-sm font-bold uppercase tracking-wide",
            active ? "text-amber-400" : "text-emerald-400",
          )}
        >
          {active ? "Active" : "Clear"}
        </span>
        {cooldown?.until && active && (
          <p className="mt-0.5 text-[10px] text-muted-foreground">
            until {timeAgo(cooldown.until)}
          </p>
        )}
        {cooldown?.reason && active && (
          <p className="mt-1 max-w-[200px] truncate text-[10px] text-muted-foreground">
            {cooldown.reason}
          </p>
        )}
      </div>
    </div>
  );
}
