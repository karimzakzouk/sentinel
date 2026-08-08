"use client";

import { Fragment, useMemo, useState } from "react";
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
  timeAgo,
  timeMDHM,
  EmptyState,
  ErrorState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import type { Incident } from "@/lib/types";
import {
  ChevronDown,
  ChevronRight,
  AlertCircle,
  CheckCircle2,
  Clock,
  FlaskConical,
  Inbox,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { motion, AnimatePresence } from "framer-motion";

// v5 fabricated L1-L5 from incident status + regex on sandbox_summary. v1
// only shows what the API actually returns: incidents with a real
// sandbox_summary (mirrored from state.execution_result.sandbox_summary by
// the API enricher in handleIncidents). If there's no summary, we say so
// — we never make up level outcomes.

interface SandboxRun {
  id: string;
  incidentId: string;
  service: string;
  namespace: string;
  timestamp: string;
  verified: boolean;
  summary: string;
  prNumber: number | null;
  prUrl: string | null;
  branch: string | null;
  fixDescription: string;
  fixType: string;
  clusterSummary: string;
  agentTrace: Incident["state"]["agent_trace"];
}

function incidentToSandboxRun(inc: Incident): SandboxRun | null {
  // Only surface incidents that actually produced a sandbox run. The
  // signal is: sandbox_summary is non-empty (the API mirrors it from
  // state.execution_result.sandbox_summary). sandbox_verified is a
  // boolean mirror of state.execution_result.sandbox_verified.
  const summary = inc.sandbox_summary ?? "";
  if (!summary && !inc.sandbox_verified) return null;
  return {
    id: `SBR-${inc.incident_id.slice(0, 6).toUpperCase()}`,
    incidentId: inc.incident_id,
    service: inc.cluster?.service ?? "—",
    namespace: inc.cluster?.namespace ?? "—",
    timestamp: inc.updated_at,
    verified: !!inc.sandbox_verified,
    summary,
    prNumber: inc.pr_number ?? null,
    prUrl: inc.pr_url ?? null,
    branch: inc.pr_branch ?? null,
    fixDescription: inc.state?.fix_proposal?.description ?? "—",
    fixType: inc.fix_type ?? inc.state?.fix_proposal?.fix_type ?? "—",
    clusterSummary: inc.cluster?.summary ?? "—",
    agentTrace: inc.state?.agent_trace ?? [],
  };
}

function ExpandedRow({ run }: { run: SandboxRun }) {
  return (
    <motion.div
      initial={{ height: 0, opacity: 0 }}
      animate={{ height: "auto", opacity: 1 }}
      exit={{ height: 0, opacity: 0 }}
      className="border-t border-zinc-800 bg-zinc-950/60"
    >
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <div className="space-y-3">
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Service</div>
            <Mono className="block text-[11px] text-cyan-400">{run.service}</Mono>
            <Mono className="block text-[11px] text-zinc-400">{run.namespace}</Mono>
          </div>
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Cluster Summary</div>
            <p className="text-[11px] leading-relaxed text-zinc-400">{run.clusterSummary}</p>
          </div>
          <div
            className={
              run.verified
                ? "rounded-md border border-emerald-500/30 bg-emerald-500/5 p-2"
                : "rounded-md border border-amber-500/30 bg-amber-500/5 p-2"
            }
          >
            <div
              className={
                run.verified
                  ? "mb-0.5 flex items-center gap-1 text-[10px] uppercase tracking-wider text-emerald-400"
                  : "mb-0.5 flex items-center gap-1 text-[10px] uppercase tracking-wider text-amber-400"
              }
            >
              {run.verified ? <CheckCircle2 className="h-3 w-3" /> : <AlertCircle className="h-3 w-3" />}
              Sandbox Verification
            </div>
            <p className="whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-zinc-300">
              {run.summary || (run.verified ? "All checks passed." : "No detail available.")}
            </p>
          </div>
          {run.prUrl && (
            <a
              href={run.prUrl}
              target="_blank"
              rel="noreferrer"
              className="block rounded-md border border-cyan-500/30 bg-cyan-500/5 p-2 text-[11px] text-cyan-300 hover:bg-cyan-500/10"
            >
              PR #{run.prNumber} → {run.branch ?? "main"}
            </a>
          )}
        </div>

        <div className="lg:col-span-2">
          <div className="mb-2 text-[10px] uppercase tracking-wider text-zinc-500">
            Fix Proposal ({run.fixType})
          </div>
          <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
            <p className="text-[11px] leading-relaxed text-zinc-300">{run.fixDescription}</p>
          </div>

          {run.agentTrace.length > 0 && (
            <div className="mt-3">
              <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">
                Agent Trace ({run.agentTrace.length} events)
              </div>
              <div className="max-h-48 space-y-0.5 overflow-y-auto rounded-md border border-zinc-800 bg-zinc-950 p-2 scrollbar-thin">
                {run.agentTrace.slice(0, 12).map((entry, i) => {
                  const phase = typeof entry.phase === "string" ? entry.phase : "—";
                  const message = typeof entry.message === "string" ? entry.message : "";
                  return (
                    <div key={i} className="flex gap-2 font-mono text-[10.5px] leading-tight text-zinc-400">
                      <span className="text-zinc-700">[{phase}]</span>
                      <span className="flex-1">{message || JSON.stringify(entry).slice(0, 100)}</span>
                    </div>
                  );
                })}
                {run.agentTrace.length > 12 && (
                  <div className="pt-1 text-center text-[10px] text-zinc-600">
                    + {run.agentTrace.length - 12} more…
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </motion.div>
  );
}

export function SandboxSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const [expanded, setExpanded] = useState<string | null>(null);

  const runs = useMemo(() => {
    return incidents
      .map(incidentToSandboxRun)
      .filter((r): r is SandboxRun => r !== null)
      .sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
  }, [incidents]);

  const stats = useMemo(() => {
    const total = runs.length;
    const passed = runs.filter((r) => r.verified).length;
    const failed = runs.filter((r) => !r.verified).length;
    return { total, passed, failed };
  }, [runs]);

  if (loading && incidents.length === 0) {
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

  if (error && incidents.length === 0) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={() => fetchIncidents()} />
      </div>
    );
  }

  return (
    <div className="p-6">
      <div className="mb-3 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "Total Runs", value: stats.total, sub: stats.total > 0 ? "incidents with sandbox data" : "no runs yet", icon: Clock, color: "text-zinc-400" },
          { label: "Verified", value: stats.passed, sub: stats.total > 0 ? `${Math.round((stats.passed / stats.total) * 100)}% pass rate` : "—", icon: CheckCircle2, color: "text-emerald-400" },
          { label: "Failed", value: stats.failed, sub: stats.failed > 0 ? "see expanded rows" : "none", icon: AlertCircle, color: stats.failed > 0 ? "text-rose-400" : "text-zinc-400" },
          { label: "Pending Detail", value: runs.filter((r) => !r.summary).length, sub: "verified but no summary", icon: FlaskConical, color: "text-cyan-400" },
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

      {runs.length === 0 ? (
        <EmptyState
          title="No sandbox data yet"
          description="Sandbox runs are surfaced here when an incident has a sandbox_summary or sandbox_verified flag set by the API. v1 does not fabricate L1-L5 outcomes from incident status."
          icon={Inbox}
        />
      ) : (
        <Panel>
          <PanelHeader
            title="Recent Sandbox Runs"
            sub="Incidents with real sandbox data"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {runs.length} runs
              </Badge>
            }
          />
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 w-8 px-3" />
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Run ID</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Incident</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Verified</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">When</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((run) => {
                  const isOpen = expanded === run.id;
                  return (
                    <Fragment key={run.id}>
                      <TableRow
                        onClick={() => setExpanded(isOpen ? null : run.id)}
                        className="cursor-pointer border-zinc-800/60 hover:bg-zinc-800/30"
                      >
                        <TableCell className="px-3 py-2">
                          {isOpen ? <ChevronDown className="h-3.5 w-3.5 text-zinc-400" /> : <ChevronRight className="h-3.5 w-3.5 text-zinc-500" />}
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-cyan-400">{run.id}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-300">{run.incidentId.slice(0, 8)}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2 text-xs text-zinc-300">{run.service}</TableCell>
                        <TableCell className="px-3 py-2">
                          <span
                            className={cn(
                              "inline-flex h-5 min-w-[2.25rem] items-center justify-center rounded border px-1 font-mono text-[10px] font-semibold",
                              run.verified
                                ? "border-emerald-500/30 bg-emerald-500/15 text-emerald-400"
                                : "border-rose-500/30 bg-rose-500/15 text-rose-400",
                            )}
                          >
                            {run.verified ? "PASS" : "FAIL"}
                          </span>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          {run.prNumber ? (
                            <Mono className="text-[11px] text-cyan-400">#{run.prNumber}</Mono>
                          ) : (
                            <span className="text-[11px] text-zinc-600">—</span>
                          )}
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-400">{timeMDHM(run.timestamp)}</Mono>
                          <div className="text-[10px] text-zinc-600">{timeAgo(run.timestamp)}</div>
                        </TableCell>
                      </TableRow>
                      {isOpen && (
                        <TableRow className="border-zinc-800/60">
                          <TableCell colSpan={7} className="p-0">
                            <AnimatePresence>
                              <ExpandedRow run={run} />
                            </AnimatePresence>
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        </Panel>
      )}
    </div>
  );
}
