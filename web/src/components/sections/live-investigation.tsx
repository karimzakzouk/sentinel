"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Panel,
  PanelHeader,
  PulseDot,
  Mono,
  ConfidenceBar,
  EmptyState,
  ErrorState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useSentinelStore } from "@/lib/store";
import type { Incident } from "@/lib/types";
import {
  CheckCircle2,
  Circle,
  Loader2,
  GitPullRequest,
  Brain,
  Clock,
  Radio,
  Play,
  XCircle,
  AlertCircle,
} from "lucide-react";
import { cn } from "@/lib/utils";

// Pipeline phases inferred from agent_trace entries. The backend's trace
// entries use `{"agent": "investigator", ...}` (not `phase`), so we map by
// agent name. The investigation can fast-path skip evidence gatherer, and
// may have no test_generator entry — both are fine.
const PIPELINE_PHASES = [
  "Detection",
  "Investigation",
  "Evidence",
  "Analysis",
  "Governance",
  "Execution",
  "Sandbox",
  "PR",
  "Auto-Merge",
] as const;
type Phase = (typeof PIPELINE_PHASES)[number];

const AGENT_TO_PHASE: Record<string, Phase> = {
  investigator: "Investigation",
  evidence_gatherer: "Evidence",
  analyst: "Analysis",
  fix_proposer: "Governance",
  test_generator: "Sandbox",
  sandbox: "Sandbox",
  executor: "Execution",
  pr_opener: "PR",
  automerger: "Auto-Merge",
};

function phaseFromEntry(entry: Incident["state"]["agent_trace"][number]): Phase | null {
  const agent = typeof entry.agent === "string" ? entry.agent : "";
  if (AGENT_TO_PHASE[agent]) return AGENT_TO_PHASE[agent];
  const phase = typeof entry.phase === "string" ? entry.phase.toLowerCase() : "";
  for (const p of PIPELINE_PHASES) {
    if (phase.includes(p.toLowerCase())) return p;
  }
  return null;
}

function computePhaseStatus(
  phase: Phase,
  trace: Incident["state"]["agent_trace"],
  incident: Incident,
): "pending" | "running" | "done" | "skipped" {
  const entriesForPhase = trace.filter((e) => phaseFromEntry(e) === phase);
  if (entriesForPhase.length === 0) {
    // If the incident has progressed past this phase, mark as skipped.
    const order: Phase[] = [...PIPELINE_PHASES];
    const reachedIdx = order.findIndex((p) => trace.some((e) => phaseFromEntry(e) === p));
    if (reachedIdx === -1) {
      return phase === "Detection" ? "done" : "pending";
    }
    const thisIdx = order.indexOf(phase);
    if (thisIdx < reachedIdx) return "skipped";
    return "pending";
  }
  // Check the latest entry for this phase.
  const last = entriesForPhase[entriesForPhase.length - 1];
  if (!last) return "pending";
  const status = typeof last.status === "string" ? last.status : "";
  if (status === "running" || status === "in_progress") return "running";
  if (status === "failed" || status === "error") return "done";
  if (status === "completed" || status === "ok" || status === "success") return "done";
  // Terminal incident statuses imply all reached phases are done.
  if (["verified", "failed", "rejected", "escalated", "resolved"].includes(incident.status)) {
    return "done";
  }
  return "done";
}

function PhaseStep({
  phase,
  status,
  isLast,
}: {
  phase: Phase;
  status: "pending" | "running" | "done" | "skipped";
  isLast: boolean;
}) {
  const icon =
    status === "done" ? <CheckCircle2 className="h-4 w-4 text-emerald-400" /> :
    status === "running" ? <Loader2 className="h-4 w-4 animate-spin text-cyan-400" /> :
    status === "skipped" ? <Circle className="h-4 w-4 text-zinc-700" /> :
    <Circle className="h-4 w-4 text-zinc-600" />;
  return (
    <div className="flex items-center gap-3">
      <div className="flex flex-col items-center">
        {icon}
        {!isLast && (
          <div className={cn("mt-1 h-6 w-px", status === "done" ? "bg-emerald-500/30" : "bg-zinc-800")} />
        )}
      </div>
      <div className="pb-4">
        <div className={cn("text-[11px] font-medium", status === "pending" ? "text-zinc-500" : status === "skipped" ? "text-zinc-700" : "text-zinc-200")}>
          {phase}
        </div>
        {status === "running" && <div className="text-[10px] text-cyan-400">in progress…</div>}
        {status === "skipped" && <div className="text-[10px] text-zinc-600">not needed</div>}
      </div>
    </div>
  );
}

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

