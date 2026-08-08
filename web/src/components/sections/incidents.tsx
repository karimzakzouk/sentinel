"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  StatusBadge,
  ConfidenceBar,
  Mono,
  Panel,
  timeAgo,
  timeMDHM,
  EmptyState,
  ErrorState,
} from "@/components/sentinel/common";
import { useSentinelStore } from "@/lib/store";
import type { Incident } from "@/lib/types";
import {
  Search,
  ChevronRight,
  GitPullRequest,
  AlertTriangle,
  Brain,
  Wrench,
  Activity,
  FileCode2,
  Inbox,
  Loader2,
} from "lucide-react";
import { cn } from "@/lib/utils";

const statusOptions = [
  { value: "all", label: "All statuses" },
  { value: "investigating", label: "Investigating" },
  { value: "verified", label: "Verified" },
  { value: "failed", label: "Failed" },
  { value: "escalated", label: "Escalated" },
  { value: "resolved", label: "Resolved" },
  { value: "root_caused", label: "Root caused" },
  { value: "fix_proposed", label: "Fix proposed" },
  { value: "executing", label: "Executing" },
];

function TraceEntry({ entry, idx }: { entry: Incident["state"]["agent_trace"][number]; idx: number }) {
  const phase = typeof entry.phase === "string" ? entry.phase : "";
  const agent = typeof entry.agent === "string" ? entry.agent : "";
  const status = typeof entry.status === "string" ? entry.status : "";
  const message = typeof entry.message === "string" ? entry.message : "";
  const statusColor =
    status === "completed" || status === "ok" || status === "success"
      ? "text-emerald-400"
      : status === "running" || status === "in_progress"
        ? "text-cyan-400"
        : status === "failed" || status === "error"
          ? "text-rose-400"
          : "text-zinc-400";
  return (
    <div className="border-l border-zinc-800 pl-3">
      <div className="flex flex-wrap items-center gap-2 py-1.5">
        <span className="font-mono text-[10px] text-zinc-600">#{idx + 1}</span>
        {phase && <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-300">{phase}</Badge>}
        {agent && <Badge variant="outline" className="font-mono text-[10px] border-cyan-700/40 bg-cyan-900/30 text-cyan-300">{agent}</Badge>}
        {status && <span className={cn("font-mono text-[10px]", statusColor)}>{status}</span>}
      </div>
      {message && <p className="text-[11px] text-zinc-400">{message}</p>}
    </div>
  );
}

export function IncidentsSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const selectedIncidentId = useSentinelStore((s) => s.selectedIncidentId);
  const setSelectedIncident = useSentinelStore((s) => s.setSelectedIncident);
  const drawerOpen = useSentinelStore((s) => s.drawerOpen);
  const setDrawerOpen = useSentinelStore((s) => s.setDrawerOpen);

  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [detailLoading, setDetailLoading] = useState(false);
  const [detail, setDetail] = useState<Incident | null>(null);

  const filtered = useMemo(() => {
    return incidents.filter((inc) => {
      if (statusFilter !== "all" && inc.status !== statusFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          inc.incident_id.toLowerCase().includes(q) ||
          inc.cluster?.service?.toLowerCase().includes(q) ||
          inc.cluster?.summary?.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [incidents, search, statusFilter]);

  // When the selected incident ID changes (from the command palette or
  // dashboard click), open the drawer and fetch fresh detail.
  useEffect(() => {
    if (!selectedIncidentId) {
      setDetail(null);
      return;
    }
    let cancelled = false;
    setDetailLoading(true);
    (async () => {
      const fetched = await useSentinelStore.getState().fetchIncident(selectedIncidentId);
      if (!cancelled) {
        setDetail(fetched);
        setDetailLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [selectedIncidentId]);

  if (loading && incidents.length === 0) {
    return (
      <div className="space-y-3 p-6">
        <div className="h-12 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
        <div className="h-96 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
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
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-zinc-500" />
          <Input
            placeholder="Search by ID, service, or summary..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-9 bg-zinc-900 pl-8 text-xs"
          />
        </div>
        <Select value={statusFilter} onValueChange={setStatusFilter}>
          <SelectTrigger className="h-9 w-[160px] bg-zinc-900 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {statusOptions.map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
          {filtered.length} / {incidents.length}
        </Badge>
      </div>

      {filtered.length === 0 ? (
        <EmptyState title="No incidents match" description="Try adjusting the search or filter." icon={Inbox} />
      ) : (
        <Panel>
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">ID</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Summary</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Confidence</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Updated</TableHead>
                  <TableHead className="h-8 w-8 px-3" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((inc) => (
                  <TableRow
                    key={inc.incident_id}
                    onClick={() => setSelectedIncident(inc.incident_id)}
                    className="cursor-pointer border-zinc-800/60 hover:bg-zinc-800/30"
                  >
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-cyan-400">{inc.incident_id.slice(0, 8)}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-xs text-zinc-300">{inc.cluster?.service ?? "—"}</TableCell>
                    <TableCell className="px-3 py-2">
                      <StatusBadge status={inc.status} />
                    </TableCell>
                    <TableCell className="max-w-[280px] px-3 py-2">
                      <span className="block truncate text-[11px] text-zinc-400">{inc.cluster?.summary ?? "—"}</span>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <ConfidenceBar value={inc.state?.root_cause?.confidence ?? 0} />
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      {inc.pr_number ? (
                        <a
                          href={inc.pr_url ?? "#"}
                          target="_blank"
                          rel="noreferrer"
                          onClick={(e) => e.stopPropagation()}
                          className="flex items-center gap-1 hover:underline"
                        >
                          <GitPullRequest className="h-3 w-3 text-zinc-500" />
                          <Mono className="text-[11px] text-cyan-400">#{inc.pr_number}</Mono>
                        </a>
                      ) : (
                        <span className="text-[11px] text-zinc-600">—</span>
                      )}
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeMDHM(inc.updated_at)}</Mono>
                      <div className="text-[10px] text-zinc-600">{timeAgo(inc.updated_at)}</div>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <ChevronRight className="h-3.5 w-3.5 text-zinc-500" />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </Panel>
      )}

      <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
        <SheetContent side="right" className="w-full overflow-y-auto bg-zinc-950 sm:max-w-lg">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2 text-sm">
              <Mono className="text-cyan-400">{selectedIncidentId?.slice(0, 8) ?? "—"}</Mono>
              <span className="text-zinc-500">·</span>
              {detail && <StatusBadge status={detail.status} />}
            </SheetTitle>
          </SheetHeader>
          {detailLoading ? (
            <div className="flex items-center justify-center p-12">
              <Loader2 className="h-5 w-5 animate-spin text-zinc-500" />
            </div>
          ) : !detail ? (
            <div className="p-12 text-center text-xs text-zinc-600">Incident not found.</div>
          ) : (
            <div className="space-y-4 p-4">
              <div>
                <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                  <AlertTriangle className="h-3 w-3" /> Service
                </div>
                <Mono className="text-[11px] text-cyan-400">{detail.cluster?.service ?? "—"}</Mono>
                <Mono className="ml-2 text-[11px] text-zinc-500">{detail.cluster?.namespace ?? "—"}</Mono>
              </div>
              {detail.cluster?.summary && (
                <div>
                  <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">Cluster Summary</div>
                  <p className="text-[12px] leading-relaxed text-zinc-300">{detail.cluster.summary}</p>
                </div>
              )}
              {detail.state?.root_cause && (
                <div>
                  <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                    <Brain className="h-3 w-3" /> Root Cause
                  </div>
                  <p className="text-[12px] leading-relaxed text-zinc-200">{detail.state.root_cause.statement}</p>
                  <ConfidenceBar value={detail.state.root_cause.confidence} className="mt-1.5" />
                </div>
              )}
              {detail.state?.hypotheses && detail.state.hypotheses.length > 0 && (
                <div>
                  <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">
                    Hypotheses ({detail.state.hypotheses.length})
                  </div>
                  <div className="space-y-1.5">
                    {detail.state.hypotheses.map((h) => (
                      <div key={h.h_id} className="rounded-md border border-zinc-800 bg-zinc-950 p-2">
                        <div className="flex items-center justify-between">
                          <Mono className="text-[11px] text-cyan-400">{h.h_id}</Mono>
                          <ConfidenceBar value={h.confidence} />
                        </div>
                        <p className="mt-1 text-[11px] text-zinc-300">{h.statement}</p>
                        {h.falsified && <span className="mt-1 inline-block font-mono text-[10px] text-rose-400">falsified</span>}
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {detail.state?.fix_proposal && (
                <div>
                  <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                    <Wrench className="h-3 w-3" /> Fix Proposal
                  </div>
                  <p className="text-[12px] leading-relaxed text-zinc-200">{detail.state.fix_proposal.description}</p>
                  <div className="mt-1.5 flex items-center gap-2 text-[10px] text-zinc-500">
                    <span className="font-mono">type: {detail.state.fix_proposal.fix_type}</span>
                    {detail.state.fix_proposal.repo && <span className="font-mono">repo: {detail.state.fix_proposal.repo}</span>}
                  </div>
                </div>
              )}
              {detail.sandbox_summary && (
                <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
                  <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                    <FileCode2 className="h-3 w-3" /> Sandbox Verification
                  </div>
                  <pre className="whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-zinc-300">{detail.sandbox_summary}</pre>
                </div>
              )}
              {detail.pr_url && (
                <a
                  href={detail.pr_url}
                  target="_blank"
                  rel="noreferrer"
                  className="flex items-center gap-2 rounded-md border border-cyan-500/30 bg-cyan-500/5 p-2 text-[11px] text-cyan-300 hover:bg-cyan-500/10"
                >
                  <GitPullRequest className="h-3 w-3" />
                  PR #{detail.pr_number} → {detail.pr_branch ?? "main"}
                </a>
              )}
              {detail.state?.agent_trace && detail.state.agent_trace.length > 0 && (
                <div>
                  <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                    <Activity className="h-3 w-3" /> Agent Trace ({detail.state.agent_trace.length})
                  </div>
                  <div className="space-y-1 rounded-md border border-zinc-800 bg-zinc-950 p-2">
                    {detail.state.agent_trace.slice(0, 20).map((entry, i) => (
                      <TraceEntry key={i} entry={entry} idx={i} />
                    ))}
                    {detail.state.agent_trace.length > 20 && (
                      <div className="pt-1 text-center text-[10px] text-zinc-600">
                        + {detail.state.agent_trace.length - 20} more…
                      </div>
                    )}
                  </div>
                </div>
              )}
              <Button
                variant="outline"
                size="sm"
                className="w-full border-zinc-700 bg-zinc-900 text-[11px] text-zinc-300"
                onClick={() => setSelectedIncident(null)}
              >
                Close
              </Button>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
