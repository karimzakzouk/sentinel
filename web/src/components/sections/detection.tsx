"use client";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Panel, PanelHeader, Mono, timeAgo, PulseDot } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { detectionState } from "@/lib/mock-data";
import { useSentinel } from "@/lib/store";
import {
  Eye,
  EyeOff,
  Play,
  Pause,
  Radio,
  Activity,
  AlertTriangle,
  Server,
  Webhook,
  Clock,
} from "lucide-react";
import { cn } from "@/lib/utils";

function formatRemaining(s: number): string {
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return `${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
}

const podStatusColor: Record<string, string> = {
  Running: "text-emerald-400",
  CrashLoopBackOff: "text-rose-400",
  OOMKilled: "text-amber-400",
  Pending: "text-zinc-400",
};

export function DetectionSection() {
  const paused = useSentinel((s) => s.detectionPaused);
  const remaining = useSentinel((s) => s.detectionPauseRemaining);
  const toggle = useSentinel((s) => s.toggleDetection);

  return (
    <div className="space-y-3 p-6">
      {/* Status panel */}
      <Panel>
        <div className="flex flex-wrap items-center justify-between gap-4 px-4 py-4">
          <div className="flex items-center gap-4">
            <div
              className={cn(
                "flex h-12 w-12 items-center justify-center rounded-lg",
                paused ? "bg-amber-500/10 ring-1 ring-amber-500/30" : "bg-emerald-500/10 ring-1 ring-emerald-500/30"
              )}
            >
              {paused ? <EyeOff className="h-5 w-5 text-amber-400" /> : <Eye className="h-5 w-5 text-emerald-400" />}
            </div>
            <div>
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold text-zinc-100">Autonomous Detection</h3>
                <Badge
                  variant="outline"
                  className={cn(
                    "font-mono text-[10px]",
                    paused ? "border-amber-500/30 bg-amber-500/10 text-amber-400" : "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                  )}
                >
                  {!paused && <PulseDot color="bg-emerald-400" className="mr-1 h-1.5 w-1.5" />}
                  {paused ? "paused" : "running"}
                </Badge>
              </div>
              <p className="mt-0.5 text-[11px] text-zinc-500">
                {paused
                  ? `Detection paused — resumes in ${formatRemaining(remaining)}`
                  : "Watching 6 log streams · 4 pods · 1 alertmanager webhook"}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Select defaultValue="30">
              <SelectTrigger size="sm" className="h-8 w-[120px] border-zinc-800 bg-zinc-900 font-mono text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="15" className="font-mono text-xs">15 minutes</SelectItem>
                <SelectItem value="30" className="font-mono text-xs">30 minutes</SelectItem>
                <SelectItem value="60" className="font-mono text-xs">1 hour</SelectItem>
                <SelectItem value="240" className="font-mono text-xs">4 hours</SelectItem>
              </SelectContent>
            </Select>
            <Button
              onClick={toggle}
              size="sm"
              className={cn(
                "h-8 text-xs",
                paused ? "bg-emerald-500 text-zinc-950 hover:bg-emerald-400" : "bg-amber-500 text-zinc-950 hover:bg-amber-400"
              )}
            >
              {paused ? <Play className="h-3 w-3" /> : <Pause className="h-3 w-3" />}
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
        </div>
      </Panel>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* Active log streams */}
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Active Log Streams"
            sub="Tailing pods for error patterns"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {detectionState.activeStreams.length} streams
              </Badge>
            }
          />
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Pod</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Namespace</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Errors</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Started</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {detectionState.activeStreams.map((s) => (
                  <TableRow key={s.pod} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <div className="flex items-center gap-1.5">
                        <Server className="h-3 w-3 text-zinc-500" />
                        <Mono className="text-[11px] text-zinc-200">{s.pod}</Mono>
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2"><Mono className="text-[11px] text-zinc-400">{s.namespace}</Mono></TableCell>
                    <TableCell className="px-3 py-2 text-xs text-zinc-300">{s.service}</TableCell>
                    <TableCell className="px-3 py-2">
                      <span className="flex items-center gap-1.5">
                        <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
                        <span className="font-mono text-[11px] text-emerald-400">{s.status}</span>
                      </span>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className={cn("text-[11px]", s.errors > 0 ? "text-rose-400" : "text-zinc-500")}>{s.errors}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeAgo(s.started)}</Mono>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </Panel>

        {/* Alertmanager */}
        <Panel>
          <PanelHeader title="Alertmanager" sub="Webhook integration" />
          <div className="px-4 py-4 space-y-3">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Webhook className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">Webhook status</span>
              </div>
              <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                <PulseDot color="bg-emerald-400" className="mr-1 h-1.5 w-1.5" />
                {detectionState.alertmanager.webhookStatus}
              </Badge>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="text-[10px] uppercase tracking-wider text-zinc-500">Last received</div>
              <Mono className="mt-0.5 block text-[11px] text-zinc-300">
                {timeAgo(detectionState.alertmanager.lastReceived)}
              </Mono>
              <div className="mt-2 text-[10px] uppercase tracking-wider text-zinc-500">Pending alerts</div>
              <Mono className="mt-0.5 block text-base text-amber-400">{detectionState.alertmanager.pendingAlerts}</Mono>
            </div>
            <div className="border-t border-zinc-800 pt-3 text-[10px] text-zinc-500">
              <div className="flex items-center justify-between">
                <span>Endpoint</span>
                <Mono className="text-zinc-400">/webhook/alertmanager</Mono>
              </div>
              <div className="mt-1 flex items-center justify-between">
                <span>Fingerprint</span>
                <Mono className="text-zinc-400">7a3f9b2c</Mono>
              </div>
            </div>
          </div>
        </Panel>
      </div>

      {/* Pod watcher */}
      <Panel>
        <PanelHeader
          title="Pod Status Watcher"
          sub="Watching for CrashLoopBackOff, OOMKilled, and restart loops"
          right={
            <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
              <AlertTriangle className="h-3 w-3" /> 2 unhealthy
            </Badge>
          }
        />
        <div className="overflow-x-auto scrollbar-thin">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Pod</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Namespace</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Restarts</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Age</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Action</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detectionState.podWatcher.map((p) => (
                <TableRow key={p.pod} className="border-zinc-800/60 hover:bg-zinc-800/30">
                  <TableCell className="px-3 py-2">
                    <div className="flex items-center gap-1.5">
                      <Server className="h-3 w-3 text-zinc-500" />
                      <Mono className="text-[11px] text-zinc-200">{p.pod}</Mono>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2"><Mono className="text-[11px] text-zinc-400">{p.namespace}</Mono></TableCell>
                  <TableCell className="px-3 py-2">
                    <span className={cn("font-mono text-[11px]", podStatusColor[p.status])}>{p.status}</span>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className={cn("text-[11px]", p.restarts > 0 ? "text-rose-400" : "text-zinc-400")}>{p.restarts}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2"><Mono className="text-[11px] text-zinc-400">{p.age}</Mono></TableCell>
                  <TableCell className="px-3 py-2">
                    {p.status !== "Running" ? (
                      <Button variant="outline" size="sm" className="h-6 border-cyan-500/30 bg-cyan-500/10 text-[10px] text-cyan-400 hover:bg-cyan-500/20">
                        <Radio className="h-2.5 w-2.5" /> Investigate
                      </Button>
                    ) : (
                      <span className="text-[10px] text-zinc-600">—</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </Panel>

      {/* Detection rules */}
      <Panel>
        <PanelHeader title="Detection Rules" sub="Pattern matchers active on log streams" />
        <div className="grid grid-cols-1 gap-px bg-zinc-800 md:grid-cols-2 lg:grid-cols-3">
          {[
            { name: "Python traceback", pattern: "Traceback \\(most recent call last\\)", matches: 142, enabled: true },
            { name: "Go panic", pattern: "panic: |goroutine \\d+ \\[running\\]", matches: 38, enabled: true },
            { name: "JS TypeError", pattern: "TypeError: Cannot read propert", matches: 67, enabled: true },
            { name: "OOMKilled", pattern: "OOMKilled|container memory limit", matches: 12, enabled: true },
            { name: "CrashLoopBackOff", pattern: "Back-off restarting failed container", matches: 24, enabled: true },
            { name: "Connection refused", pattern: "ECONNREFUSED|Connection refused", matches: 19, enabled: true },
            { name: "Timeout", pattern: "TimeoutError|context deadline exceeded", matches: 31, enabled: true },
            { name: "Nil pointer", pattern: "nil pointer dereference", matches: 8, enabled: true },
            { name: "Concurrent map writes", pattern: "concurrent map writes", matches: 3, enabled: false },
          ].map((r) => (
            <div key={r.name} className="bg-zinc-900/60 px-4 py-2.5">
              <div className="flex items-center justify-between">
                <span className="text-xs text-zinc-200">{r.name}</span>
                <span className={cn("h-1.5 w-1.5 rounded-full", r.enabled ? "bg-emerald-400" : "bg-zinc-700")} />
              </div>
              <Mono className="mt-0.5 block truncate text-[10px] text-zinc-500">/{r.pattern}/</Mono>
              <div className="mt-1 flex items-center justify-between text-[10px]">
                <span className="text-zinc-500">matches (24h)</span>
                <Mono className={r.enabled ? "text-cyan-400" : "text-zinc-600"}>{r.matches}</Mono>
              </div>
            </div>
          ))}
        </div>
      </Panel>
    </div>
  );
}
