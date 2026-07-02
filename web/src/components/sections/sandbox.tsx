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
import { Panel, PanelHeader, LevelBadge, Mono, timeAgo, timeMDHM } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { sandboxRuns, type SandboxRun } from "@/lib/mock-data";
import { ChevronDown, ChevronRight, AlertCircle, RefreshCw, CheckCircle2, XCircle, Clock } from "lucide-react";
import { cn } from "@/lib/utils";
import { motion, AnimatePresence } from "framer-motion";

function ExpandedRow({ run }: { run: SandboxRun }) {
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
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Canary Image</div>
            <Mono className="block break-all text-[11px] text-cyan-400">{run.canaryImage}</Mono>
          </div>
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Namespace</div>
            <Mono className="block text-[11px] text-zinc-300">{run.namespace}</Mono>
            <div className="mt-0.5 text-[10px] text-zinc-500">age: {run.namespaceAge}</div>
          </div>
          {run.infraError && (
            <div className="rounded-md border border-rose-500/30 bg-rose-500/5 p-2">
              <div className="mb-0.5 flex items-center gap-1 text-[10px] uppercase tracking-wider text-rose-400">
                <AlertCircle className="h-3 w-3" /> Infra Error
              </div>
              <p className="font-mono text-[11px] text-rose-300">{run.infraError}</p>
            </div>
          )}
          {run.retries.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1 text-[10px] uppercase tracking-wider text-zinc-500">
                <RefreshCw className="h-3 w-3" /> Retry History ({run.retries.length})
              </div>
              <div className="space-y-1">
                {run.retries.map((r, i) => (
                  <div key={i} className="rounded border border-zinc-800 bg-zinc-950 p-2">
                    <div className="flex items-center justify-between">
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                        attempt {r.attempt}
                      </Badge>
                      <Mono className="text-[10px] text-zinc-600">{timeAgo(r.time)}</Mono>
                    </div>
                    <p className="mt-1 font-mono text-[10.5px] leading-relaxed text-zinc-400">{r.feedback}</p>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* Level details */}
        <div className="lg:col-span-2">
          <div className="mb-2 text-[10px] uppercase tracking-wider text-zinc-500">Per-Level Details</div>
          <div className="space-y-2">
            {run.levelDetails.map((lvl) => (
              <div key={lvl.level} className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
                <div className="mb-2 flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <LevelBadge status={lvl.status} level={lvl.level} />
                    <span className="text-[12px] font-medium text-zinc-200">{lvl.name}</span>
                  </div>
                  <span className="font-mono text-[10px] text-zinc-500">{lvl.testOutput}</span>
                </div>
                <div className="grid grid-cols-2 gap-2">
                  <div>
                    <div className="mb-1 text-[9px] uppercase tracking-wider text-zinc-600">Pod Logs</div>
                    <div className="space-y-0.5">
                      {lvl.podLogs.length === 0 ? (
                        <div className="font-mono text-[10px] text-zinc-700">— no output —</div>
                      ) : (
                        lvl.podLogs.map((log, i) => (
                          <div key={i} className="flex gap-1.5 font-mono text-[10px] leading-tight text-zinc-400">
                            <span className="text-zinc-700">$</span>
                            <span className={cn(log.includes("✓") && "text-emerald-300", log.includes("PASS") && "text-emerald-300")}>{log}</span>
                          </div>
                        ))
                      )}
                    </div>
                  </div>
                  <div>
                    <div className="mb-1 text-[9px] uppercase tracking-wider text-zinc-600">Probes</div>
                    {lvl.probeResults.length === 0 ? (
                      <div className="font-mono text-[10px] text-zinc-700">— none —</div>
                    ) : (
                      <div className="space-y-0.5">
                        {lvl.probeResults.map((p, i) => (
                          <div key={i} className="flex items-center gap-1.5 font-mono text-[10px]">
                            {p.status === "pass" ? (
                              <CheckCircle2 className="h-3 w-3 text-emerald-400" />
                            ) : (
                              <XCircle className="h-3 w-3 text-rose-400" />
                            )}
                            <span className="text-zinc-300">{p.name}</span>
                            <span className={cn("ml-auto", p.status === "pass" ? "text-emerald-400" : "text-rose-400")}>
                              {p.status}
                            </span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                </div>
                {lvl.reproducerOutput && (
                  <div className="mt-2 border-t border-zinc-800 pt-2">
                    <div className="mb-0.5 text-[9px] uppercase tracking-wider text-zinc-600">Reproducer Output</div>
                    <p className="font-mono text-[10.5px] text-amber-300">{lvl.reproducerOutput}</p>
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
    </motion.div>
  );
}

export function SandboxSection() {
  const [expanded, setExpanded] = useState<string | null>("SBR-7A3F");

  return (
    <div className="p-6">
      {/* Stats strip */}
      <div className="mb-3 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "Runs (24h)", value: "7", sub: "across 5 incidents", icon: Clock },
          { label: "Pass Rate", value: "71%", sub: "5/7 passed", icon: CheckCircle2 },
          { label: "In Progress", value: "2", sub: "L3, L4 phases", icon: RefreshCw },
          { label: "Infra Errors", value: "1", sub: "ImagePullBackOff", icon: AlertCircle },
        ].map((s) => {
          const Icon = s.icon;
          return (
            <Panel key={s.label}>
              <div className="px-4 py-3">
                <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
                  <Icon className="h-3 w-3" /> {s.label}
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
          title="Recent Sandbox Runs"
          sub="L1–L5 verification pipeline"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {sandboxRuns.length} runs
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
              {sandboxRuns.map((run) => {
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
                        <Mono className="text-[11px] text-zinc-300">{run.incidentId}</Mono>
                      </TableCell>
                      <TableCell className="px-3 py-2 text-xs text-zinc-300">{run.service}</TableCell>
                      {run.levels.map((lvl, i) => (
                        <TableCell key={i} className="px-3 py-2">
                          <LevelBadge status={lvl} level={i + 1} />
                        </TableCell>
                      ))}
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
                      <TableRow key={run.id + "-expand"} className="border-zinc-800/60">
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
    </div>
  );
}
