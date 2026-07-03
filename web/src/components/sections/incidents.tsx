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
import type { Incident, Hypothesis, FixProposal } from "@/lib/types";
import {
  Search,
  Filter,
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

// ─── Agent trace rendering ──────────────────────────────────────────────────
// agent_trace is `Array<Record<string, unknown>>` — we render each entry as a
// flat list showing the phase / agent / duration / tokens / cost when present,
// plus a JSON fallback for everything else.

function TraceEntry({ entry, idx }: { entry: Record<string, unknown>; idx: number }) {
  const phase = typeof entry.phase === "string" ? entry.phase : "";
  const agent = typeof entry.agent === "string" ? entry.agent : "";
  const status = typeof entry.status === "string" ? entry.status : "";
  const duration = typeof entry.duration === "string" || typeof entry.duration === "number" ? String(entry.duration) : "";
  const tokens = typeof entry.tokens === "number" ? entry.tokens : undefined;
  const cost = typeof entry.cost === "number" ? entry.cost : undefined;
  const message = typeof entry.message === "string" ? entry.message : "";

  const statusColor =
    status === "completed" || status === "ok" || status === "success"
      ? "text-emerald-400"
      : status === "running" || status === "in_progress"
      ? "text-cyan-400"
      : status === "failed" || status === "error"
      ? "text-rose-400"
      : "text-zinc-400";

  // Filter out the fields we've already rendered
  const restEntries = Object.entries(entry).filter(
    ([k]) => !["phase", "agent", "status", "duration", "tokens", "cost", "message"].includes(k),
  );

  return (
    <div className="border-l border-zinc-800 pl-3">
      <div className="flex flex-wrap items-center gap-2 py-1.5">
        <span className="font-mono text-[10px] text-zinc-600">#{idx + 1}</span>
        {phase && (
          <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-300">
            {phase}
          </Badge>
        )}
        {agent && (
          <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
            {agent}
          </Badge>
        )}
        {status && (
          <span className={cn("font-mono text-[10px]", statusColor)}>{status}</span>
        )}
        {duration && <Mono className="text-[10px] text-zinc-500">{duration}</Mono>}
        {tokens !== undefined && <Mono className="text-[10px] text-zinc-500">{tokens.toLocaleString()} tok</Mono>}
        {cost !== undefined && <Mono className="text-[10px] text-cyan-400">${cost.toFixed(3)}</Mono>}
      </div>
      {message && <p className="text-[11px] leading-relaxed text-zinc-400">{message}</p>}
      {restEntries.length > 0 && (
        <pre className="mt-1 overflow-x-auto rounded border border-zinc-800 bg-zinc-950 p-2 font-mono text-[10px] text-zinc-500 scrollbar-thin">
          {JSON.stringify(Object.fromEntries(restEntries), null, 2)}
        </pre>
      )}
    </div>
  );
}

function HypothesisRow({ h }: { h: Hypothesis }) {
  return (
    <div
      className={cn(
        "flex items-center gap-3 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2",
        h.falsified && "opacity-50",
      )}
    >
      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
        {h.h_id}
      </Badge>
      <span className={cn("flex-1 text-[12px] text-zinc-300", h.falsified && "line-through")}>
        {h.statement}
      </span>
      {h.falsified ? (
        <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
          falsified
        </Badge>
      ) : (
        <ConfidenceBar value={h.confidence} />
      )}
    </div>
  );
}

function FixProposalView({ fix }: { fix: FixProposal }) {
  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
      <div className="mb-2 flex items-center gap-2">
        <Badge variant="outline" className="font-mono text-[10px] border-cyan-500/30 bg-cyan-500/10 text-cyan-400">
          {fix.fix_type}
        </Badge>
        {fix.repo && <Mono className="text-[11px] text-zinc-400">{fix.repo}</Mono>}
        {fix.branch_name && <Mono className="text-[11px] text-zinc-500">→ {fix.branch_name}</Mono>}
      </div>
      <p className="text-[12px] leading-relaxed text-zinc-300">{fix.description}</p>
      <div className="mt-2 flex items-center gap-2 border-t border-zinc-800 pt-2">
        <span className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</span>
        <ConfidenceBar value={fix.proposed_confidence} />
      </div>
      {fix.files_to_change.length > 0 && (
        <div className="mt-3 space-y-2">
          <div className="text-[10px] uppercase tracking-wider text-zinc-500">
            Files ({fix.files_to_change.length})
          </div>
          {fix.files_to_change.map((f, i) => (
            <div key={i} className="rounded border border-zinc-800 bg-zinc-950">
              <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900 px-2.5 py-1.5">
                <Mono className="text-[11px] text-zinc-300">{f.path}</Mono>
                <div className="text-[10px] text-zinc-500">
                  {f.new_content ? `${f.new_content.length} chars` : f.changes?.length ? `${f.changes.length} edits` : "—"}
                </div>
              </div>
              {f.new_content && (
                <pre className="max-h-60 overflow-y-auto whitespace-pre-wrap p-2 font-mono text-[10.5px] leading-relaxed text-zinc-300 scrollbar-thin">
                  {f.new_content}
                </pre>
              )}
              {f.changes && f.changes.length > 0 && (
                <div className="space-y-1 p-2">
                  {f.changes.map((c, j) => (
                    <div key={j} className="rounded border border-zinc-800 overflow-hidden">
                      <pre className="whitespace-pre-wrap break-words font-mono text-[10.5px] leading-relaxed text-rose-300 bg-rose-950/20 p-2 m-0 overflow-x-auto scrollbar-thin">
                        {String(c.search ?? "")
                          .split("\n")
                          .map((line, i) => `- ${line}`)
                          .join("\n")}
                      </pre>
                      <pre className="whitespace-pre-wrap break-words font-mono text-[10.5px] leading-relaxed text-emerald-300 bg-emerald-950/20 p-2 m-0 overflow-x-auto scrollbar-thin">
                        {String(c.replace ?? "")
                          .split("\n")
                          .map((line, i) => `+ ${line}`)
                          .join("\n")}
                      </pre>
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function IncidentDetail({ incident, loading }: { incident: Incident | null; loading: boolean }) {
  if (loading && !incident) {
    return (
      <div className="flex items-center justify-center px-4 py-12">
        <Loader2 className="h-5 w-5 animate-spin text-zinc-500" />
      </div>
    );
  }
  if (!incident) {
    return (
      <div className="px-4 py-12 text-center text-xs text-zinc-600">
        Incident not found.
      </div>
    );
  }

  const cluster = incident.cluster;
  const state = incident.state;
  const rootCause = state?.root_cause;
  const hypotheses = state?.hypotheses ?? [];
  const fix = state?.fix_proposal;
  const trace = state?.agent_trace ?? [];

  return (
    <div className="space-y-5 px-4 py-4">
      {/* Cluster summary */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <AlertTriangle className="h-3 w-3" /> Cluster Summary
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
          <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-[11px]">
            <div className="text-zinc-500">Service</div>
            <div className="font-mono text-zinc-200">{cluster?.service ?? "—"}</div>
            <div className="text-zinc-500">Namespace</div>
            <div className="font-mono text-zinc-200">{cluster?.namespace ?? "—"}</div>
            <div className="text-zinc-500">Severity</div>
            <div className="font-mono text-zinc-200">{cluster?.severity ?? "—"}</div>
            <div className="text-zinc-500">Created</div>
            <div className="font-mono text-zinc-200">{timeMDHM(incident.created_at)}</div>
            <div className="text-zinc-500">Updated</div>
            <div className="font-mono text-zinc-200">{timeMDHM(incident.updated_at)}</div>
          </div>
          {cluster?.summary && (
            <p className="mt-2 border-t border-zinc-800 pt-2 text-[12px] leading-relaxed text-zinc-300">
              {cluster.summary}
            </p>
          )}
          {cluster?.anomalies && cluster.anomalies.length > 0 && (
            <div className="mt-2 space-y-1 border-t border-zinc-800 pt-2">
              <div className="text-[10px] uppercase tracking-wider text-zinc-500">
                Anomalies ({cluster.anomalies.length})
              </div>
              {cluster.anomalies.map((a, i) => (
                <div key={i} className="rounded border border-zinc-800 bg-zinc-950 p-2">
                  <div className="flex items-center justify-between">
                    <Mono className="text-[11px] text-zinc-300">{a.detector}</Mono>
                    <Mono className="text-[11px] text-amber-400">{a.score.toFixed(2)}</Mono>
                  </div>
                  {a.explanation && (
                    <p className="mt-1 text-[11px] text-zinc-500">{a.explanation}</p>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {/* Root cause */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Brain className="h-3 w-3" /> Root Cause
        </h4>
        {rootCause ? (
          <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
            <p className="text-[12px] leading-relaxed text-zinc-300">{rootCause.statement}</p>
            <div className="mt-2 flex flex-wrap items-center gap-2 border-t border-zinc-800 pt-2">
              <span className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</span>
              <ConfidenceBar value={rootCause.confidence} />
              {rootCause.category && (
                <Badge variant="outline" className="ml-auto font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                  {rootCause.category}
                </Badge>
              )}
            </div>
            {rootCause.source_files && rootCause.source_files.length > 0 && (
              <div className="mt-2 space-y-1">
                <div className="text-[10px] uppercase tracking-wider text-zinc-500">Source files</div>
                {rootCause.source_files.map((f, i) => (
                  <Mono key={i} className="block text-[11px] text-cyan-400">
                    <FileCode2 className="mr-1 inline h-3 w-3" />
                    {f}
                  </Mono>
                ))}
              </div>
            )}
          </div>
        ) : (
          <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/50 p-3 text-center text-[11px] text-zinc-600">
            Root cause not yet determined.
          </div>
        )}
      </section>

      {/* Hypotheses */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Activity className="h-3 w-3" /> Hypotheses ({hypotheses.length})
        </h4>
        {hypotheses.length === 0 ? (
          <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/50 p-3 text-center text-[11px] text-zinc-600">
            No hypotheses generated yet.
          </div>
        ) : (
          <div className="space-y-1.5">
            {hypotheses.map((h) => (
              <HypothesisRow key={h.h_id} h={h} />
            ))}
          </div>
        )}
      </section>

      {/* Fix proposal */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Wrench className="h-3 w-3" /> Fix Proposal
        </h4>
        {fix ? (
          <FixProposalView fix={fix} />
        ) : (
          <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/50 p-3 text-center text-[11px] text-zinc-600">
            No fix proposed yet.
          </div>
        )}
      </section>

      {/* Pull request */}
      {incident.pr_url && (
        <section>
          <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
            <GitPullRequest className="h-3 w-3" /> Pull Request
          </h4>
          <a
            href={incident.pr_url}
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-2 rounded-md border border-cyan-500/30 bg-cyan-500/5 px-3 py-2 text-[12px] text-cyan-300 hover:bg-cyan-500/10"
          >
            <GitPullRequest className="h-3.5 w-3.5" />
            <Mono className="text-[11px]">
              #{incident.pr_number} · {incident.pr_repo}
            </Mono>
            <ChevronRight className="ml-auto h-3 w-3" />
          </a>
        </section>
      )}

      {/* Agent trace */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Activity className="h-3 w-3" /> Agent Trace ({trace.length})
        </h4>
        {trace.length === 0 ? (
          <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/50 p-3 text-center text-[11px] text-zinc-600">
            No agent trace events yet.
          </div>
        ) : (
          <div className="space-y-2 rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
            {trace.map((entry, i) => (
              <TraceEntry key={i} entry={entry} idx={i} />
            ))}
          </div>
        )}
      </section>

      {/* Beta distribution */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          Beta Distribution (confidence prior)
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
          <div className="grid grid-cols-2 gap-3 text-[11px]">
            <div>
              <span className="text-zinc-500">α (alpha)</span>
              <Mono className="ml-2 text-zinc-300">{incident.beta_alpha.toFixed(2)}</Mono>
            </div>
            <div>
              <span className="text-zinc-500">β (beta)</span>
              <Mono className="ml-2 text-zinc-300">{incident.beta_beta.toFixed(2)}</Mono>
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}

export function IncidentsSection() {
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [serviceFilter, setServiceFilter] = useState<string>("all");
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const fetchIncident = useSentinelStore((s) => s.fetchIncident);
  const selectedId = useSentinelStore((s) => s.selectedIncidentId);
  const setSelectedIncident = useSentinelStore((s) => s.setSelectedIncident);
  const drawerOpen = useSentinelStore((s) => s.drawerOpen);
  const setDrawerOpen = useSentinelStore((s) => s.setDrawerOpen);

  const [detailLoading, setDetailLoading] = useState(false);

  // Fetch full incident detail when one is selected — list view may have
  // a partial state object.
  useEffect(() => {
    if (!selectedId) return;
    setDetailLoading(true);
    fetchIncident(selectedId).finally(() => setDetailLoading(false));
  }, [selectedId, fetchIncident]);

  const services = useMemo(
    () => ["all", ...Array.from(new Set(incidents.map((i) => i.cluster?.service).filter(Boolean) as string[]))],
    [incidents],
  );

  const filtered = useMemo(() => {
    return incidents.filter((i) => {
      if (statusFilter !== "all" && i.status !== statusFilter) return false;
      if (serviceFilter !== "all" && i.cluster?.service !== serviceFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          i.incident_id.toLowerCase().includes(q) ||
          (i.cluster?.service ?? "").toLowerCase().includes(q) ||
          (i.cluster?.summary ?? "").toLowerCase().includes(q) ||
          i.status.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [incidents, search, statusFilter, serviceFilter]);

  const selected = incidents.find((i) => i.incident_id === selectedId) ?? null;

  return (
    <div className="p-6">
      <Panel>
        {/* Filter bar */}
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-4 py-3">
          <div className="relative min-w-[200px] flex-1">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-zinc-500" />
            <Input
              placeholder="Search incidents, services, summaries..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="h-8 bg-zinc-900 pl-8 font-mono text-xs"
            />
          </div>
          <Select value={statusFilter} onValueChange={setStatusFilter}>
            <SelectTrigger size="sm" className="h-8 w-[150px] border-zinc-800 bg-zinc-900 font-mono text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {statusOptions.map((o) => (
                <SelectItem key={o.value} value={o.value} className="font-mono text-xs">{o.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={serviceFilter} onValueChange={setServiceFilter}>
            <SelectTrigger size="sm" className="h-8 w-[170px] border-zinc-800 bg-zinc-900 font-mono text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {services.map((s) => (
                <SelectItem key={s} value={s} className="font-mono text-xs">{s === "all" ? "All services" : s}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Badge variant="outline" className="h-8 gap-1 border-zinc-700 bg-zinc-900 px-2 font-mono text-[11px] text-zinc-400">
            <Filter className="h-3 w-3" />
            {filtered.length} / {incidents.length}
          </Badge>
        </div>

        {/* Loading / error / empty / table */}
        {loading && incidents.length === 0 ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-10 animate-pulse rounded bg-zinc-800/60" />
            ))}
          </div>
        ) : error && incidents.length === 0 ? (
          <div className="p-4">
            <ErrorState message={error} onRetry={fetchIncidents} />
          </div>
        ) : incidents.length === 0 ? (
          <div className="p-4">
            <EmptyState
              title="No incidents yet"
              description="When Sentinel detects an anomaly (log pattern, pod crash, or Alertmanager webhook), an incident will appear here."
              icon={Inbox}
            />
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-8 text-center text-xs text-zinc-500">
            No incidents match your filters.
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">ID</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Severity</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Updated</TableHead>
                  <TableHead className="h-8 px-3" />
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
                    <TableCell className="px-3 py-2">
                      <div className="flex flex-col">
                        <span className="text-xs text-zinc-200">{inc.cluster?.service ?? "—"}</span>
                        <Mono className="text-[10px] text-zinc-500">{inc.cluster?.namespace ?? "—"}</Mono>
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2"><StatusBadge status={inc.status} /></TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-300">{inc.cluster?.severity ?? "—"}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      {inc.pr_number ? (
                        <Mono className="text-[11px] text-cyan-400">#{inc.pr_number}</Mono>
                      ) : (
                        <span className="text-[11px] text-zinc-600">—</span>
                      )}
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeMDHM(inc.updated_at)}</Mono>
                      <div className="text-[10px] text-zinc-600">{timeAgo(inc.updated_at)}</div>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-right">
                      <ChevronRight className="h-3.5 w-3.5 text-zinc-600" />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>

      {/* Full-screen incident detail (replaces the old narrow drawer) */}
      <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
        <SheetContent side="right" className="w-screen overflow-y-auto border-zinc-800 bg-zinc-950 p-0 sm:max-w-none scrollbar-thin">
          {selected && (
            <>
              <SheetHeader className="border-b border-zinc-800 p-4">
                <div className="flex items-start justify-between pr-6">
                  <div className="flex flex-col gap-1.5">
                    <SheetTitle className="flex items-center gap-2 font-mono text-base text-zinc-50">
                      <AlertTriangle className="h-4 w-4 text-amber-400" />
                      {selected.incident_id.slice(0, 8)}
                    </SheetTitle>
                    <div className="flex flex-wrap items-center gap-2">
                      <StatusBadge status={selected.status} />
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-300">
                        {selected.cluster?.service ?? "—"}
                      </Badge>
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                        {selected.cluster?.namespace ?? "—"}
                      </Badge>
                      {selected.pr_number && (
                        <Badge variant="outline" className="font-mono text-[10px] border-cyan-500/30 bg-cyan-500/10 text-cyan-400">
                          <GitPullRequest className="h-3 w-3" /> PR #{selected.pr_number}
                        </Badge>
                      )}
                    </div>
                  </div>
                </div>
              </SheetHeader>
              <IncidentDetail incident={selected} loading={detailLoading} />
              <div className="border-t border-zinc-800 p-4">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 border-zinc-700 bg-zinc-900 text-xs text-zinc-300 hover:bg-zinc-800"
                  onClick={() => setDrawerOpen(false)}
                >
                  Close
                </Button>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
