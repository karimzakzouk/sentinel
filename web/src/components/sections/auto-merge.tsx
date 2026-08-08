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
import { Panel, PanelHeader, Mono, timeAgo, timeMDHM, PulseDot, EmptyState } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import { Merge, GitPullRequest, Activity, CheckCircle2, XCircle, Timer, Clock, AlertCircle } from "lucide-react";
import { cn } from "@/lib/utils";

const stateStyle: Record<string, string> = {
  queued: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  blocked: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  merging: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  merged: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  reverted: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  rejected: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

// v5 had a "Gates" column that always rendered 0 pass / 0 fail / 0 pending —
// the gate data wasn't returned by /automerge. v1 drops the dead column
// entirely. The auto_merge_status string on each incident is shown as-is.

interface QueueEntry {
  pr: number;
  inc: string;
  svc: string;
  outcome: string;
  url: string;
  updated: string;
}

export function AutoMergeSection() {
  const autoMerge = useSentinelStore((s) => s.autoMerge);
  const refreshAutoMerge = useSentinelStore((s) => s.refreshAutoMerge);
  const incidents = useSentinelStore((s) => s.incidents);

  const recentOutcomes = useMemo<QueueEntry[]>(() => {
    return incidents
      .filter((inc) => inc.pr_number && inc.pr_url)
      .map((inc) => {
        let outcome = "queued";
        if (inc.status === "resolved" || inc.status === "verified") outcome = "merged";
        else if (inc.status === "rejected" || inc.status === "failed") outcome = "reverted";
        else if (inc.auto_merge_status) outcome = inc.auto_merge_status;
        return {
          pr: inc.pr_number as number,
          inc: inc.incident_id.slice(0, 8),
          svc: inc.cluster?.service ?? "—",
          outcome,
          url: inc.pr_url as string,
          updated: inc.updated_at,
        };
      })
      .sort((a, b) => (a.updated > b.updated ? -1 : 1));
  }, [incidents]);

  if (!autoMerge) {
    return (
      <div className="p-6">
        <EmptyState
          title="Auto-merge not configured"
          description="Enable auto-merge in Settings → Auto-Merge to start tracking the queue, rate limits, and gate status here."
          icon={Merge}
        />
      </div>
    );
  }

  const totalMerged = recentOutcomes.filter((o) => o.outcome === "merged").length;
  const totalReverted = recentOutcomes.filter((o) => o.outcome === "reverted").length;
  const totalDecided = totalMerged + totalReverted;
  const successRate = totalDecided > 0 ? (totalMerged / totalDecided) * 100 : 0;

  const rateUsed = autoMerge.merges_this_hour ?? 0;
  const rateLimit = autoMerge.max_per_hour ?? 0;
  const ratePct = rateLimit > 0 ? (rateUsed / rateLimit) * 100 : 0;
  const rateColor = ratePct >= 100 ? "bg-rose-500" : ratePct >= 66 ? "bg-amber-500" : "bg-emerald-500";
  const cooldownActive = autoMerge.cooldown_active ?? false;
  const cooldownReason = autoMerge.cooldown_reason ?? "";

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <CheckCircle2 className="h-3 w-3 text-emerald-400" /> Total Merged
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{totalMerged}</Mono>
            <div className="text-[10px] text-zinc-500">from incidents</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <XCircle className="h-3 w-3 text-rose-400" /> Total Reverted
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{totalReverted}</Mono>
            <div className="text-[10px] text-zinc-500">rejected / failed</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Activity className="h-3 w-3 text-cyan-400" /> Success Rate
            </div>
            <Mono className="mt-1 block text-xl text-emerald-400">{successRate.toFixed(1)}%</Mono>
            <div className="text-[10px] text-zinc-500">target ≥ 85%</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Timer className="h-3 w-3 text-amber-400" /> Rate Limit
            </div>
            <Mono className={cn("mt-1 block text-xl", ratePct >= 100 ? "text-rose-400" : "text-amber-400")}>
              {rateUsed}/{rateLimit}
            </Mono>
            <div className="text-[10px] text-zinc-500">per hour · {Math.max(0, rateLimit - rateUsed)} remaining</div>
          </div>
        </Panel>
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Auto-Merge Queue"
            sub="PRs awaiting merge or blocked"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {recentOutcomes.length} entries
              </Badge>
            }
          />
          <div className="overflow-x-auto scrollbar-thin">
            {recentOutcomes.length === 0 ? (
              <div className="px-4 py-12 text-center text-xs text-zinc-600">
                Queue is empty — no PRs awaiting auto-merge.
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="border-zinc-800 hover:bg-transparent">
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Incident</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">State</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Updated</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recentOutcomes.map((q) => (
                    <TableRow key={`${q.inc}-${q.pr}`} className="border-zinc-800/60 hover:bg-zinc-800/30">
                      <TableCell className="px-3 py-2">
                        <Mono className="text-[11px] text-cyan-400">{q.inc}</Mono>
                      </TableCell>
                      <TableCell className="px-3 py-2 text-xs text-zinc-300">{q.svc}</TableCell>
                      <TableCell className="px-3 py-2">
                        <a href={q.url} target="_blank" rel="noreferrer" className="flex items-center gap-1 hover:underline">
                          <GitPullRequest className="h-3 w-3 text-zinc-500" />
                          <Mono className="text-[11px] text-cyan-400">#{q.pr}</Mono>
                        </a>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", stateStyle[q.outcome] ?? "border-zinc-700 bg-zinc-900 text-zinc-400")}>
                          {q.outcome === "queued" && <PulseDot color="bg-cyan-400" className="mr-1 h-1.5 w-1.5" />}
                          {q.outcome}
                        </Badge>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Mono className="text-[11px] text-zinc-400">{timeMDHM(q.updated)}</Mono>
                        <div className="text-[10px] text-zinc-600">{timeAgo(q.updated)}</div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </Panel>

        <div className="space-y-3">
          <Panel>
            <PanelHeader title="Rate Limit" sub="GitHub merges per hour" />
            <div className="px-4 py-4">
              <div className="flex items-baseline justify-between">
                <Mono className="text-2xl text-zinc-100">
                  {rateUsed}
                  <span className="text-zinc-600">/{rateLimit}</span>
                </Mono>
                <span className="text-[10px] text-zinc-500">1h window</span>
              </div>
              <div className="mt-3 h-2 overflow-hidden rounded-full bg-zinc-800">
                <div className={cn("h-full rounded-full transition-all", rateColor)} style={{ width: `${Math.min(100, ratePct)}%` }} />
              </div>
              <div className="mt-2 flex items-center justify-between text-[10px]">
                <span className="text-zinc-500">{Math.max(0, rateLimit - rateUsed)} remaining</span>
                <span className="font-mono text-zinc-500">resets hourly</span>
              </div>
            </div>
          </Panel>

          <Panel>
            <PanelHeader title="Cooldown" sub="Per-policy backoff" />
            <div className="px-4 py-4">
              {cooldownActive ? (
                <div>
                  <div className="flex items-center gap-2">
                    <Timer className="h-4 w-4 text-amber-400" />
                    <Mono className="text-base text-amber-400">active</Mono>
                  </div>
                  <p className="mt-2 text-[11px] text-zinc-500">{cooldownReason || "Auto-merge is paused."}</p>
                </div>
              ) : (
                <div>
                  <div className="flex items-center gap-2">
                    <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                    <Mono className="text-base text-emerald-400">no active cooldown</Mono>
                  </div>
                  <p className="mt-2 text-[11px] text-zinc-500">Auto-merge is eligible to run.</p>
                </div>
              )}
            </div>
          </Panel>

          <Panel>
            <PanelHeader title="Enabled" sub="Auto-merge policy state" />
            <div className="px-4 py-3">
              <div className="flex items-center gap-2">
                {autoMerge.enabled ? (
                  <>
                    <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                    <span className="font-mono text-sm text-emerald-400">enabled</span>
                  </>
                ) : (
                  <>
                    <XCircle className="h-4 w-4 text-zinc-500" />
                    <span className="font-mono text-sm text-zinc-400">disabled</span>
                  </>
                )}
              </div>
            </div>
          </Panel>
        </div>
      </div>

      {recentOutcomes.length === 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-cyan-500/20 bg-cyan-500/5 px-4 py-3">
          <Clock className="h-4 w-4 shrink-0 text-cyan-400" />
          <p className="text-xs text-zinc-300">
            Auto-merge is enabled and ready. Once an incident produces a verified PR, it will appear in the queue.
          </p>
        </div>
      )}

      <div className="flex items-center justify-end gap-2">
        {recentOutcomes.length === 0 && (
          <span className="text-[10px] text-zinc-600">
            <AlertCircle className="inline h-3 w-3" /> /automerge returns policy state only — queue entries are derived from incidents.
          </span>
        )}
        <button onClick={() => refreshAutoMerge()} className="text-[11px] text-cyan-400 hover:text-cyan-300">
          Refresh state →
        </button>
      </div>
    </div>
  );
}
