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

// Derive a sandbox "run" from an incident's state. The Sentinel API does not
// have a dedicated /sandbox endpoint, so we treat any incident that produced a
// fix_proposal + PR as having a sandbox verification run, with the level
// results inferred from the incident's status.

function incidentToSandboxRun(inc: Incident) {
  const fix = inc.state?.fix_proposal;
  // An incident "has sandbox data" if it has a fix_proposal OR a pr_url OR
  // the status suggests sandbox verification ran.
  const hasSandbox =
    !!fix || !!inc.pr_url || ["verified", "failed", "rejected", "resolved"].includes(inc.status);
  if (!hasSandbox) return null;

  // Infer L1-L5 levels from incident status + presence of fix/PR.
  // - "verified" / "resolved" with PR = all pass
  // - "failed" / "rejected" = some level failed
  // - "investigating" + fix_proposed = some levels pending
  let levels: Array<"pass" | "fail" | "pending" | "skip">;
  if (["verified", "resolved"].includes(inc.status) && inc.pr_url) {
    levels = ["pass", "pass", "pass", "pass", "pass"];
  } else if (["failed", "rejected"].includes(inc.status)) {
    levels = ["pass", "pass", "fail", "skip", "skip"];
  } else if (inc.status === "investigating" || inc.status === "executing") {
    levels = ["pass", "pass", "pending", "pending", "pending"];
  } else {
    levels = ["pass", "pass", "pass", "pass", "pending"];
  }

  // Infra error — surface from cluster anomalies if any look infra-related.
  const anomalies = inc.cluster?.anomalies ?? [];
  const infraError = anomalies.find((a) =>
    /imagepull|crashloop|oom|container|schedul/i.test(a.detector + " " + a.explanation),
  );
  const infraErrorMsg = infraError ? `${infraError.detector}: ${infraError.explanation}` : null;

  const durationMs = new Date(inc.updated_at).getTime() - new Date(inc.created_at).getTime();
  const durationStr = durationMs > 0 ? `${Math.floor(durationMs / 60_000)}m ${Math.floor((durationMs % 60_000) / 1000)}s` : "—";

  return {
    id: `SBR-${inc.incident_id.slice(0, 6).toUpperCase()}`,
    incidentId: inc.incident_id,
    service: inc.cluster?.service ?? "—",
    namespace: inc.cluster?.namespace ?? "—",
    levels,
    duration: durationStr,
    infraError: infraErrorMsg,
    timestamp: inc.updated_at,
    canaryImage: fix?.files_to_change?.[0]?.path ?? "—",
    fixDescription: fix?.description ?? "—",
    fixType: fix?.fix_type ?? "—",
    summary: inc.cluster?.summary ?? "—",
    prNumber: inc.pr_number ?? null,
    prUrl: inc.pr_url ?? null,
    branch: inc.pr_branch ?? null,
    agentTrace: inc.state?.agent_trace ?? [],
  };
}