function FixDiffViewer({ incident }: { incident: Incident }) {
  const fix = incident.state?.fix_proposal;
  if (!fix || fix.files_to_change.length === 0) {
    return (
      <div className="px-4 py-6 text-center text-xs text-zinc-600">
        No fix proposal yet — the fix proposer agent hasn't run.
      </div>
    );
  }
  return (
    <div className="space-y-3 p-4">
      {fix.files_to_change.map((fc, i) => (
        <div key={`${fc.path}-${i}`} className="rounded-md border border-zinc-800 bg-zinc-950">
          <div className="border-b border-zinc-800 px-3 py-1.5 font-mono text-[11px] text-cyan-400">{fc.path}</div>
          {fc.new_content ? (
            <pre className="max-h-60 overflow-auto p-3 font-mono text-[11px] leading-relaxed text-zinc-300 scrollbar-thin">{fc.new_content}</pre>
          ) : fc.changes && fc.changes.length > 0 ? (
            <div className="space-y-2 p-3">
              {fc.changes.map((c, j) => (
                <div key={j} className="space-y-1">
                  <div className="rounded bg-rose-500/10 px-2 py-1 font-mono text-[11px] text-rose-300">- {c.search}</div>
                  <div className="rounded bg-emerald-500/10 px-2 py-1 font-mono text-[11px] text-emerald-300">+ {c.replace}</div>
                </div>
              ))}
            </div>
          ) : (
            <div className="px-3 py-2 text-[11px] text-zinc-600">No content</div>
          )}
        </div>
      ))}
    </div>
  );
}

