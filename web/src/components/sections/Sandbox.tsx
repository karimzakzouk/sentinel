"use client";

import { Fragment, useMemo, useState } from "react";
import {
  FlaskConical,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  XCircle,
  AlertTriangle,
  Terminal,
  Clock,
} from "lucide-react";
import { listIncidents } from "@/lib/api";
import type { Incident } from "@/lib/types";
import { useFetch } from "@/hooks/use-fetch";
import { useSSE } from "@/hooks/use-sse";
import { cn, formatDuration, timeAgo, shortId } from "@/lib/utils";
import {
  Panel,
  Skeleton,
  ErrorState,
  EmptyState,
  SectionHeader,
} from "@/components/ui-helpers";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

interface SandboxRun {
  incidentId: string;
  service: string;
  namespace: string;
  levelsPassed: number;
  levelsTotal: number;
  durationSec: number | null;
  infraError: boolean;
  logs: string;
  status: "passed" | "failed" | "partial";
  timestamp: string;
}

function extractSandboxRuns(incidents: Incident[]): SandboxRun[] {
  const runs: SandboxRun[] = [];
  for (const inc of incidents) {
    const trace = inc.state?.agent_trace || [];
    let currentLevels: Array<{ level: number; pass: boolean }> = [];
    let logs = "";
    let duration: number | null = null;
    let infraError = false;
    let ts = inc.updated_at || inc.created_at;

    for (const ev of trace) {
      const phase = String(
        ev.phase || ev.type || ev.step || ev.agent || "",
      ).toLowerCase();
      const isSandbox =
        phase.includes("sandbox") ||
        phase.includes("verif") ||
        "level" in ev ||
        "sandbox_level" in ev;

      if (isSandbox) {
        const level =
          (ev.level as number) ??
          (ev.sandbox_level as number) ??
          (typeof ev.step === "string" && /^l?[1-5]$/i.test(ev.step as string)
            ? parseInt(String(ev.step).replace(/\D/g, ""), 10)
            : undefined);
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
          currentLevels.push({ level, pass: fail ? false : pass });
        }
        const evLogs =
          (ev.logs as string) ||
          (ev.pod_logs as string) ||
          (ev.output as string) ||
          (ev.stdout as string);
        if (evLogs && typeof evLogs === "string") {
          logs = evLogs;
        }
        const dur =
          (ev.duration_sec as number) ??
          (ev.duration_ms as number) != null
            ? (ev.duration_ms as number) / 1000
            : (ev.elapsed as number);
        if (dur != null) duration = dur;
        if (
          ev.infra_error === true ||
          (typeof ev.error === "string" && ev.error.toLowerCase().includes("infra"))
        ) {
          infraError = true;
        }
        if (ev.timestamp) ts = String(ev.timestamp);
      }
    }

    if (currentLevels.length > 0 || logs) {
      const passed = currentLevels.filter((l) => l.pass).length;
      const total = currentLevels.length;
      const status: SandboxRun["status"] =
        total === 0 ? "failed" : passed === total ? "passed" : "partial";
      runs.push({
        incidentId: inc.incident_id,
        service: inc.cluster?.service || "unknown",
        namespace: inc.cluster?.namespace || "default",
        levelsPassed: passed,
        levelsTotal: total,
        durationSec: duration,
        infraError,
        logs,
        status,
        timestamp: ts,
      });
    }
  }
  return runs.sort(
    (a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
  );
}