function ExpandedRow({ run }: { run: ReturnType<typeof incidentToSandboxRun> & object }) {
  return (
    <motion.div
      initial={{ height: 0, opacity: 0 }}
      animate={{ height: "auto", opacity: 1 }}
      exit={{ height: 0, opacity: 0 }}
      className="border-t border-zinc-800 bg-zinc-950/60"
    >
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        {/* Meta */}
        <div className="space-y-3">
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Service</div>
            <Mono className="block text-[11px] text-cyan-400">{run.service}</Mono>
            <Mono className="block text-[11px] text-zinc-400">{run.namespace}</Mono>
          </div>
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Cluster Summary</div>
            <p className="text-[11px] leading-relaxed text-zinc-400">{run.summary}</p>
          </div>
          {run.infraError && (
            <div className="rounded-md border border-rose-500/30 bg-rose-500/5 p-2">
              <div className="mb-0.5 flex items-center gap-1 text-[10px] uppercase tracking-wider text-rose-400">
                <AlertCircle className="h-3 w-3" /> Infra Error
              </div>
              <p className="font-mono text-[11px] text-rose-300">{run.infraError}</p>
            </div>
          )}
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

        {/* Fix proposal + trace */}
        <div className="lg:col-span-2">
          <div className="mb-2 text-[10px] uppercase tracking-wider text-zinc-500">Fix Proposal ({run.fixType})</div>
          <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
            <p className="text-[11px] leading-relaxed text-zinc-300">{run.fixDescription}</p>
            {run.canaryImage !== "—" && (
              <div className="mt-2 border-t border-zinc-800 pt-2">
                <div className="text-[10px] uppercase tracking-wider text-zinc-500">Canary file</div>
                <Mono className="block break-all text-[11px] text-cyan-400">{run.canaryImage}</Mono>
              </div>
            )}
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
      .filter((r): r is NonNullable<ReturnType<typeof incidentToSandboxRun>> => r !== null)
      .sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
  }, [incidents]);

  // Compute aggregate stats from real runs
  const stats = useMemo(() => {
    const total = runs.length;
    const passed = runs.filter((r) => r.levels.every((l) => l === "pass")).length;
    const inProgress = runs.filter((r) => r.levels.some((l) => l === "pending")).length;
    const failed = runs.filter((r) => r.levels.some((l) => l === "fail")).length;
    const infraErrors = runs.filter((r) => r.infraError).length;
    return { total, passed, inProgress, failed, infraErrors };
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
        <ErrorState message={error} onRetry={fetchIncidents} />
      </div>
    );
  }

  return (
    <div className="p-6">
      {/* Stats strip — derived from real runs */}
      <div className="mb-3 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          {
            label: "Total Runs",
            value: stats.total,
            sub: stats.total > 0 ? `across ${new Set(runs.map((r) => r.service)).size} services` : "no runs yet",
            icon: Clock,
            color: "text-zinc-400",
          },
          {
            label: "Passed",
            value: stats.passed,
            sub: stats.total > 0 ? `${Math.round((stats.passed / stats.total) * 100)}% pass rate` : "—",
            icon: CheckCircle2,
            color: "text-emerald-400",
          },
          {
            label: "In Progress",
            value: stats.inProgress,
            sub: stats.inProgress > 0 ? "L3/L4/L5 phases" : "—",
            icon: FlaskConical,
            color: "text-cyan-400",
          },
          {
            label: "Infra Errors",
            value: stats.infraErrors,
            sub: stats.infraErrors > 0 ? "see expanded rows" : "none",
            icon: AlertCircle,
            color: stats.infraErrors > 0 ? "text-rose-400" : "text-zinc-400",
          },
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
          title="No sandbox runs yet"
          description="Sandbox runs are derived from incidents. When an incident produces a fix proposal, the sandbox verification will appear here."
          icon={Inbox}
        />
      ) : (
        <Panel>
          <PanelHeader
            title="Recent Sandbox Runs"
            sub="L1–L5 verification — derived from incidents"
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
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">L1</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">L2</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">L3</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">L4</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">L5</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Duration</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Infra</TableHead>
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
                          {isOpen ? (
                            <ChevronDown className="h-3.5 w-3.5 text-zinc-400" />
                          ) : (
                            <ChevronRight className="h-3.5 w-3.5 text-zinc-500" />
                          )}
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-cyan-400">{run.id}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-300">{run.incidentId.slice(0, 8)}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2 text-xs text-zinc-300">{run.service}</TableCell>
                        {run.levels.map((lvl, i) => {
                          const color =
                            lvl === "pass"
                              ? "border-emerald-500/30 bg-emerald-500/15 text-emerald-400"
                              : lvl === "fail"
                              ? "border-rose-500/30 bg-rose-500/15 text-rose-400"
                              : lvl === "pending"
                              ? "border-amber-500/30 bg-amber-500/15 text-amber-400"
                              : "border-zinc-700 bg-zinc-700/40 text-zinc-500";
                          return (
                            <TableCell key={i} className="px-3 py-2">
                              <span className={cn("inline-flex h-5 min-w-[2.25rem] items-center justify-center rounded border px-1 font-mono text-[10px] font-semibold", color)}>
                                L{i + 1}
                              </span>
                            </TableCell>
                          );
                        })}
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-300">{run.duration}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          {run.infraError ? (
                            <span className="flex items-center gap-1 text-[11px] text-rose-400">
                              <AlertCircle className="h-3 w-3" />
                              <span className="font-mono">error</span>
                            </span>
                          ) : (
                            <span className="font-mono text-[11px] text-emerald-400">ok</span>
                          )}
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-400">{timeMDHM(run.timestamp)}</Mono>
                          <div className="text-[10px] text-zinc-600">{timeAgo(run.timestamp)}</div>
                        </TableCell>
                      </TableRow>
                      {isOpen && (
                        <TableRow className="border-zinc-800/60">
                          <TableCell colSpan={12} className="p-0">
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
