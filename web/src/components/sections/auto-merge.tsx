"use client";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Panel, PanelHeader, Mono, timeAgo, timeMDHM, PulseDot } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { autoMergeState } from "@/lib/mock-data";
import { useSentinel } from "@/lib/store";
import { Merge, GitPullRequest, Clock, Activity, CheckCircle2, XCircle, Timer } from "lucide-react";
import { cn } from "@/lib/utils";

const stateStyle: Record<string, string> = {
  queued: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  blocked: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  merging: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  reverted: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

function formatRemaining(s: number): string {
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return `${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
}

export function AutoMergeSection() {
  const rate = autoMergeState.rateLimit;
  const stats = autoMergeState.stats;
  const queue = autoMergeState.queue;
  const detectionPauseRemaining = useSentinel((s) => s.detectionPauseRemaining);

  const ratePct = (rate.used / rate.limit) * 100;
  const rateColor = ratePct >= 100 ? "bg-rose-500" : ratePct >= 66 ? "bg-amber-500" : "bg-emerald-500";

  return (
    <div className="space-y-3 p-6">
      {/* Stats strip */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <CheckCircle2 className="h-3 w-3 text-emerald-400" /> Total Merged
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{stats.totalMerged}</Mono>
            <div className="text-[10px] text-zinc-500">last 7 days</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <XCircle className="h-3 w-3 text-rose-400" /> Total Reverted
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{stats.totalReverted}</Mono>
            <div className="text-[10px] text-zinc-500">auto-reverted by sandbox</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Activity className="h-3 w-3 text-cyan-400" /> Success Rate
            </div>
            <Mono className="mt-1 block text-xl text-emerald-400">{stats.successRate}%</Mono>
            <div className="text-[10px] text-zinc-500">target ≥ 85%</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Timer className="h-3 w-3 text-amber-400" /> Rate Limit
            </div>
            <Mono className={cn("mt-1 block text-xl", ratePct >= 100 ? "text-rose-400" : "text-amber-400")}>{rate.used}/{rate.limit}</Mono>
            <div className="text-[10px] text-zinc-500">per {rate.window}</div>
          </div>
        </Panel>
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* Queue table */}
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Auto-Merge Queue"
            sub="PRs awaiting merge or blocked"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {queue.length} in queue
              </Badge>
            }
          />
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Incident</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">State</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Gates</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Queued</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {queue.map((q) => (
                  <TableRow key={q.incidentId} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-cyan-400">{q.incidentId}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-xs text-zinc-300">{q.service}</TableCell>
                    <TableCell className="px-3 py-2">
                      {q.pr ? (
                        <span className="flex items-center gap-1">
                          <GitPullRequest className="h-3 w-3 text-zinc-500" />
                          <Mono className="text-[11px] text-cyan-400">#{q.pr}</Mono>
                        </span>
                      ) : (
                        <span className="text-[11px] text-zinc-600">—</span>
                      )}
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", stateStyle[q.state])}>
                        {q.state === "queued" && <PulseDot color="bg-cyan-400" className="mr-1 h-1.5 w-1.5" />}
                        {q.state}
                      </Badge>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <div className="flex items-center gap-1">
                        {q.gates.pass > 0 && (
                          <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                            {q.gates.pass} pass
                          </Badge>
                        )}
                        {q.gates.fail > 0 && (
                          <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                            {q.gates.fail} fail
                          </Badge>
                        )}
                        {q.gates.pending > 0 && (
                          <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[10px] text-amber-400">
                            {q.gates.pending} pending
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeMDHM(q.queuedAt)}</Mono>
                      <div className="text-[10px] text-zinc-600">{timeAgo(q.queuedAt)}</div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </Panel>

        {/* Rate limit + cooldown */}
        <div className="space-y-3">
          <Panel>
            <PanelHeader title="Rate Limit" sub="GitHub merges per hour" />
            <div className="px-4 py-4">
              <div className="flex items-baseline justify-between">
                <Mono className="text-2xl text-zinc-100">{rate.used}<span className="text-zinc-600">/{rate.limit}</span></Mono>
                <span className="text-[10px] text-zinc-500">{rate.window} window</span>
              </div>
              <div className="mt-3 h-2 overflow-hidden rounded-full bg-zinc-800">
                <div className={cn("h-full rounded-full transition-all", rateColor)} style={{ width: `${ratePct}%` }} />
              </div>
              <div className="mt-2 flex items-center justify-between text-[10px]">
                <span className="text-zinc-500">{rate.limit - rate.used} remaining</span>
                <span className="font-mono text-zinc-500">resets in 42m</span>
              </div>
              <div className="mt-3 border-t border-zinc-800 pt-3">
                <Button variant="outline" size="sm" className="h-7 w-full border-zinc-700 bg-zinc-900 text-[11px] text-zinc-300">
                  <Clock className="h-3 w-3" /> Reset window
                </Button>
              </div>
            </div>
          </Panel>

          <Panel>
            <PanelHeader title="Cooldown" sub="Per-incident backoff" />
            <div className="px-4 py-4">
              {autoMergeState.cooldown.active ? (
                <div>
                  <div className="flex items-center gap-2">
                    <Timer className="h-4 w-4 text-amber-400" />
                    <Mono className="text-2xl text-amber-400">{formatRemaining(autoMergeState.cooldown.remaining)}</Mono>
                  </div>
                  <p className="mt-2 text-[11px] text-zinc-500">Active cooldown on INC-2B8E1D7A after a reverted merge.</p>
                </div>
              ) : (
                <div>
                  <div className="flex items-center gap-2">
                    <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                    <Mono className="text-base text-emerald-400">no active cooldown</Mono>
                  </div>
                  <p className="mt-2 text-[11px] text-zinc-500">All providers eligible for auto-merge.</p>
                </div>
              )}
              {detectionPauseRemaining > 0 && (
                <div className="mt-3 border-t border-zinc-800 pt-3 text-[11px] text-amber-400">
                  Detection paused: <Mono>{formatRemaining(detectionPauseRemaining)}</Mono>
                </div>
              )}
            </div>
          </Panel>
        </div>
      </div>

      {/* Recent merges */}
      <Panel>
        <PanelHeader
          title="Recent Auto-Merges"
          sub="Last 24 hours"
          right={
            <Button variant="outline" size="sm" className="h-7 border-zinc-700 bg-zinc-900 text-[11px] text-zinc-300">
              <Merge className="h-3 w-3" /> Drain queue
            </Button>
          }
        />
        <div className="overflow-x-auto scrollbar-thin">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Incident</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Outcome</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Gates</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Confidence</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Merged At</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {[
                { pr: 4821, inc: "INC-9C1F4E8D", svc: "search-indexer", outcome: "merged", conf: 93, time: "16:58:42" },
                { pr: 4818, inc: "INC-4D7E2A8F", svc: "notifications-worker", outcome: "merged", conf: 96, time: "16:33:18" },
                { pr: 4812, inc: "INC-3A7C1E5D", svc: "checkout-api", outcome: "merged", conf: 94, time: "13:42:01" },
                { pr: 4805, inc: "INC-6B9D4F2A", svc: "search-indexer", outcome: "merged", conf: 89, time: "11:18:33" },
                { pr: 4800, inc: "—", svc: "auth-service", outcome: "reverted", conf: 0, time: "10:42:11" },
                { pr: 4798, inc: "INC-7D2E9B4F", svc: "billing-cron", outcome: "merged", conf: 90, time: "08:12:47" },
              ].map((r) => (
                <TableRow key={r.pr} className="border-zinc-800/60 hover:bg-zinc-800/30">
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-cyan-400">#{r.pr}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-300">{r.inc}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2 text-xs text-zinc-300">{r.svc}</TableCell>
                  <TableCell className="px-3 py-2">
                    {r.outcome === "merged" ? (
                      <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                        <CheckCircle2 className="h-3 w-3" /> merged
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                        <XCircle className="h-3 w-3" /> reverted
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <div className="flex gap-0.5">
                      {Array.from({ length: 11 }).map((_, i) => (
                        <span key={i} className={cn("h-2 w-1 rounded-sm", r.outcome === "merged" ? "bg-emerald-500" : i < 5 ? "bg-rose-500" : "bg-zinc-700")} />
                      ))}
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className={cn("text-[11px]", r.conf >= 85 ? "text-emerald-400" : "text-zinc-500")}>{r.conf}%</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-400">{r.time}</Mono>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </Panel>
    </div>
  );
}