// Trigger dialog — replaces v5's window.prompt / window.alert. Uses the
// shadcn Dialog primitive so it's keyboard-accessible and screen-reader
// friendly.
function TriggerDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const triggerInvestigation = useSentinelStore((s) => s.triggerInvestigation);
  const [service, setService] = useState("");
  const [namespace, setNamespace] = useState("default");
  const [reason, setReason] = useState("manual trigger");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const reset = () => {
    setService("");
    setNamespace("default");
    setReason("manual trigger");
    setError(null);
    setSubmitting(false);
  };

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!service.trim()) {
      setError("Service is required");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await triggerInvestigation(service.trim(), namespace.trim() || "default", reason.trim() || "manual trigger");
      reset();
      onOpenChange(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to trigger investigation");
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) reset();
        onOpenChange(v);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Trigger Investigation</DialogTitle>
          <DialogDescription>
            Manually launch a Sentinel investigation for a service. The new incident will appear at the top of the activity feed.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="trig-service" className="text-[11px] uppercase tracking-wider text-zinc-500">Service</Label>
            <Input
              id="trig-service"
              value={service}
              onChange={(e) => setService(e.target.value)}
              placeholder="checkout-api"
              className="h-9 bg-zinc-950 font-mono text-xs"
              autoFocus
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="trig-ns" className="text-[11px] uppercase tracking-wider text-zinc-500">Namespace</Label>
            <Input
              id="trig-ns"
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              placeholder="default"
              className="h-9 bg-zinc-950 font-mono text-xs"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="trig-reason" className="text-[11px] uppercase tracking-wider text-zinc-500">Reason</Label>
            <Input
              id="trig-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="manual trigger"
              className="h-9 bg-zinc-950 font-mono text-xs"
            />
          </div>
          {error && (
            <div className="flex items-center gap-2 rounded-md border border-rose-500/30 bg-rose-500/5 p-2">
              <AlertCircle className="h-3.5 w-3.5 shrink-0 text-rose-400" />
              <p className="font-mono text-[11px] text-rose-300">{error}</p>
            </div>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" size="sm" className="bg-cyan-500 text-zinc-950 hover:bg-cyan-400" disabled={submitting}>
              {submitting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
              {submitting ? "Triggering…" : "Trigger"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function LiveInvestigationSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const sseConnected = useSentinelStore((s) => s.sseConnected);
  const [triggerOpen, setTriggerOpen] = useState(false);

  const active = useMemo(() => {
    return incidents
      .filter((i) =>
        ["investigating", "executing", "fix_proposed", "approved", "root_caused", "governance_review", "detected"].includes(i.status),
      )
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())[0];
  }, [incidents]);

  // Auto-refresh while there's an active investigation so the pipeline
  // stepper advances without the user manually clicking refresh. Silent —
  // we never want the dashboard to flicker here.
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => fetchIncidents({ silent: true }), 5000);
    return () => window.clearInterval(id);
  }, [active, fetchIncidents]);

  if (loading && incidents.length === 0) {
    return (
      <div className="space-y-3 p-6">
        <div className="h-20 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
          <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
        </div>
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
    <div className="space-y-3 p-6">
      <TriggerDialog open={triggerOpen} onOpenChange={setTriggerOpen} />

      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Radio className="h-4 w-4 text-cyan-400" />
          <h2 className="text-sm font-semibold text-zinc-100">Live Pipeline</h2>
          {sseConnected ? (
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
              <PulseDot color="bg-emerald-400" className="mr-1 h-1.5 w-1.5" /> streaming
            </Badge>
          ) : (
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-500">
              disconnected
            </Badge>
          )}
        </div>
        <Button size="sm" className="bg-cyan-500 text-zinc-950 hover:bg-cyan-400" onClick={() => setTriggerOpen(true)}>
          <Play className="h-3.5 w-3.5" /> Trigger investigation
        </Button>
      </div>

      {!active ? (
        <EmptyState
          title="No active investigation"
          description="Trigger a new investigation to watch the agent pipeline advance in real time. Recent investigations are visible in the Incidents tab."
          icon={Radio}
        />
      ) : (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-12">
          <Panel className="lg:col-span-4">
            <PanelHeader title="Pipeline" sub={active.cluster?.service ?? "—"} />
            <div className="p-4">
              <div className="mb-3 flex items-center gap-2 text-[11px]">
                <Mono className="text-cyan-400">{active.incident_id.slice(0, 8)}</Mono>
                <Badge variant="outline" className="font-mono text-[10px] border-amber-500/30 bg-amber-500/10 text-amber-400">
                  {active.status}
                </Badge>
              </div>
              <div>
                {PIPELINE_PHASES.map((phase, i) => (
                  <PhaseStep
                    key={phase}
                    phase={phase}
                    status={computePhaseStatus(phase, active.state?.agent_trace ?? [], active)}
                    isLast={i === PIPELINE_PHASES.length - 1}
                  />
                ))}
              </div>
            </div>
          </Panel>

          <Panel className="lg:col-span-8">
            <PanelHeader
              title="Agent Trace"
              sub={`${active.state?.agent_trace?.length ?? 0} events`}
              right={
                <div className="flex items-center gap-2 font-mono text-[10px] text-zinc-500">
                  <Clock className="h-3 w-3" />
                  {new Date(active.updated_at).toLocaleTimeString("en-US", { hour12: false })}
                </div>
              }
            />
            <div className="max-h-80 space-y-1 overflow-y-auto p-4 scrollbar-thin">
              {(active.state?.agent_trace ?? []).length === 0 ? (
                <div className="py-6 text-center text-xs text-zinc-600">No trace events yet — the agent hasn't started.</div>
              ) : (
                (active.state?.agent_trace ?? []).map((entry, i) => <TraceEntry key={i} entry={entry} idx={i} />)
              )}
            </div>
          </Panel>

          <Panel className="lg:col-span-6">
            <PanelHeader title="Hypotheses" sub={`${active.state?.hypotheses?.length ?? 0} candidates`} />
            <div className="space-y-2 p-4">
              {(active.state?.hypotheses ?? []).length === 0 ? (
                <div className="py-6 text-center text-xs text-zinc-600">No hypotheses yet — the investigator hasn't run.</div>
              ) : (
                (active.state?.hypotheses ?? []).map((h) => (
                  <div key={h.h_id} className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5">
                    <div className="flex items-center justify-between">
                      <Mono className="text-[11px] text-cyan-400">{h.h_id}</Mono>
                      <div className="flex items-center gap-2">
                        {h.falsified ? (
                          <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                            <XCircle className="h-3 w-3" /> falsified
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                            <CheckCircle2 className="h-3 w-3" /> active
                          </Badge>
                        )}
                        <ConfidenceBar value={h.confidence} />
                      </div>
                    </div>
                    <p className="mt-1.5 text-[11px] leading-relaxed text-zinc-300">{h.statement}</p>
                  </div>
                ))
              )}
            </div>
          </Panel>

          <Panel className="lg:col-span-6">
            <PanelHeader
              title="Root Cause"
              sub={active.state?.root_cause ? `${Math.round(active.state.root_cause.confidence * 100)}% confidence` : "not yet determined"}
              right={active.state?.root_cause && <Brain className="h-3.5 w-3.5 text-cyan-400" />}
            />
            <div className="p-4">
              {active.state?.root_cause ? (
                <div className="space-y-2">
                  <p className="text-[12px] leading-relaxed text-zinc-200">{active.state.root_cause.statement}</p>
                  <div className="flex items-center justify-between border-t border-zinc-800 pt-2 text-[11px]">
                    <span className="text-zinc-500">Category</span>
                    <Mono className="text-zinc-300">{active.state.root_cause.category}</Mono>
                  </div>
                  <ConfidenceBar value={active.state.root_cause.confidence} className="border-t border-zinc-800 pt-2" />
                </div>
              ) : (
                <div className="py-6 text-center text-xs text-zinc-600">Root cause hasn't been determined yet.</div>
              )}
            </div>
          </Panel>

          <Panel className="lg:col-span-12">
            <PanelHeader
              title="Fix Diff"
              sub={active.state?.fix_proposal ? `${active.state.fix_proposal.fix_type} · ${active.state.fix_proposal.files_to_change.length} file(s)` : "no fix yet"}
              right={
                active.pr_url && (
                  <a href={active.pr_url} target="_blank" rel="noreferrer" className="flex items-center gap-1 text-[11px] text-cyan-400 hover:text-cyan-300">
                    <GitPullRequest className="h-3 w-3" /> PR #{active.pr_number}
                  </a>
                )
              }
            />
            <FixDiffViewer incident={active} />
          </Panel>
        </div>
      )}
    </div>
  );
}
