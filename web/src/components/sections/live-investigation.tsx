"use client";

import { useEffect, useMemo, useState } from "react";
import { motion } from "framer-motion";
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
import { Progress } from "@/components/ui/progress";
import { Button } from "@/components/ui/button";
import { useSentinelStore } from "@/lib/store";
import type { Incident } from "@/lib/types";
import {
  CheckCircle2,
  Circle,
  Loader2,
  GitPullRequest,
  Brain,
  Activity,
  Clock,
  DollarSign,
  Zap,
  Radio,
  Play,
} from "lucide-react";
import { cn } from "@/lib/utils";

// Pipeline phases inferred from agent_trace entries. We map each trace entry's
// `phase` field to one of these labels (case-insensitive). If no trace entries
// are present, we fall back to a single "Detection" step.
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

function tracePhases(incident: Incident): Set<string> {
  const set = new Set<string>();
  for (const entry of incident.state?.agent_trace ?? []) {
    const phase = typeof entry.phase === "string" ? entry.phase.toLowerCase() : "";
    if (phase) set.add(phase);
  }
  return set;
}

function activePhaseIndex(incident: Incident): number {
  // If incident is resolved/verified/failed, all phases done.
  if (["verified", "resolved", "failed", "rejected", "escalated"].includes(incident.status)) {
    return PIPELINE_PHASES.length;
  }
  const phases = tracePhases(incident);
  // Find the last phase we've seen in our canonical order.
  let lastSeenIdx = -1;
  for (let i = 0; i < PIPELINE_PHASES.length; i++) {
    const p = PIPELINE_PHASES[i].toLowerCase();
    if (phases.has(p)) lastSeenIdx = i;
  }
  // Current phase = last seen + 1 (in progress), or 0 if nothing yet.
  if (lastSeenIdx === -1) return 0;
  return Math.min(lastSeenIdx + 1, PIPELINE_PHASES.length - 1);
}

