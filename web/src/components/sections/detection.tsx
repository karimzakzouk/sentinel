"use client";

import { useEffect, useState } from "react";
import {
  Panel,
  PanelHeader,
  Mono,
  PulseDot,
  EmptyState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSentinelStore } from "@/lib/store";
import {
  Eye,
  EyeOff,
  Play,
  Pause,
  Radio,
  Activity,
  AlertTriangle,
  Webhook,
  Clock,
  Loader2,
  CheckCircle2,
  Server,
  Wifi,
  WifiOff,
} from "lucide-react";
import { cn } from "@/lib/utils";

export function DetectionSection() {
  const healthy = useSentinelStore((s) => s.healthy);
  const sseConnected = useSentinelStore((s) => s.sseConnected);
  const version = useSentinelStore((s) => s.version);
  const incidents = useSentinelStore((s) => s.incidents);
  const activityFeed = useSentinelStore((s) => s.activityFeed);
  const pauseAutonomous = useSentinelStore((s) => s.pauseAutonomous);
  const resumeAutonomous = useSentinelStore((s) => s.resumeAutonomous);
  const refreshHealth = useSentinelStore((s) => s.refreshHealth);
  const runtimeConfig = useSentinelStore((s) => s.runtimeConfig);

  const [duration, setDuration] = useState("30");
  const [acting, setActing] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  // We don't know if detection is "paused" from the API directly — we use
  // health status as a proxy (down = could be paused or crashed). This is
  // shown honestly to the user.
  const paused = !healthy;

  // Recent "detection-like" events from the activity feed (incident type
  // events signal that detection picked something up).
  const recentDetections = activityFeed
    .filter((e) => e.type === "incident")
    .slice(0, 6);

  // Refresh health every 15s so the autonomous-mode indicator stays current.
  useEffect(() => {
    const id = setInterval(() => refreshHealth(), 15_000);
    return () => clearInterval(id);
  }, [refreshHealth]);

  const onToggle = async () => {
    setActing(true);
    setActionError(null);
    try {
      if (paused) {
        await resumeAutonomous();
      } else {
        await pauseAutonomous(parseInt(duration, 10));
      }
      // Refresh health after action to reflect the new state.
      setTimeout(() => refreshHealth(), 500);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Action failed");
    } finally {
      setActing(false);
    }
  };

  // Webhook endpoints exposed by Sentinel (static — from server.go routes).
  const webhooks = [
    {
      path: "/webhook/alertmanager",
      label: "Alertmanager",
      description: "Receives Alertmanager alerts (POST). Triggers investigation on firing alerts.",
    },
    {
      path: "/webhook/github",
      label: "GitHub",
      description: "Receives GitHub webhook events (pull_request, issue_comment). Drives PR review + chatops.",
    },
    {
      path: "/slack/events",
      label: "Slack",
      description: "Receives Slack slash command events. Powers the /sentinel chatops bot.",
    },
  ];

  return (
    <div className="space-y-3 p-6">
      {/* Status panel */}
      <Panel>
        <div className="flex flex-wrap items-center justify-between gap-4 px-4 py-4">
          <div className="flex items-center gap-4">
            <div
              className={cn(
                "flex h-12 w-12 items-center justify-center rounded-lg",
                paused
                  ? "bg-amber-500/10 ring-1 ring-amber-500/30"
                  : "bg-emerald-500/10 ring-1 ring-emerald-500/30",
              )}
            >
              {paused ? (
                <EyeOff className="h-5 w-5 text-amber-400" />
              ) : (
                <Eye className="h-5 w-5 text-emerald-400" />
              )}
            </div>
            <div>
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold text-zinc-100">Autonomous Detection</h3>
                <Badge
                  variant="outline"
                  className={cn(
                    "font-mono text-[10px]",
                    paused
                      ? "border-amber-500/30 bg-amber-500/10 text-amber-400"
                      : "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
                  )}
                >
                  {!paused && <PulseDot color="bg-emerald-400" className="mr-1 h-1.5 w-1.5" />}
                  {paused ? "paused or down" : "running"}
                </Badge>
              </div>
              <p className="mt-0.5 text-[11px] text-zinc-500">
                {paused
                  ? "API health check is failing — detection may be paused or the server is unreachable."
                  : `Sentinel v${version || "—"} is healthy and watching for anomalies.`}
              </p>
              {actionError && (
                <p className="mt-1 font-mono text-[10px] text-rose-400">{actionError}</p>
              )}
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Select value={duration} onValueChange={setDuration} disabled={paused || acting}>
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
              onClick={onToggle}
              size="sm"
              disabled={acting}
              className={cn(
                "h-8 text-xs",
                paused
                  ? "bg-emerald-500 text-zinc-950 hover:bg-emerald-400"
                  : "bg-amber-500 text-zinc-950 hover:bg-amber-400",
              )}
            >
              {acting ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : paused ? (
                <Play className="h-3 w-3" />
              ) : (
                <Pause className="h-3 w-3" />
              )}
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
        </div>
      </Panel>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* API + SSE + autonomous status */}
        <Panel>
          <PanelHeader title="System Status" sub="From /healthz + SSE" />
          <div className="space-y-3 p-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Server className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">API health</span>
              </div>
              <Badge
                variant="outline"
                className={cn(
                  "font-mono text-[10px]",
                  healthy
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                    : "border-rose-500/30 bg-rose-500/10 text-rose-400",
                )}
              >
                {healthy ? "ok" : "down"}
              </Badge>
            </div>

            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                {sseConnected ? (
                  <Wifi className="h-4 w-4 text-zinc-500" />
                ) : (
                  <WifiOff className="h-4 w-4 text-zinc-500" />
                )}
                <span className="text-xs text-zinc-300">SSE stream</span>
              </div>
              <Badge
                variant="outline"
                className={cn(
                  "font-mono text-[10px]",
                  sseConnected
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                    : "border-rose-500/30 bg-rose-500/10 text-rose-400",
                )}
              >
                {sseConnected ? "connected" : "disconnected"}
              </Badge>
            </div>

            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Radio className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">Autonomous mode</span>
              </div>
              <Badge
                variant="outline"
                className={cn(
                  "font-mono text-[10px]",
                  paused
                    ? "border-amber-500/30 bg-amber-500/10 text-amber-400"
                    : "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
                )}
              >
                {paused ? "paused" : "running"}
              </Badge>
            </div>

            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Activity className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">Version</span>
              </div>
              <Mono className="text-[11px] text-zinc-300">v{version || "—"}</Mono>
            </div>

            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <AlertTriangle className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">Incidents (7d)</span>
              </div>
              <Mono className="text-[11px] text-zinc-300">{incidents.length}</Mono>
            </div>
          </div>
        </Panel>

        {/* Webhook endpoints */}
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Webhook Endpoints"
            sub="Alertmanager + GitHub + Slack intake"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {webhooks.length} endpoints
              </Badge>
            }
          />
          <div className="divide-y divide-zinc-800/60">
            {webhooks.map((w) => (
              <div key={w.path} className="px-4 py-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Webhook className="h-3.5 w-3.5 text-zinc-500" />
                    <span className="text-xs font-medium text-zinc-200">{w.label}</span>
                    <Mono className="text-[11px] text-cyan-400">{w.path}</Mono>
                  </div>
                  <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-500">
                    POST
                  </Badge>
                </div>
                <p className="mt-1 text-[11px] text-zinc-500">{w.description}</p>
              </div>
            ))}
          </div>
          {runtimeConfig && (
            <div className="border-t border-zinc-800 px-4 py-2 text-[10px] text-zinc-500">
              <div className="flex items-center justify-between">
                <span>Log level</span>
                <Mono className="text-zinc-400">{runtimeConfig.log_level ?? "info"}</Mono>
              </div>
            </div>
          )}
        </Panel>
      </div>

      {/* Recent detections — from SSE activity feed */}
      <Panel>
        <PanelHeader
          title="Recent Detections"
          sub="Live incident events from the SSE stream"
          right={
            sseConnected ? (
              <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                <PulseDot color="bg-emerald-400" className="mr-1 h-1.5 w-1.5" />
                streaming
              </Badge>
            ) : (
              <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                disconnected
              </Badge>
            )
          }
        />
        <div className="divide-y divide-zinc-800/60">
          {recentDetections.length === 0 ? (
            <div className="px-4 py-12 text-center">
              <EmptyState
                title="No detections yet"
                description="When Sentinel's autonomous detection picks up an anomaly (log pattern, pod crash, or Alertmanager alert), it will appear here in real time via SSE."
                icon={Eye}
                className="border-0 bg-transparent py-0"
              />
            </div>
          ) : (
            recentDetections.map((e) => (
              <div key={e.id} className="flex items-start gap-2.5 px-4 py-2.5">
                <div className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded bg-zinc-800/80">
                  <AlertTriangle className="h-3 w-3 text-amber-400" />
                </div>
                <div className="min-w-0 flex-1">
                  <p className="text-[12px] leading-tight text-zinc-300">{e.message}</p>
                  <p className="mt-0.5 font-mono text-[10px] text-zinc-600">{new Date(e.time).toLocaleString("en-US", { hour12: false })}</p>
                </div>
              </div>
            ))
          )}
        </div>
      </Panel>

      {/* Help / limitations */}
      <Panel>
        <PanelHeader title="About this view" sub="What's available via the API" />
        <div className="space-y-2 p-4 text-[11px] text-zinc-400">
          <p className="flex items-start gap-2">
            <CheckCircle2 className="mt-0.5 h-3 w-3 shrink-0 text-emerald-400" />
            <span>
              <Mono>POST /admin/pause?duration=NNm</Mono> and <Mono>POST /admin/resume</Mono> are wired to the
              buttons above. Pause requires a duration (defaults to 30m).
            </span>
          </p>
          <p className="flex items-start gap-2">
            <CheckCircle2 className="mt-0.5 h-3 w-3 shrink-0 text-emerald-400" />
            <span>
              <Mono>GET /healthz</Mono> is polled every 15s for the autonomous-mode indicator, and{" "}
              <Mono>GET /events</Mono> (SSE) drives the recent-detections list.
            </span>
          </p>
          <p className="flex items-start gap-2">
            <Clock className="mt-0.5 h-3 w-3 shrink-0 text-amber-400" />
            <span>
              Per-pod log streams, pod watchers, and detection rules are not exposed via the public API.
              Use <Mono>kubectl logs</Mono> or the Sentinel Slack bot for live pod introspection.
            </span>
          </p>
        </div>
      </Panel>
    </div>
  );
}
