"use client";

import { Fragment, useState } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Panel, PanelHeader, Mono, timeAgo, timeMDHM } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { pullRequests, type PullRequest } from "@/lib/mock-data";
import {
  ChevronDown,
  ChevronRight,
  GitPullRequest,
  GitMerge,
  GitBranch,
  CheckCircle2,
  XCircle,
  CircleDashed,
  Shield,
} from "lucide-react";
import { motion, AnimatePresence } from "framer-motion";
import { cn } from "@/lib/utils";

const prStatusStyles: Record<PullRequest["status"], string> = {
  open: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30",
  merged: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
  closed: "bg-zinc-700/40 text-zinc-400 border-zinc-700",
  pending: "bg-amber-500/15 text-amber-400 border-amber-500/30",
};

const autoMergeStyles: Record<PullRequest["autoMerge"], string> = {
  merged: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
  queued: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30",
  blocked: "bg-amber-500/15 text-amber-400 border-amber-500/30",
  reverted: "bg-rose-500/15 text-rose-400 border-rose-500/30",
  skipped: "bg-zinc-700/40 text-zinc-400 border-zinc-700",
};

function GateList({ pr }: { pr: PullRequest }) {
  const passed = pr.gates.filter((g) => g.status === "pass").length;
  const failed = pr.gates.filter((g) => g.status === "fail").length;
  const pending = pr.gates.filter((g) => g.status === "pending").length;

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
            <Shield className="h-3 w-3" /> Auto-Merge Gate Breakdown
          </div>
          <div className="flex items-center gap-2 font-mono text-[10px]">
            <span className="text-emerald-400">{passed} pass</span>
            <span className="text-rose-400">{failed} fail</span>
            <span className="text-amber-400">{pending} pending</span>
            <span className="text-zinc-500">/ {pr.gates.length}</span>
          </div>
        </div>
        <div className="grid grid-cols-1 gap-1.5 md:grid-cols-2">
          {pr.gates.map((g, i) => (
            <div
              key={i}
              className="flex items-center justify-between rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5"
            >
              <div className="flex items-center gap-2">
                {g.status === "pass" && <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />}
                {g.status === "fail" && <XCircle className="h-3.5 w-3.5 text-rose-400" />}
                {g.status === "pending" && <CircleDashed className="h-3.5 w-3.5 text-amber-400" />}
                <span className="text-[12px] text-zinc-300">{g.name}</span>
              </div>
              <span
                className={cn(
                  "font-mono text-[10px] uppercase",
                  g.status === "pass" && "text-emerald-400",
                  g.status === "fail" && "text-rose-400",
                  g.status === "pending" && "text-amber-400"
                )}
              >
                {g.status}
              </span>
            </div>
          ))}
        </div>

        {pr.incidentId && (
          <div className="mt-3 flex items-center gap-2 border-t border-zinc-800 pt-3">
            <span className="text-[10px] uppercase tracking-wider text-zinc-500">Linked incident</span>
            <Mono className="text-[11px] text-cyan-400">{pr.incidentId}</Mono>
          </div>
        )}
      </div>
    </motion.div>
  );
}

export function PullRequestsSection() {
  const [expanded, setExpanded] = useState<number | null>(4825);

  return (
    <div className="p-6">
      {/* Stats */}
      <div className="mb-3 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "Total PRs", value: "7", sub: "last 24h", icon: GitPullRequest, color: "text-cyan-400" },
          { label: "Merged", value: "5", sub: "auto-merge: 5", icon: GitMerge, color: "text-emerald-400" },
          { label: "Open", value: "1", sub: "queued for merge", icon: GitBranch, color: "text-cyan-400" },
          { label: "Closed", value: "1", sub: "tests failing", icon: XCircle, color: "text-rose-400" },
        ].map((s) => {
          const Icon = s.icon;
          return (
            <Panel key={s.label}>
              <div className="px-4 py-3">
                <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
                  <Icon className={`h-3 w-3 ${s.color}`} /> {s.label}
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
          title="Pull Requests"
          sub="Sentinel-generated PRs and auto-merge gate status"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {pullRequests.length} PRs
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
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Sandbox</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Auto-Merge</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Gates</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Opened</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Merged</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pullRequests.map((pr) => {
                const isOpen = expanded === pr.pr;
                const passed = pr.gates.filter((g) => g.status === "pass").length;
                const total = pr.gates.length;
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
                        {pr.sandboxVerified ? (
                          <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                            <CheckCircle2 className="h-3 w-3" /> L1–L5
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-500">
                            <CircleDashed className="h-3 w-3" /> pending
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", autoMergeStyles[pr.autoMerge])}>
                          {pr.autoMerge}
                        </Badge>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <div className="flex items-center gap-1.5">
                          <div className="flex gap-0.5">
                            {Array.from({ length: 11 }).map((_, i) => (
                              <span
                                key={i}
                                className={cn(
                                  "h-2.5 w-1 rounded-sm",
                                  i < passed ? "bg-emerald-500" : i < total - pr.gates.filter((g) => g.status === "pending").length ? "bg-rose-500" : "bg-zinc-700"
                                )}
                              />
                            ))}
                          </div>
                          <Mono className="text-[10px] text-zinc-500">{passed}/{total}</Mono>
                        </div>
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
                        <TableCell colSpan={10} className="p-0">
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
    </div>
  );
}