export function Sandbox({ refreshKey }: { refreshKey: number }) {
  const { events } = useSSE();
  const { data, loading, error, refetch } = useFetch<Incident[]>(
    () => listIncidents({ last: "168h" }),
    [refreshKey, events.length],
    { intervalMs: 30000 },
  );

  const runs = useMemo(() => extractSandboxRuns(data || []), [data]);
  const [expanded, setExpanded] = useState<string | null>(null);

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Sandbox"
        description="Verification runs with progressive level gates (L1–L5)"
      />

      {/* Summary */}
      <div className="grid grid-cols-3 gap-3">
        <SummaryCard
          label="Total Runs"
          value={runs.length}
          icon={FlaskConical}
          accent="cyan"
        />
        <SummaryCard
          label="Passed"
          value={runs.filter((r) => r.status === "passed").length}
          icon={CheckCircle2}
          accent="emerald"
        />
        <SummaryCard
          label="Infra Errors"
          value={runs.filter((r) => r.infraError).length}
          icon={AlertTriangle}
          accent="rose"
        />
      </div>

      <Panel title="Recent Runs" bodyClassName="p-0">
        {loading && !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : error ? (
          <div className="p-4">
            <ErrorState message={error} onRetry={refetch} />
          </div>
        ) : runs.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={FlaskConical}
              title="No sandbox runs yet"
              description="Sandbox verifications appear here once Sentinel runs a fix through the L1–L5 verification gates."
            />
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-sentinel">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="w-8 text-muted-foreground" />
                  <TableHead className="text-muted-foreground">Status</TableHead>
                  <TableHead className="text-muted-foreground">Incident</TableHead>
                  <TableHead className="text-muted-foreground">Service</TableHead>
                  <TableHead className="text-muted-foreground">Namespace</TableHead>
                  <TableHead className="text-muted-foreground">Levels</TableHead>
                  <TableHead className="text-muted-foreground">Duration</TableHead>
                  <TableHead className="text-muted-foreground">Infra</TableHead>
                  <TableHead className="text-muted-foreground">When</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((run, i) => {
                  const key = `${run.incidentId}-${i}`;
                  const isOpen = expanded === key;
                  return (
                    <Fragment key={key}>
                      <TableRow
                        onClick={() => setExpanded(isOpen ? null : key)}
                        className="cursor-pointer border-zinc-800"
                      >
                        <TableCell className="text-muted-foreground">
                          {isOpen ? (
                            <ChevronDown className="size-4" />
                          ) : (
                            <ChevronRight className="size-4" />
                          )}
                        </TableCell>
                        <TableCell>
                          <SandboxStatusBadge status={run.status} />
                        </TableCell>
                        <TableCell className="font-mono text-xs text-cyan-400">
                          {shortId(run.incidentId)}
                        </TableCell>
                        <TableCell className="font-mono text-xs text-zinc-200">
                          {run.service}
                        </TableCell>
                        <TableCell className="font-mono text-xs text-muted-foreground">
                          {run.namespace}
                        </TableCell>
                        <TableCell>
                          <LevelDots passed={run.levelsPassed} total={run.levelsTotal} />
                        </TableCell>
                        <TableCell className="whitespace-nowrap font-mono text-xs text-zinc-300">
                          {formatDuration(run.durationSec)}
                        </TableCell>
                        <TableCell>
                          {run.infraError ? (
                            <span className="inline-flex items-center gap-1 text-[10px] text-rose-400">
                              <AlertTriangle className="size-3" /> yes
                            </span>
                          ) : (
                            <span className="text-[10px] text-muted-foreground">no</span>
                          )}
                        </TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {timeAgo(run.timestamp)}
                        </TableCell>
                      </TableRow>
                      {isOpen && (
                        <TableRow className="border-zinc-800">
                          <TableCell colSpan={9} className="bg-zinc-950/50 p-4">
                            <PodLogs run={run} />
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>
    </div>
  );
}

function SandboxStatusBadge({ status }: { status: SandboxRun["status"] }) {
  const map = {
    passed: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
    partial: "border-amber-500/30 bg-amber-500/10 text-amber-400",
    failed: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  };
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide",
        map[status],
      )}
    >
      {status === "passed" && <CheckCircle2 className="size-2.5" />}
      {status === "failed" && <XCircle className="size-2.5" />}
      {status}
    </span>
  );
}

function LevelDots({ passed, total }: { passed: number; total: number }) {
  if (total === 0)
    return <span className="text-[10px] text-muted-foreground">—</span>;
  return (
    <div className="flex items-center gap-1">
      {Array.from({ length: total }).map((_, i) => (
        <span
          key={i}
          className={cn(
            "size-2 rounded-full",
            i < passed ? "bg-emerald-500" : "bg-rose-500",
          )}
          title={`L${i + 1}: ${i < passed ? "pass" : "fail"}`}
        />
      ))}
      <span className="ml-1 font-mono text-[10px] text-muted-foreground">
        {passed}/{total}
      </span>
    </div>
  );
}

function PodLogs({ run }: { run: SandboxRun }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-muted-foreground">
        <Terminal className="size-3.5 text-cyan-400" />
        Pod logs
        {run.durationSec != null && (
          <span className="flex items-center gap-1">
            <Clock className="size-3" />
            {formatDuration(run.durationSec)}
          </span>
        )}
      </div>
      {run.logs ? (
        <pre className="max-h-72 overflow-auto rounded-md border border-zinc-800 bg-black/60 p-3 font-mono text-[11px] leading-relaxed text-zinc-300 scrollbar-sentinel">
          {run.logs.length > 2048
            ? run.logs.slice(-2048)
            : run.logs}
        </pre>
      ) : (
        <div className="rounded-md border border-dashed border-zinc-800 px-3 py-6 text-center text-xs text-muted-foreground">
          No pod logs captured for this run.
        </div>
      )}
    </div>
  );
}

function SummaryCard({
  label,
  value,
  icon: Icon,
  accent,
}: {
  label: string;
  value: number;
  icon: React.ComponentType<{ className?: string }>;
  accent: "cyan" | "emerald" | "rose";
}) {
  const colors = {
    cyan: "text-cyan-400 border-cyan-500/20",
    emerald: "text-emerald-400 border-emerald-500/20",
    rose: "text-rose-400 border-rose-500/20",
  };
  return (
    <div className={cn("flex items-center gap-3 rounded-xl border bg-card p-4", colors[accent])}>
      <Icon className={cn("size-5", colors[accent].split(" ")[0])} />
      <div>
        <div className={cn("font-mono text-xl font-bold tabular-nums", colors[accent].split(" ")[0])}>
          {value}
        </div>
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
      </div>
    </div>
  );
}
