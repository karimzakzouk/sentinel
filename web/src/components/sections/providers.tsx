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
import { Panel, PanelHeader, Mono, EmptyState, ErrorState } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import { Cpu, Zap, AlertTriangle, Inbox } from "lucide-react";
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

function providerStatus(p: { tripped: boolean }): string {
  return p.tripped ? "tripped" : "healthy";
}

export function ProvidersSection() {
  const providers = useSentinelStore((s) => s.providers);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const refreshProviders = useSentinelStore((s) => s.refreshProviders);

  const stats = useMemo(() => {
    const total = providers.length;
    const healthy = providers.filter((p) => !p.tripped).length;
    const tripped = providers.filter((p) => p.tripped).length;
    const totalCalls = providers.reduce((a, p) => a + p.totalCalls, 0);
    const totalFailures = providers.reduce((a, p) => a + p.totalFailures, 0);
    return { total, healthy, tripped, totalCalls, totalFailures };
  }, [providers]);

  if (loading && providers.length === 0) {
    return (
      <div className="space-y-3 p-6">
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="h-20 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
          ))}
        </div>
        <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
      </div>
    );
  }

  if (error && providers.length === 0) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={refreshProviders} />
      </div>
    );
  }

  if (providers.length === 0) {
    return (
      <div className="p-6">
        <EmptyState
          title="No providers configured"
          description="The /providers endpoint returned an empty list. Sentinel is running in single-provider mode, or the multi-provider pool isn't configured."
          icon={Inbox}
        />
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "Total", value: stats.total, sub: "in pool", icon: Cpu, color: "text-zinc-400" },
          { label: "Healthy", value: stats.healthy, sub: "circuit closed", icon: Zap, color: "text-emerald-400" },
          { label: "Tripped", value: stats.tripped, sub: "circuit open", icon: AlertTriangle, color: stats.tripped > 0 ? "text-rose-400" : "text-zinc-400" },
          { label: "Total Calls", value: stats.totalCalls, sub: `${stats.totalFailures} failures`, icon: Zap, color: "text-cyan-400" },
        ].map((s) => {
          const Icon = s.icon;
          return (
            <Panel key={s.label}>
              <div className="px-4 py-3">
                <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
                  <Icon className={cn("h-3 w-3", s.color)} /> {s.label}
                </div>
                <Mono className="mt-1 block text-xl text-zinc-100">{s.value}</Mono>
                <div className="text-[10px] text-zinc-500">{s.sub}</div>
              </div>
            </Panel>
          );
        })}
      </div>

      <Panel>
        <PanelHeader
          title="Provider Pool"
          sub="Live circuit breaker states from /providers"
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
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">ID</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Provider</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Priority</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Calls</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Failures</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Last Failure</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((p) => {
                const status = providerStatus(p);
                return (
                  <TableRow key={p.id} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-cyan-400">{p.id}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-xs text-zinc-300">{p.provider}</TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{p.priority}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <div className="flex items-center gap-1.5">
                        <span className={cn("h-1.5 w-1.5 rounded-full", statusDot[status] ?? "bg-zinc-500")} />
                        <span className={cn("font-mono text-[11px]", statusText[status] ?? "text-zinc-400")}>{status}</span>
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-300">{p.totalCalls}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className={cn("text-[11px]", p.totalFailures > 0 ? "text-rose-400" : "text-zinc-400")}>
                        {p.totalFailures}
                      </Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      {p.lastFailureReason ? (
                        <span className="text-[11px] text-zinc-400" title={p.lastFailureReason}>
                          {p.lastFailureReason.slice(0, 60)}
                          {p.lastFailureReason.length > 60 ? "…" : ""}
                        </span>
                      ) : (
                        <span className="text-[11px] text-zinc-600">—</span>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      </Panel>

      <div className="flex justify-end">
        <button onClick={() => refreshProviders()} className="text-[11px] text-cyan-400 hover:text-cyan-300">
          Refresh →
        </button>
      </div>
    </div>
  );
}
