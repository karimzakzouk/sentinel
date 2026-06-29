"use client";

import { Server, Zap, AlertTriangle, Snowflake, Activity } from "lucide-react";
import { listProviders } from "@/lib/api";
import type { Provider } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import { timeAgo } from "@/lib/utils";
import {
  Panel,
  Skeleton,
  ErrorState,
  EmptyState,
  SectionHeader,
} from "@/components/ui-helpers";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const STATUS_META: Record<
  string,
  { color: string; ring: string; label: string; icon: React.ComponentType<{ className?: string }> }
> = {
  healthy: { color: "text-emerald-400", ring: "bg-emerald-500", label: "Healthy", icon: Zap },
  tripped: { color: "text-rose-400", ring: "bg-rose-500", label: "Tripped", icon: AlertTriangle },
  cooling: { color: "text-amber-400", ring: "bg-amber-500", label: "Cooling", icon: Snowflake },
};

export function Providers({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();
  const { data, loading, error, refetch } = useFetch<Provider[]>(
    () => listProviders(),
    [refreshKey, events.length],
    { intervalMs: 30000 },
  );

  const summary = (data || []).reduce(
    (acc, p) => {
      const k = (p.status || "unknown").toLowerCase();
      acc[k] = (acc[k] || 0) + 1;
      return acc;
    },
    {} as Record<string, number>,
  );

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="LLM Providers"
        description="Circuit-breaker pool with priority fallback"
      />

      {/* Circuit-breaker summary */}
      <div className="grid grid-cols-3 gap-3">
        {(["healthy", "tripped", "cooling"] as const).map((s) => {
          const meta = STATUS_META[s];
          const Icon = meta.icon;
          const count = summary[s] || 0;
          return (
            <div
              key={s}
              className="flex items-center gap-3 rounded-xl border border-zinc-800 bg-card p-4"
            >
              <div className="relative flex size-10 items-center justify-center">
                <span
                  className={`absolute inline-flex size-10 rounded-full opacity-20 ${meta.ring}`}
                />
                <span className={`relative size-2.5 rounded-full ${meta.ring}`} />
              </div>
              <div className="flex flex-col">
                <span className="font-mono text-2xl font-bold tabular-nums text-zinc-50">
                  {count}
                </span>
                <span className={`flex items-center gap-1 text-[11px] uppercase tracking-wide ${meta.color}`}>
                  <Icon className="size-3" />
                  {meta.label}
                </span>
              </div>
            </div>
          );
        })}
      </div>

      <Panel bodyClassName="p-0">
        {loading && !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : error ? (
          <div className="p-4">
            <ErrorState message={error} onRetry={refetch} />
          </div>
        ) : !data || data.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={Server}
              title="No providers configured"
              description="Configure LLM providers in Settings to populate the circuit-breaker pool."
            />
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-sentinel">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="text-muted-foreground">Status</TableHead>
                  <TableHead className="text-muted-foreground">Name</TableHead>
                  <TableHead className="text-muted-foreground">Priority</TableHead>
                  <TableHead className="text-muted-foreground">Model</TableHead>
                  <TableHead className="text-muted-foreground">Trips</TableHead>
                  <TableHead className="text-muted-foreground">Last Trip</TableHead>
                  <TableHead className="text-muted-foreground">Reason</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data
                  .slice()
                  .sort((a, b) => (a.priority || 0) - (b.priority || 0))
                  .map((p) => {
                    const status = (p.status || "unknown").toLowerCase();
                    const meta = STATUS_META[status] || STATUS_META.healthy;
                    return (
                      <TableRow key={p.name} className="border-zinc-800">
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <span className={`size-2.5 rounded-full ${meta.ring}`} />
                            <span className={`text-xs font-medium ${meta.color}`}>
                              {meta.label}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="font-mono text-xs text-zinc-100">
                          {p.name}
                        </TableCell>
                        <TableCell>
                          <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-300">
                            P{p.priority}
                          </span>
                        </TableCell>
                        <TableCell className="font-mono text-xs text-cyan-400">
                          {p.model || "—"}
                          {p.fast_model && p.fast_model !== p.model && (
                            <span className="ml-1 text-[10px] text-muted-foreground">
                              / {p.fast_model}
                            </span>
                          )}
                        </TableCell>
                        <TableCell className="font-mono text-xs tabular-nums text-zinc-300">
                          {p.trips_total ?? 0}
                        </TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {p.last_trip ? timeAgo(p.last_trip) : "—"}
                        </TableCell>
                        <TableCell className="max-w-xs truncate text-xs text-muted-foreground">
                          {p.last_trip_reason || "—"}
                        </TableCell>
                      </TableRow>
                    );
                  })}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>

      {/* Circuit-breaker legend */}
      <Panel title="Circuit-Breaker States" description="how Sentinel routes LLM calls">
        <div className="grid gap-3 sm:grid-cols-3">
          <CBState
            color="bg-emerald-500"
            title="Healthy (Green)"
            desc="Accepting requests. Calls route to this provider by priority."
          />
          <CBState
            color="bg-amber-500"
            title="Cooling (Yellow)"
            desc="Tripped recently; in cooldown window. Skipped until timer expires."
          />
          <CBState
            color="bg-rose-500"
            title="Tripped (Red)"
            desc="Actively failing. Circuit open; requests fall through to next provider."
          />
        </div>
      </Panel>
    </div>
  );
}

function CBState({
  color,
  title,
  desc,
}: {
  color: string;
  title: string;
  desc: string;
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
      <span className={`mt-1 size-2.5 shrink-0 rounded-full ${color}`} />
      <div>
        <p className="text-xs font-semibold text-zinc-100">{title}</p>
        <p className="mt-0.5 text-xs text-muted-foreground">{desc}</p>
      </div>
    </div>
  );
}
