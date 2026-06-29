"use client";

import { useMemo, useState } from "react";
import {
  AlertOctagon,
  ExternalLink,
  GitBranch,
  Search,
  Filter,
  FileCode,
  Clock,
  CheckCircle2,
  XCircle,
} from "lucide-react";
import { listIncidents } from "@/lib/api";
import type { Incident } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import {
  cn,
  timeAgo,
  shortId,
  formatConfidence,
  truncate,
} from "@/lib/utils";
import { Panel, Skeleton, ErrorState, EmptyState, SectionHeader } from "@/components/ui-helpers";
import { StatusBadge } from "@/components/StatusBadge";
import { ConfidenceBar } from "@/components/ConfidenceBar";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const STATUS_FILTERS = ["all", "investigating", "diagnosed", "fix_proposed", "verifying", "merged", "resolved", "reverted", "failed"];

export function Incidents({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();
  const [statusFilter, setStatusFilter] = useState("all");
  const [serviceFilter, setServiceFilter] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const { data, loading, error, refetch } = useFetch<Incident[]>(
    () => listIncidents({ last: "168h" }),
    [refreshKey, events.length],
    { intervalMs: 15000 },
  );

  const services = useMemo(() => {
    const set = new Set<string>();
    (data || []).forEach((i) => {
      if (i.cluster?.service) set.add(i.cluster.service);
    });
    return Array.from(set).sort();
  }, [data]);

  const filtered = useMemo(() => {
    return (data || []).filter((i) => {
      if (statusFilter !== "all" && (i.status || "").toLowerCase() !== statusFilter)
        return false;
      if (serviceFilter && i.cluster?.service !== serviceFilter) return false;
      return true;
    });
  }, [data, statusFilter, serviceFilter]);

  const selected = useMemo(
    () => (data || []).find((i) => i.incident_id === selectedId) || null,
    [data, selectedId],
  );

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Incidents"
        description="Autonomous investigations across your services"
      />

      <Panel bodyClassName="p-0">
        {/* Filters */}
        <div className="flex flex-col gap-3 border-b border-zinc-800 p-3 sm:flex-row sm:items-center">
          <div className="flex items-center gap-2 overflow-x-auto scrollbar-sentinel">
            <Filter className="size-3.5 shrink-0 text-muted-foreground" />
            {STATUS_FILTERS.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => setStatusFilter(s)}
                className={cn(
                  "shrink-0 rounded-md border px-2.5 py-1 text-[11px] font-medium uppercase tracking-wide transition-colors",
                  statusFilter === s
                    ? "border-cyan-500/40 bg-cyan-500/10 text-cyan-400"
                    : "border-zinc-800 text-muted-foreground hover:text-zinc-200",
                )}
              >
                {s}
              </button>
            ))}
          </div>
          <div className="relative sm:ml-auto sm:w-56">
            <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <select
              value={serviceFilter}
              onChange={(e) => setServiceFilter(e.target.value)}
              className="h-9 w-full rounded-md border border-zinc-800 bg-zinc-900 pl-8 pr-2 text-sm text-zinc-200 outline-none focus:border-cyan-500/50"
            >
              <option value="">All services</option>
              {services.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* Table */}
        {loading && !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : error ? (
          <div className="p-4">
            <ErrorState message={error} onRetry={refetch} />
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={AlertOctagon}
              title="No incidents found"
              description={
                data && data.length > 0
                  ? "No incidents match the current filters."
                  : "Sentinel hasn't detected any anomalies yet. New incidents will appear here in real time."
              }
            />
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-sentinel">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="text-muted-foreground">Status</TableHead>
                  <TableHead className="text-muted-foreground">ID</TableHead>
                  <TableHead className="text-muted-foreground">Service</TableHead>
                  <TableHead className="text-muted-foreground">Summary</TableHead>
                  <TableHead className="text-muted-foreground">Severity</TableHead>
                  <TableHead className="text-muted-foreground">PR</TableHead>
                  <TableHead className="text-muted-foreground">Updated</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((inc) => (
                  <TableRow
                    key={inc.incident_id}
                    onClick={() => setSelectedId(inc.incident_id)}
                    className="cursor-pointer border-zinc-800"
                  >
                    <TableCell>
                      <StatusBadge status={inc.status} />
                    </TableCell>
                    <TableCell className="font-mono text-xs text-cyan-400">
                      {shortId(inc.incident_id)}
                    </TableCell>
                    <TableCell className="font-mono text-xs text-zinc-200">
                      {inc.cluster?.service}
                      <span className="text-muted-foreground"> / {inc.cluster?.namespace}</span>
                    </TableCell>
                    <TableCell className="max-w-xs truncate text-xs text-zinc-300">
                      {truncate(inc.cluster?.summary, 60)}
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={inc.cluster?.severity || "unknown"} />
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {inc.pr_number ? (
                        <span className="text-amber-400">#{inc.pr_number}</span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                      {timeAgo(inc.updated_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>

      {/* Detail sheet */}
      <Sheet open={!!selected} onOpenChange={(o) => !o && setSelectedId(null)}>
        <SheetContent
          side="right"
          className="w-full border-zinc-800 bg-background p-0 sm:max-w-2xl"
        >
          {selected && <IncidentDetail incident={selected} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}

// ─── Incident detail (inside the Sheet) ──────────────────────────────────────

function IncidentDetail({ incident }: { incident: Incident }) {
  const sandboxLevels = extractSandboxLevels(incident);
  const trace = incident.state?.agent_trace || [];

  return (
    <div className="flex h-full flex-col">
      <SheetHeader className="border-b border-zinc-800 pr-12">
        <SheetTitle className="flex items-center gap-2 font-mono text-base">
          <StatusBadge status={incident.status} />
          <span className="text-cyan-400">{shortId(incident.incident_id)}</span>
        </SheetTitle>
        <SheetDescription className="font-mono text-xs">
          {incident.cluster?.service} / {incident.cluster?.namespace}
          {" · "}
          {timeAgo(incident.created_at)}
        </SheetDescription>
      </SheetHeader>

      <div className="flex-1 space-y-5 overflow-y-auto scrollbar-sentinel p-4">
        {/* Cluster summary */}
        <Section title="Cluster Summary">
          <p className="text-sm leading-relaxed text-zinc-200">
            {incident.cluster?.summary || "No summary available."}
          </p>
          {incident.cluster?.anomalies?.length > 0 && (
            <div className="mt-3 space-y-1.5">
              {incident.cluster.anomalies.map((a, i) => (
                <div
                  key={i}
                  className="flex items-start gap-2 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2"
                >
                  <span className="font-mono text-[10px] uppercase text-cyan-400">
                    {a.detector}
                  </span>
                  <span className="font-mono text-[10px] text-amber-400">
                    score {a.score.toFixed(2)}
                  </span>
                  <span className="flex-1 text-xs text-zinc-300">{a.explanation}</span>
                </div>
              ))}
            </div>
          )}
        </Section>

        {/* Hypotheses */}
        <Section title="Hypotheses" count={incident.state?.hypotheses?.length}>
          {incident.state?.hypotheses?.length ? (
            <div className="space-y-2.5">
              {incident.state.hypotheses.map((h, i) => (
                <div
                  key={h.h_id || i}
                  className="rounded-md border border-zinc-800 bg-zinc-900/40 p-3"
                >
                  <div className="mb-1.5 flex items-center gap-2">
                    <span className="font-mono text-[10px] text-muted-foreground">
                      {h.h_id || `H${i + 1}`}
                    </span>
                    {h.falsified && (
                      <span className="rounded border border-rose-500/30 bg-rose-500/10 px-1.5 py-0.5 text-[9px] uppercase text-rose-400">
                        falsified
                      </span>
                    )}
                  </div>
                  <p className="mb-2 text-xs text-zinc-200">{h.statement}</p>
                  <ConfidenceBar value={h.confidence} label="confidence" />
                  {h.source_files?.length > 0 && (
                    <div className="mt-2 flex flex-wrap gap-1">
                      {h.source_files.map((f, j) => (
                        <FileChip key={j} path={f} />
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <Muted hint="No hypotheses generated yet." />
          )}
        </Section>

        {/* Root cause */}
        <Section title="Root Cause">
          {incident.state?.root_cause ? (
            <div className="rounded-md border border-violet-500/20 bg-violet-500/5 p-3">
              <div className="mb-2 flex items-center justify-between">
                <span className="rounded border border-violet-500/30 bg-violet-500/10 px-1.5 py-0.5 font-mono text-[9px] uppercase text-violet-400">
                  {incident.state.root_cause.category}
                </span>
                <ConfidenceBar
                  value={incident.state.root_cause.confidence}
                  className="w-24"
                  showValue
                />
              </div>
              <p className="text-sm leading-relaxed text-zinc-100">
                {incident.state.root_cause.statement}
              </p>
              {incident.state.root_cause.source_files?.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {incident.state.root_cause.source_files.map((f, j) => (
                    <FileChip key={j} path={f} />
                  ))}
                </div>
              )}
            </div>
          ) : (
            <Muted hint="Root cause not yet determined." />
          )}
        </Section>

        {/* Fix proposal */}
        <Section title="Fix Proposal">
          {incident.state?.fix_proposal ? (
            <div className="space-y-2.5">
              <div className="rounded-md border border-amber-500/20 bg-amber-500/5 p-3">
                <div className="mb-2 flex items-center justify-between">
                  <span className="font-mono text-[10px] uppercase text-amber-400">
                    {incident.state.fix_proposal.fix_type}
                  </span>
                  <ConfidenceBar
                    value={incident.state.fix_proposal.proposed_confidence}
                    className="w-24"
                    showValue
                  />
                </div>
                <p className="text-sm leading-relaxed text-zinc-100">
                  {incident.state.fix_proposal.description}
                </p>
                <div className="mt-2 flex items-center gap-2 font-mono text-[10px] text-muted-foreground">
                  <GitBranch className="size-3" />
                  {incident.state.fix_proposal.branch_name}
                </div>
              </div>
              {incident.state.fix_proposal.files_to_change?.length > 0 && (
                <div className="space-y-1.5">
                  <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                    Files ({incident.state.fix_proposal.files_to_change.length})
                  </span>
                  {incident.state.fix_proposal.files_to_change.map((f, i) => (
                    <div
                      key={i}
                      className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2"
                    >
                      <FileCode className="size-3.5 shrink-0 text-cyan-400" />
                      <span className="flex-1 truncate font-mono text-xs text-zinc-200">
                        {f.path}
                      </span>
                      {f.new_content ? (
                        <span className="text-[9px] uppercase text-emerald-400">full</span>
                      ) : f.changes?.length ? (
                        <span className="text-[9px] uppercase text-amber-400">
                          {f.changes.length} patch
                        </span>
                      ) : null}
                    </div>
                  ))}
                </div>
              )}
            </div>
          ) : (
            <Muted hint="No fix proposed yet." />
          )}
        </Section>

        {/* PR link */}
        {incident.pr_url && (
          <Section title="Pull Request">
            <a
              href={incident.pr_url}
              target="_blank"
              rel="noopener noreferrer"
              className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 transition-colors hover:border-cyan-500/40"
            >
              <GitBranch className="size-4 text-cyan-400" />
              <span className="flex-1 font-mono text-xs text-zinc-200">
                {incident.pr_repo}#{incident.pr_number}
              </span>
              <span className="font-mono text-[10px] text-muted-foreground">
                {incident.pr_branch}
              </span>
              <ExternalLink className="size-3.5 text-muted-foreground" />
            </a>
            {incident.auto_merge_status && (
              <div className="mt-2 flex items-center gap-2">
                <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                  auto-merge:
                </span>
                <StatusBadge status={incident.auto_merge_status} />
              </div>
            )}
          </Section>
        )}

        {/* Sandbox verification levels */}
        <Section title="Sandbox Verification">
          {sandboxLevels.length > 0 ? (
            <div className="space-y-1.5">
              {sandboxLevels.map((lvl) => (
                <div
                  key={lvl.level}
                  className="flex items-center gap-3 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2"
                >
                  <span className="font-mono text-xs text-cyan-400">L{lvl.level}</span>
                  <span className="flex-1 truncate text-xs text-zinc-300">
                    {lvl.label}
                  </span>
                  {lvl.pass ? (
                    <span className="flex items-center gap-1 text-[10px] text-emerald-400">
                      <CheckCircle2 className="size-3.5" /> pass
                    </span>
                  ) : (
                    <span className="flex items-center gap-1 text-[10px] text-rose-400">
                      <XCircle className="size-3.5" /> fail
                    </span>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <Muted hint="No sandbox verification recorded." />
          )}
        </Section>

        {/* Agent trace timeline */}
        <Section title="Agent Trace" count={trace.length}>
          {trace.length > 0 ? (
            <div className="space-y-0">
              {trace.map((ev, i) => (
                <TraceEvent key={i} ev={ev} last={i === trace.length - 1} />
              ))}
            </div>
          ) : (
            <Muted hint="No agent trace events yet." />
          )}
        </Section>
      </div>
    </div>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <section>
      <h3 className="mb-2 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        {title}
        {count != null && (
          <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-300">
            {count}
          </span>
        )}
      </h3>
      {children}
    </section>
  );
}

function Muted({ hint }: { hint: string }) {
  return (
    <p className="rounded-md border border-dashed border-zinc-800 px-3 py-3 text-xs text-muted-foreground">
      {hint}
    </p>
  );
}

function FileChip({ path }: { path: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded border border-zinc-800 bg-zinc-900/60 px-1.5 py-0.5 font-mono text-[10px] text-zinc-300">
      <FileCode className="size-2.5 text-cyan-400" />
      {path}
    </span>
  );
}

function TraceEvent({ ev, last }: { ev: Record<string, unknown>; last: boolean }) {
  const entries = Object.entries(ev);
  const phase =
    (ev.phase as string) ||
    (ev.type as string) ||
    (ev.step as string) ||
    (ev.agent as string) ||
    "event";
  const ts = (ev.timestamp as string) || (ev.time as string) || (ev.ts as string);
  const msg =
    (ev.message as string) ||
    (ev.detail as string) ||
    (ev.description as string) ||
    (ev.action as string);
  return (
    <div className="flex gap-3">
      <div className="flex flex-col items-center">
        <span className="mt-1 size-2 rounded-full border border-cyan-500/40 bg-cyan-500/20" />
        {!last && <span className="w-px flex-1 bg-zinc-800" />}
      </div>
      <div className="flex-1 pb-3">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[11px] font-medium text-cyan-400">{phase}</span>
          {ts && (
            <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
              <Clock className="size-2.5" />
              {timeAgo(ts)}
            </span>
          )}
        </div>
        {msg && <p className="mt-0.5 text-xs text-zinc-300">{msg}</p>}
        {entries.length > 0 && !msg && (
          <pre className="mt-1 overflow-x-auto rounded bg-zinc-900/60 p-2 font-mono text-[10px] text-muted-foreground scrollbar-sentinel">
            {JSON.stringify(ev, null, 2)}
          </pre>
        )}
      </div>
    </div>
  );
}

// Extract sandbox verification levels (1-5) from agent_trace events.
function extractSandboxLevels(
  incident: Incident,
): Array<{ level: number; pass: boolean; label: string }> {
  const trace = incident.state?.agent_trace || [];
  const found: Array<{ level: number; pass: boolean; label: string }> = [];
  for (const ev of trace) {
    const level =
      (ev.level as number) ??
      (ev.sandbox_level as number) ??
      (typeof ev.step === "string" && /^l?[1-5]$/i.test(ev.step as string)
        ? parseInt(String(ev.step).replace(/\D/g, ""), 10)
        : undefined);
    const phase = String(ev.phase || ev.type || ev.step || ev.agent || "").toLowerCase();
    if (level == null && !phase.includes("sandbox")) continue;
    const pass =
      ev.pass === true ||
      ev.passed === true ||
      ev.status === "pass" ||
      ev.status === "passed" ||
      ev.result === "pass" ||
      ev.success === true;
    const fail =
      ev.pass === false ||
      ev.passed === false ||
      ev.status === "fail" ||
      ev.status === "failed" ||
      ev.result === "fail";
    if (level != null && level >= 1 && level <= 5) {
      found.push({
        level,
        pass: fail ? false : pass,
        label: String(ev.name || ev.label || ev.description || ev.message || `Level ${level}`),
      });
    }
  }
  // Dedupe by level, prefer explicit pass/fail.
  const byLevel = new Map<number, { level: number; pass: boolean; label: string }>();
  for (const l of found) {
    const existing = byLevel.get(l.level);
    if (!existing) byLevel.set(l.level, l);
  }
  return Array.from(byLevel.values()).sort((a, b) => a.level - b.level);
}