function PipelineStepper({ incident }: { incident: Incident }) {
  const phaseIdx = activePhaseIndex(incident);
  const progress = (phaseIdx / PIPELINE_PHASES.length) * 100;

  return (
    <Panel>
      <PanelHeader
        title="Investigation Pipeline"
        sub={`${incident.incident_id.slice(0, 8)} · ${incident.cluster?.service ?? "—"} · ${incident.cluster?.namespace ?? "—"}`}
        right={
          <Badge variant="outline" className="border-cyan-500/30 bg-cyan-500/10 font-mono text-[10px] text-cyan-300">
            <PulseDot color="bg-cyan-400" className="h-1.5 w-1.5" />
            {incident.status}
          </Badge>
        }
      />
      <div className="px-4 py-4">
        <div className="flex items-center gap-1 overflow-x-auto scrollbar-thin">
          {PIPELINE_PHASES.map((phase, i) => {
            const done = i < phaseIdx;
            const current = i === phaseIdx;
            const upcoming = i > phaseIdx;
            return (
              <div key={phase} className="flex flex-1 items-center">
                <div className="flex flex-col items-center gap-1.5">
                  <div
                    className={cn(
                      "relative flex h-8 w-8 items-center justify-center rounded-full border",
                      done && "border-emerald-500/40 bg-emerald-500/15 text-emerald-400",
                      current && "border-cyan-500 bg-cyan-500/20 text-cyan-300",
                      upcoming && "border-zinc-700 bg-zinc-900 text-zinc-600",
                    )}
                  >
                    {done ? (
                      <CheckCircle2 className="h-4 w-4" />
                    ) : current ? (
                      <>
                        <motion.span
                          animate={{ scale: [1, 1.15, 1] }}
                          transition={{ duration: 1.5, repeat: Infinity }}
                          className="absolute inset-0 rounded-full border-2 border-cyan-400/60"
                        />
                        <Loader2 className="h-4 w-4 animate-spin" />
                      </>
                    ) : (
                      <Circle className="h-4 w-4" />
                    )}
                  </div>
                  <span
                    className={cn(
                      "whitespace-nowrap text-[10px] font-medium",
                      done && "text-emerald-400",
                      current && "text-cyan-300",
                      upcoming && "text-zinc-600",
                    )}
                  >
                    {phase}
                  </span>
                </div>
                {i < PIPELINE_PHASES.length - 1 && (
                  <div className="mx-1 h-px flex-1 bg-zinc-800">
                    <div
                      className={cn(
                        "h-px transition-all duration-500",
                        done ? "bg-emerald-500/60" : current ? "bg-cyan-500/60" : "bg-transparent",
                      )}
                      style={{ width: current ? "50%" : done ? "100%" : "0%" }}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
        <div className="mt-3 flex items-center justify-between text-[11px]">
          <span className="text-zinc-500">
            Current phase:{" "}
            <span className="font-mono text-cyan-300">
              {PIPELINE_PHASES[Math.min(phaseIdx, PIPELINE_PHASES.length - 1)]}
            </span>
          </span>
          <span className="font-mono text-zinc-400">{progress.toFixed(0)}% complete</span>
        </div>
      </div>
    </Panel>
  );
}

function LiveConsole({ incident }: { incident: Incident }) {
  const trace = incident.state?.agent_trace ?? [];

  // Render trace entries as console lines — each entry contributes one or more
  // lines (phase header + message + structured fields as JSON).
  const lines = useMemo(() => {
    const out: Array<{ ts: string; phase: string; level: string; text: string }> = [];
    for (const entry of trace) {
      const phase = typeof entry.phase === "string" ? entry.phase : "—";
      const agent = typeof entry.agent === "string" ? entry.agent : "";
      const status = typeof entry.status === "string" ? entry.status : "";
      const message = typeof entry.message === "string" ? entry.message : "";
      const ts = typeof entry.ts === "string" || typeof entry.time === "string"
        ? String(entry.ts ?? entry.time)
        : incident.updated_at;

      if (message) {
        out.push({ ts, phase, level: status || phase, text: message });
      }
      // Surface structured fields (skipping ones we already used)
      for (const [k, v] of Object.entries(entry)) {
        if (["phase", "agent", "status", "message", "ts", "time"].includes(k)) continue;
        if (typeof v === "string" || typeof v === "number" || typeof v === "boolean") {
          out.push({ ts, phase, level: k, text: `${k}=${v}` });
        }
      }
      if (agent && !message) {
        out.push({ ts, phase, level: "agent", text: `agent=${agent} status=${status || "—"}` });
      }
    }
    return out;
  }, [trace, incident.updated_at]);

  return (
    <Panel className="flex flex-col">
      <PanelHeader
        title="Live Event Stream"
        sub={`${incident.incident_id.slice(0, 8)} · ${trace.length} trace entries`}
        right={
          <div className="flex items-center gap-2">
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
              <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
              {incident.status}
            </Badge>
            <span className="font-mono text-[10px] text-zinc-500">{lines.length} lines</span>
          </div>
        }
      />
      <div className="max-h-[420px] min-h-[280px] overflow-y-auto bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed scrollbar-thin">
        {lines.length === 0 ? (
          <div className="text-zinc-600">$ waiting for trace events...</div>
        ) : (
          lines.map((line, i) => (
            <motion.div
              key={i}
              initial={{ opacity: 0, x: -4 }}
              animate={{ opacity: 1, x: 0 }}
              className="flex gap-2"
            >
              <span className="shrink-0 text-zinc-700">
                {new Date(line.ts).toLocaleTimeString("en-US", { hour12: false })}
              </span>
              <span
                className={cn(
                  "shrink-0 font-semibold",
                  line.level === "completed" || line.level === "success" || line.level === "ok"
                    ? "text-emerald-400"
                    : line.level === "running" || line.level === "in_progress"
                    ? "text-cyan-400"
                    : line.level === "failed" || line.level === "error"
                    ? "text-rose-400"
                    : "text-zinc-400",
                )}
              >
                [{line.phase}]
              </span>
              <span
                className={cn(
                  "text-zinc-300",
                  line.text.includes("PASS") && "text-emerald-300 font-semibold",
                  line.text.includes("FAIL") && "text-rose-300 font-semibold",
                  line.text.includes("✓") && "text-emerald-300",
                )}
              >
                {line.text}
              </span>
            </motion.div>
          ))
        )}
        <div className="mt-1 flex gap-2">
          <span className="text-cyan-400">$</span>
          <span className="animate-blink text-zinc-400">▊</span>
        </div>
      </div>
    </Panel>
  );
}

function AgentTraceTree({ incident }: { incident: Incident }) {
  const trace = incident.state?.agent_trace ?? [];

  // Aggregate stats from the trace
  const stats = useMemo(() => {
    let tokens = 0;
    let cost = 0;
    let durationSec = 0;
    for (const entry of trace) {
      if (typeof entry.tokens === "number") tokens += entry.tokens;
      if (typeof entry.cost === "number") cost += entry.cost;
      if (typeof entry.duration_sec === "number") durationSec += entry.duration_sec;
      else if (typeof entry.duration === "string") {
        // Try to parse "1m 30s" style
        const m = entry.duration.match(/(\d+)m\s*(\d+)?s?/);
        if (m) {
          durationSec += parseInt(m[1], 10) * 60;
          if (m[2]) durationSec += parseInt(m[2], 10);
        }
      }
    }
    return { tokens, cost, durationSec };
  }, [trace]);

  const formatDur = (s: number) => {
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    const rem = s % 60;
    return rem ? `${m}m ${rem}s` : `${m}m`;
  };

  return (
    <Panel>
      <PanelHeader
        title="Agent Trace"
        sub="Pipeline phases with tokens, cost, duration"
        right={
          <div className="flex items-center gap-2 font-mono text-[10px] text-zinc-500">
            <Zap className="h-3 w-3 text-cyan-400" />
            <span className="text-cyan-300">{trace.length} events</span>
          </div>
        }
      />
      <div className="max-h-[420px] overflow-y-auto px-2 py-2 scrollbar-thin">
        {trace.length === 0 ? (
          <div className="px-4 py-8 text-center text-xs text-zinc-600">
            No trace events yet — agent will populate this as it runs.
          </div>
        ) : (
          <div className="space-y-1">
            {trace.map((entry, i) => {
              const phase = typeof entry.phase === "string" ? entry.phase : "—";
              const agent = typeof entry.agent === "string" ? entry.agent : "";
              const status = typeof entry.status === "string" ? entry.status : "";
              const duration = typeof entry.duration === "string" ? entry.duration : "";
              const tokens = typeof entry.tokens === "number" ? entry.tokens : undefined;
              const cost = typeof entry.cost === "number" ? entry.cost : undefined;
              const message = typeof entry.message === "string" ? entry.message : "";

              return (
                <div
                  key={i}
                  className="rounded px-2 py-1.5 hover:bg-zinc-800/40"
                  style={{ paddingLeft: `${8}px` }}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={cn(
                        "h-1.5 w-1.5 rounded-full",
                        status === "completed" || status === "ok" || status === "success"
                          ? "bg-emerald-400"
                          : status === "running" || status === "in_progress"
                          ? "bg-cyan-400 animate-pulse-dot"
                          : status === "failed" || status === "error"
                          ? "bg-rose-400"
                          : "bg-zinc-600",
                      )}
                    />
                    <span className="text-xs text-zinc-200">{phase}</span>
                    {agent && (
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                        {agent}
                      </Badge>
                    )}
                    <span className="ml-auto flex items-center gap-2 font-mono text-[10px] text-zinc-500">
                      {duration && <span>{duration}</span>}
                      {tokens !== undefined && <span>{tokens.toLocaleString()} tok</span>}
                      {cost !== undefined && <span className="text-cyan-400">${cost.toFixed(3)}</span>}
                    </span>
                  </div>
                  {message && (
                    <p className="mt-1 pl-3.5 font-mono text-[10.5px] leading-relaxed text-zinc-400">
                      {message}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
      <div className="border-t border-zinc-800 px-4 py-2.5">
        <div className="grid grid-cols-3 gap-3 text-[11px]">
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <Clock className="h-3 w-3" /> Duration
            </div>
            <Mono className="text-zinc-300">{stats.durationSec > 0 ? formatDur(stats.durationSec) : "—"}</Mono>
          </div>
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <Activity className="h-3 w-3" /> Tokens
            </div>
            <Mono className="text-zinc-300">
              {stats.tokens > 0 ? stats.tokens.toLocaleString() : "—"}
            </Mono>
          </div>
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <DollarSign className="h-3 w-3" /> Cost
            </div>
            <Mono className="text-cyan-300">
              {stats.cost > 0 ? `$${stats.cost.toFixed(3)}` : "—"}
            </Mono>
          </div>
        </div>
      </div>
    </Panel>
  );
}

function FixDiffViewer({ incident }: { incident: Incident }) {
  const fix = incident.state?.fix_proposal;
  if (!fix) return null;
  const firstFile = fix.files_to_change[0];

  return (
    <Panel>
      <PanelHeader
        title="Fix Proposal"
        sub={firstFile?.path ?? fix.description.slice(0, 60)}
        right={
          incident.pr_number ? (
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              <GitPullRequest className="h-3 w-3" />
              PR #{incident.pr_number}
            </Badge>
          ) : undefined
        }
      />
      <div className="space-y-3 p-4">
        <p className="text-[12px] leading-relaxed text-zinc-300">{fix.description}</p>
        {fix.files_to_change.length > 0 && (
          <div className="space-y-2">
            {fix.files_to_change.map((f, i) => (
              <div key={i} className="overflow-hidden rounded border border-zinc-800">
                <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900 px-2.5 py-1.5">
                  <Mono className="text-[11px] text-zinc-300">{f.path}</Mono>
                  <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-500">
                    {f.new_content ? `${f.new_content.length} chars` : f.changes?.length ? `${f.changes.length} edits` : "—"}
                  </Badge>
                </div>
                {f.new_content && (
                  <pre className="max-h-64 overflow-y-auto whitespace-pre-wrap bg-zinc-950 p-3 font-mono text-[10.5px] leading-relaxed text-zinc-300 scrollbar-thin">
                    {f.new_content}
                  </pre>
                )}
                {f.changes && f.changes.length > 0 && (
                  <div className="space-y-1 bg-zinc-950 p-2">
                    {f.changes.map((c, j) => (
                      <div key={j} className="rounded border border-zinc-800 p-1.5">
                        <div className="font-mono text-[10.5px] text-rose-400">- {c.search}</div>
                        <div className="mt-0.5 font-mono text-[10.5px] text-emerald-400">+ {c.replace}</div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </Panel>
  );
}

export function LiveInvestigationSection() {
  const incidents = useSentinelStore((s) => s.incidents);
  const loading = useSentinelStore((s) => s.loading);
  const error = useSentinelStore((s) => s.error);
  const fetchIncidents = useSentinelStore((s) => s.fetchIncidents);
  const triggerInvestigation = useSentinelStore((s) => s.triggerInvestigation);
  const [triggering, setTriggering] = useState(false);

  // Refresh incidents every 10s while viewing this section so the live view
  // stays current.
  useEffect(() => {
    const id = setInterval(() => fetchIncidents(), 10_000);
    return () => clearInterval(id);
  }, [fetchIncidents]);

  // Find the most recent "active" incident — investigating/executing/etc.
  const activeIncident = useMemo(() => {
    const active = incidents.filter((i) =>
      ["investigating", "executing", "fix_proposed", "approved", "root_caused"].includes(i.status),
    );
    if (active.length > 0) {
      // Most recent first
      return active.sort((a, b) =>
        new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
      )[0];
    }
    // Fallback to the most recent incident of any status
    if (incidents.length > 0) {
      return [...incidents].sort((a, b) =>
        new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
      )[0];
    }
    return null;
  }, [incidents]);

  const onTrigger = async () => {
    setTriggering(true);
    try {
      // Trigger an investigation for a placeholder service. In a real
      // deployment the user would pick the service from a dropdown, but for
      // now we prompt.
      const service = window.prompt("Service to investigate (e.g. checkout-api):");
      if (!service) return;
      const namespace = window.prompt("Namespace (default: default):") || "default";
      await triggerInvestigation(service, namespace);
    } catch (err) {
      window.alert(`Failed: ${err instanceof Error ? err.message : "unknown error"}`);
    } finally {
      setTriggering(false);
    }
  };

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
        <ErrorState message={error} onRetry={fetchIncidents} />
      </div>
    );
  }

  if (!activeIncident) {
    return (
      <div className="space-y-3 p-6">
        <EmptyState
          title="No active investigation"
          description="Trigger an investigation manually, or wait for Sentinel's autonomous detection to pick up an anomaly."
          icon={Radio}
        />
        <Panel>
          <div className="flex items-center justify-between p-4">
            <div>
              <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-300">Manual trigger</h3>
              <p className="mt-0.5 text-[11px] text-zinc-500">
                POST /admin/investigate — requires service + namespace.
              </p>
            </div>
            <Button
              size="sm"
              className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400"
              onClick={onTrigger}
              disabled={triggering}
            >
              {triggering ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
              Trigger investigation
            </Button>
          </div>
        </Panel>
      </div>
    );
  }

  const incident = activeIncident;
  const rootConfidence = incident.state?.root_cause?.confidence ?? 0;
  const elapsedMs = Date.now() - new Date(incident.created_at).getTime();
  const elapsedMin = Math.floor(elapsedMs / 60_000);
  const elapsedSec = Math.floor((elapsedMs % 60_000) / 1000);

  return (
    <div className="space-y-3 p-6">
      {/* Header strip with phase status */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Incident</div>
            <div className="mt-1 flex items-center gap-2">
              <Mono className="text-sm text-cyan-400">{incident.incident_id.slice(0, 8)}</Mono>
              <PulseDot color="bg-amber-400" className="h-2 w-2" />
            </div>
            <div className="mt-1 text-[11px] text-zinc-400">
              {incident.cluster?.service ?? "—"} · {incident.cluster?.namespace ?? "—"}
            </div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Status</div>
            <div className="mt-1 flex items-center gap-2">
              <Loader2 className="h-3.5 w-3.5 animate-spin text-cyan-400" />
              <span className="font-mono text-sm text-cyan-300">{incident.status}</span>
            </div>
            <div className="mt-2">
              <Progress value={(activePhaseIndex(incident) / PIPELINE_PHASES.length) * 100} className="h-1 bg-zinc-800" />
            </div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</div>
            <div className="mt-1"><ConfidenceBar value={rootConfidence} /></div>
            <div className="mt-1 text-[11px] text-zinc-500">autonomous threshold: 85%</div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Elapsed</div>
            <Mono className="mt-1 block text-sm text-zinc-200">
              {elapsedMin}m {elapsedSec}s
            </Mono>
            <div className="mt-1 text-[11px] text-zinc-500">
              created {new Date(incident.created_at).toLocaleTimeString("en-US", { hour12: false })}
            </div>
          </div>
        </Panel>
      </div>

      <PipelineStepper incident={incident} />

      {/* Main grid: trace tree + live console */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <AgentTraceTree incident={incident} />
        <LiveConsole incident={incident} />
      </div>

      {incident.state?.fix_proposal && <FixDiffViewer incident={incident} />}

      {/* Manual trigger always available at the bottom */}
      <Panel>
        <div className="flex items-center justify-between p-4">
          <div>
            <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-300">Trigger another investigation</h3>
            <p className="mt-0.5 text-[11px] text-zinc-500">
              POST /admin/investigate
            </p>
          </div>
          <Button
            size="sm"
            className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400"
            onClick={onTrigger}
            disabled={triggering}
          >
            {triggering ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
            Trigger
          </Button>
        </div>
      </Panel>

      {/* Hint when no truly active incident */}
      {!["investigating", "executing", "fix_proposed", "approved", "root_caused"].includes(incident.status) && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-4 py-3">
          <Brain className="h-4 w-4 shrink-0 text-amber-400" />
          <p className="text-xs text-zinc-300">
            Showing the most recent incident ({incident.status}). No investigation is currently in progress.
          </p>
        </div>
      )}
    </div>
  );
}
