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
  GitPullRequest,
  GitMerge,
  GitBranch,
  CheckCircle2,
  XCircle,
  Inbox,
  ExternalLink,
} from "lucide-react";
import { motion, AnimatePresence } from "framer-motion";
import { cn } from "@/lib/utils";

function incidentToPR(inc: Incident) {
  if (!inc.pr_number) return null;
  // Infer PR status from incident status
  let status: "open" | "merged" | "closed" | "pending";
  let autoMerge: "merged" | "queued" | "blocked" | "reverted" | "skipped";
  if (["verified", "resolved"].includes(inc.status)) {
    status = "merged";
    autoMerge = "merged";
  } else if (["failed", "rejected"].includes(inc.status)) {
    status = "closed";
    autoMerge = inc.auto_merge_status === "reverted" ? "reverted" : "blocked";
  } else if (inc.status === "executing" || inc.status === "approved") {
    status = "open";
    autoMerge = "queued";
  } else {
    status = "open";
    autoMerge = "queued";
  }

  return {
    pr: inc.pr_number,
    repo: inc.pr_repo ?? "—",
    branch: inc.pr_branch ?? "—",
    url: inc.pr_url ?? null,
    title: inc.state?.fix_proposal?.description ?? inc.cluster?.summary ?? "—",
    status,
    autoMerge,
    autoMergeStatus: inc.auto_merge_status ?? null,
    opened: inc.created_at,
    merged: ["verified", "resolved"].includes(inc.status) ? inc.updated_at : null,
    incidentId: inc.incident_id,
    service: inc.cluster?.service ?? "—",
    fixType: inc.state?.fix_proposal?.fix_type ?? "—",
    confidence: inc.state?.root_cause?.confidence ?? 0,
  };
}

const prStatusStyles: Record<string, string> = {
  open: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30",
  merged: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
  closed: "bg-zinc-700/40 text-zinc-400 border-zinc-700",
  pending: "bg-amber-500/15 text-amber-400 border-amber-500/30",
};

const autoMergeStyles: Record<string, string> = {
  merged: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
  queued: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30",
  blocked: "bg-amber-500/15 text-amber-400 border-amber-500/30",
  reverted: "bg-rose-500/15 text-rose-400 border-rose-500/30",
  skipped: "bg-zinc-700/40 text-zinc-400 border-zinc-700",
};

