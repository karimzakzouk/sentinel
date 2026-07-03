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
import {
  Panel,
  PanelHeader,
  Mono,
  timeAgo,
  timeMDHM,
  PulseDot,
  EmptyState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import {
  Merge,
  GitPullRequest,
  Activity,
  CheckCircle2,
  XCircle,
  Timer,
  Clock,
  AlertCircle,
} from "lucide-react";
import { cn } from "@/lib/utils";

const stateStyle: Record<string, string> = {
  queued: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  blocked: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  merging: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  merged: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  reverted: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  rejected: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

function formatRemaining(iso: string | null | undefined): string {
  if (!iso) return "—";
  const ms = new Date(iso).getTime() - Date.now();
  if (ms <= 0) return "expired";
  const sec = Math.floor(ms / 1000);
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function AutoMergeSection() {
  const autoMerge = useSentinelStore((s) => s.autoMerge);
  const refreshAutoMerge = useSentinelStore((s) => s.refreshAutoMerge);
  const incidents = useSentinelStore((s) => s.incidents);

  // /automerge returns only config + rate limit, not a queue. Derive a
  // queue/outcome view from incidents that have a PR attached. Status
  // mapping: any non-resolved/non-verified incident with a PR is "queued"
  // (waiting for auto-merge gates); resolved = merged; rejected/failed =
  // reverted/rejected.
  const recentOutcomes = useMemo(() => {
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

  const rateUsed = autoMerge.merges_this_hour || 0;
  const rateLimit = autoMerge.max_per_hour || 0;
  const ratePct = rateLimit > 0 ? (rateUsed / rateLimit) * 100 : 0;
  const rateColor = ratePct >= 100 ? "bg-rose-500" : ratePct >= 66 ? "bg-amber-500" : "bg-emerald-500";
  const cooldownActive = autoMerge.cooldown_active || false;
  const cooldownReason = autoMerge.cooldown_reason || "";

  return (
    <div className="space-y-3 p-6">
      {/* Stats strip */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <CheckCircle2 className="h-3 w-3 text-emerald-400" /> Total Merged
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{0}</Mono>
            <div className="text-[10px] text-zinc-500">cumulative</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <XCircle className="h-3 w-3 text-rose-400" /> Total Reverted
            </div>
            <Mono className="mt-1 block text-xl text-zinc-100">{0}</Mono>
            <div className="text-[10px] text-zinc-500">auto-reverted by sandbox</div>
          </div>
        </Panel>
        <Panel>
          <div className="px-4 py-3">
            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
              <Activity className="h-3 w-3 text-cyan-400" /> Success Rate
            </div>
            <Mono className="mt-1 block text-xl text-emerald-400">{(0).toFixed(1)}%</Mono>
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
            <div className="text-[10px] text-zinc-500">per hour · {rateLimit - rateUsed} remaining</div>
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
                {recentOutcomes.length} in queue
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
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Repo</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">State</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Gates</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Queued</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recentOutcomes.map((q) => (
                    <TableRow key={q.inc} className="border-zinc-800/60 hover:bg-zinc-800/30">
                      <TableCell className="px-3 py-2">
                        <Mono className="text-[11px] text-cyan-400">{q.inc.slice(0, 8)}</Mono>
                      </TableCell>
                      <TableCell className="px-3 py-2 text-xs text-zinc-300">{q.svc}</TableCell>
                      <TableCell className="px-3 py-2">
                        {q.pr ? (
                          <a
                            href={q.url}
                            target="_blank"
                            rel="noreferrer"
                            className="flex items-center gap-1 hover:underline"
                          >
                            <GitPullRequest className="h-3 w-3 text-zinc-500" />
                            <Mono className="text-[11px] text-cyan-400">#{q.pr}</Mono>
                          </a>
                        ) : (
                          <span className="text-[11px] text-zinc-600">—</span>
                        )}
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Mono className="text-[11px] text-zinc-400">{"—"}</Mono>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Badge variant="outline" className={cn("font-mono text-[10px] lowercase", stateStyle[q.outcome] ?? "border-zinc-700 bg-zinc-900 text-zinc-400")}>
                          {q.outcome === "queued" && <PulseDot color="bg-cyan-400" className="mr-1 h-1.5 w-1.5" />}
                          {q.outcome}
                        </Badge>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <div className="flex items-center gap-1">
                          {[].length > 0 && (
                            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                              {[].length} pass
                            </Badge>
                          )}
                          {[].length > 0 && (
                            <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                              {[].length} fail
                            </Badge>
                          )}
                          {[].length === 0 && [].length === 0 && (
                            <span className="text-[10px] text-zinc-600">—</span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Mono className="text-[11px] text-zinc-400">{timeMDHM("")}</Mono>
                        <div className="text-[10px] text-zinc-600">{timeAgo("")}</div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </Panel>

        {/* Rate limit + cooldown */}
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
                <div
                  className={cn("h-full rounded-full transition-all", rateColor)}
                  style={{ width: `${Math.min(100, ratePct)}%` }}
                />
              </div>
              <div className="mt-2 flex items-center justify-between text-[10px]">
                <span className="text-zinc-500">{rateLimit - rateUsed} remaining</span>
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
                    <Mono className="text-2xl text-amber-400">{formatRemaining("")}</Mono>
                  </div>
                  <p className="mt-2 text-[11px] text-zinc-500">
                    {cooldownReason || "Active cooldown — auto-merge is paused."}
                  </p>
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

      {/* Recent outcomes (derived from queue states) */}
      <Panel>
        <PanelHeader
          title="Recent Auto-Merge Activity"
          sub="From the current queue"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {recentOutcomes.length} entries
            </Badge>
          }
        />
        <div className="overflow-x-auto scrollbar-thin">
          {recentOutcomes.length === 0 ? (
            <div className="px-4 py-12 text-center text-xs text-zinc-600">
              <AlertCircle className="mx-auto mb-2 h-5 w-5 text-zinc-600" />
              No recent auto-merge activity. Once a PR is queued, it will appear here.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Incident</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Outcome</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Gates</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Queued</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {recentOutcomes.map((r) => (
                  <TableRow key={r.pr + "-" + r.inc} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-cyan-400">#{r.pr}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-300">{r.inc.slice(0, 8)}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-xs text-zinc-300">{r.svc}</TableCell>
                    <TableCell className="px-3 py-2">
                      <Badge
                        variant="outline"
                        className={cn(
                          "font-mono text-[10px] lowercase",
                          r.outcome === "merged"
                            ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                            : r.outcome === "reverted"
                            ? "border-rose-500/30 bg-rose-500/10 text-rose-400"
                            : r.outcome === "blocked"
                            ? "border-amber-500/30 bg-amber-500/10 text-amber-400"
                            : "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
                        )}
                      >
                        {r.outcome === "merged" && <CheckCircle2 className="h-3 w-3" />}
                        {r.outcome === "reverted" && <XCircle className="h-3 w-3" />}
                        {r.outcome}
                      </Badge>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <div className="flex gap-1.5">
                        <span className="font-mono text-[10px] text-emerald-400">{0} pass</span>
                        {0 > 0 && (
                          <span className="font-mono text-[10px] text-rose-400">{0} fail</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeMDHM("")}</Mono>
                      <div className="text-[10px] text-zinc-600">{timeAgo("")}</div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      </Panel>

      {/* Hint when nothing's happening */}
      {recentOutcomes.length === 0 && 0 === 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-cyan-500/20 bg-cyan-500/5 px-4 py-3">
          <Clock className="h-4 w-4 shrink-0 text-cyan-400" />
          <p className="text-xs text-zinc-300">
            Auto-merge is enabled and ready. Once an incident produces a verified PR, it will appear in the queue.
          </p>
        </div>
      )}

      {/* Refresh button (manual) */}
      <div className="flex justify-end">
        <button
          onClick={() => refreshAutoMerge()}
          className="text-[11px] text-cyan-400 hover:text-cyan-300"
        >
          Refresh state →
        </button>
      </div>
    </div>
  );
}