function GateList({ pr }: { pr: ReturnType<typeof incidentToPR> & object }) {
  // Gates aren't returned by the API directly; surface what we can infer from
  // the incident: sandbox verification (pass/fail), confidence (≥85 = pass),
  // PR existence. We present these as "inferred gates" — clearly labeled.
  const gates = [
    {
      name: "fix_proposal_exists",
      status: "pass" as const,
    },
    {
      name: "confidence_threshold",
      status: pr.confidence >= 0.85 ? ("pass" as const) : ("fail" as const),
    },
    {
      name: "sandbox_l5",
      status: ["verified", "resolved"].includes(pr.status) ? ("pass" as const) : ("pending" as const),
    },
    {
      name: "auto_merge_enabled",
      status: pr.autoMerge === "merged" ? ("pass" as const) : pr.autoMerge === "blocked" ? ("fail" as const) : ("pending" as const),
    },
  ];

  const passed = gates.filter((g) => g.status === "pass").length;
  const failed = gates.filter((g) => g.status === "fail").length;
  const pending = gates.filter((g) => g.status === "pending").length;

  return (
    <motion.div
      initial={{ height: 0, opacity: 0 }}
      animate={{ height: "auto", opacity: 1 }}
      exit={{ height: 0, opacity: 0 }}
      className="border-t border-zinc-800 bg-zinc-950/60"
    >
      <div className="p-4">
        <div className="mb-3 flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
            Inferred Gate Status
          </div>
          <div className="flex items-center gap-2 font-mono text-[10px]">
            <span className="text-emerald-400">{passed} pass</span>
            <span className="text-rose-400">{failed} fail</span>
            <span className="text-amber-400">{pending} pending</span>
            <span className="text-zinc-500">/ {gates.length}</span>
          </div>
        </div>
        <div className="grid grid-cols-1 gap-1.5 md:grid-cols-2">
          {gates.map((g, i) => (
            <div
              key={i}
              className="flex items-center justify-between rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5"
            >
              <div className="flex items-center gap-2">
                {g.status === "pass" && <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />}
                {g.status === "fail" && <XCircle className="h-3.5 w-3.5 text-rose-400" />}
                {g.status === "pending" && <span className="h-3.5 w-3.5 rounded-full border border-amber-400" />}
                <span className="font-mono text-[12px] text-zinc-300">{g.name}</span>
              </div>
              <span
                className={cn(
                  "font-mono text-[10px] uppercase",
                  g.status === "pass" && "text-emerald-400",
                  g.status === "fail" && "text-rose-400",
                  g.status === "pending" && "text-amber-400",
                )}
              >
                {g.status}
              </span>
            </div>
          ))}
        </div>

        <div className="mt-3 grid grid-cols-2 gap-3 border-t border-zinc-800 pt-3 text-[11px] sm:grid-cols-3">
          <div>
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Linked incident</div>
            <Mono className="text-[11px] text-cyan-400">{pr.incidentId.slice(0, 8)}</Mono>
          </div>
          <div>
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Service</div>
            <Mono className="text-[11px] text-zinc-300">{pr.service}</Mono>
          </div>
          <div>
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Fix type</div>
            <Mono className="text-[11px] text-zinc-300">{pr.fixType}</Mono>
          </div>
          <div>
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</div>
            <Mono className="text-[11px] text-zinc-300">{(pr.confidence * 100).toFixed(0)}%</Mono>
          </div>
          <div>
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Branch</div>
            <Mono className="text-[11px] text-zinc-300">{pr.branch}</Mono>
          </div>
          {pr.url && (
            <div>
              <div className="text-[10px] uppercase tracking-wider text-zinc-500">GitHub</div>
              <a
                href={pr.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-[11px] text-cyan-400 hover:text-cyan-300"
              >
                open <ExternalLink className="h-3 w-3" />
              </a>
            </div>
          )}
        </div>
      </div>
    </motion.div>
  );
}

export function PullRequestsSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const [expanded, setExpanded] = useState<number | null>(null);

  const prs = useMemo(() => {
    return incidents
      .map(incidentToPR)
      .filter((p): p is NonNullable<ReturnType<typeof incidentToPR>> => p !== null)
      .sort((a, b) => new Date(b.opened).getTime() - new Date(a.opened).getTime());
  }, [incidents]);

  const stats = useMemo(() => {
    const total = prs.length;
    const merged = prs.filter((p) => p.status === "merged").length;
    const open = prs.filter((p) => p.status === "open").length;
    const closed = prs.filter((p) => p.status === "closed").length;
    return { total, merged, open, closed };
  }, [prs]);

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
      {/* Stats — derived from incidents */}
      <div className="mb-3 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "Total PRs", value: stats.total, sub: "from incidents", icon: GitPullRequest, color: "text-cyan-400" },
          { label: "Merged", value: stats.merged, sub: "auto-merged", icon: GitMerge, color: "text-emerald-400" },
          { label: "Open", value: stats.open, sub: "queued or in review", icon: GitBranch, color: "text-cyan-400" },
          { label: "Closed", value: stats.closed, sub: "rejected or reverted", icon: XCircle, color: "text-rose-400" },
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

      {prs.length === 0 ? (
        <EmptyState
          title="No pull requests yet"
          description="Sentinel opens PRs after an incident produces a fix proposal that passes sandbox verification. PRs will appear here automatically."
          icon={Inbox}
        />
      ) : (
        <Panel>
          <PanelHeader
            title="Pull Requests"
            sub="Sentinel-generated PRs (derived from incidents)"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {prs.length} PRs
              </Badge>
            }
          />
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 w-8 px-3" />
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Repo</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Title</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Auto-Merge</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Confidence</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Opened</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Merged</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {prs.map((pr) => {
                  const isOpen = expanded === pr.pr;
                  return (
                    <Fragment key={pr.pr}>
                      <TableRow
                        onClick={() => setExpanded(isOpen ? null : pr.pr)}
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
                          <div className="flex items-center gap-1.5">
                            <GitPullRequest className="h-3.5 w-3.5 text-zinc-500" />
                            <Mono className="text-[11px] text-cyan-400">#{pr.pr}</Mono>
                          </div>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-300">{pr.repo}</Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2 max-w-[280px]">
                          <span className="block truncate text-[12px] text-zinc-200">{pr.title}</span>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", prStatusStyles[pr.status])}>
                            {pr.status}
                          </Badge>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", autoMergeStyles[pr.autoMerge])}>
                            {pr.autoMerge}
                          </Badge>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className={cn("text-[11px]", pr.confidence >= 0.85 ? "text-emerald-400" : "text-zinc-400")}>
                            {(pr.confidence * 100).toFixed(0)}%
                          </Mono>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          <Mono className="text-[11px] text-zinc-400">{timeMDHM(pr.opened)}</Mono>
                          <div className="text-[10px] text-zinc-600">{timeAgo(pr.opened)}</div>
                        </TableCell>
                        <TableCell className="px-3 py-2">
                          {pr.merged ? (
                            <>
                              <Mono className="text-[11px] text-emerald-400">{timeMDHM(pr.merged)}</Mono>
                              <div className="text-[10px] text-zinc-600">{timeAgo(pr.merged)}</div>
                            </>
                          ) : (
                            <span className="text-[11px] text-zinc-600">—</span>
                          )}
                        </TableCell>
                      </TableRow>
                      {isOpen && (
                        <TableRow className="border-zinc-800/60">
                          <TableCell colSpan={9} className="p-0">
                            <AnimatePresence>
                              <GateList pr={pr} />
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
