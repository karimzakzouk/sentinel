"use client";

import { useEffect, useState } from "react";
import {
  Panel,
  PanelHeader,
  Mono,
  PulseDot,
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
  Play,
  Pause,
  Radio,
  AlertTriangle,
  Webhook,
  Loader2,
  CheckCircle2,
  Server,
  Wifi,
  WifiOff,
  EyeOff,
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

  const [duration, setDuration] = useState("30");
  const [acting, setActing] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  // API-down is the only signal we have. We don't claim to know whether
  // detection is paused vs. crashed — shown honestly to the user.
  const paused = !healthy;

  const recentDetections = activityFeed.filter((e) => e.type === "incident").slice(0, 6);

  useEffect(() => {
    const id = window.setInterval(() => refreshHealth(), 15_000);
    return () => window.clearInterval(id);
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
      window.setTimeout(() => refreshHealth(), 500);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Action failed");
    } finally {
      setActing(false);
    }
  };

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
      description: "Slack Events API callback. Verifies the signing secret, then dispatches to the bot.",
    },
  ];

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* Status */}
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Autonomous Detection"
            sub="Live state of the detection loop"
            right={
              paused ? (
                <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[10px] text-amber-400">
                  <Pause className="h-3 w-3" /> paused / down
                </Badge>
              ) : (
                <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                  <Play className="h-3 w-3" /> running
                </Badge>
              )
            }
          />
          <div className="grid grid-cols-1 gap-3 p-4 sm:grid-cols-3">
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                <Server className="h-3 w-3" /> API
              </div>
              <div className="mt-1.5 flex items-center gap-1.5">
                {healthy ? <Wifi className="h-3 w-3 text-emerald-400" /> : <WifiOff className="h-3 w-3 text-rose-400" />}
                <Mono className={healthy ? "text-emerald-400" : "text-rose-400"}>
                  {healthy ? "healthy" : "down"}
                </Mono>
              </div>
              <div className="mt-1 text-[10px] text-zinc-600">v{version || "—"}</div>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                <Radio className="h-3 w-3" /> SSE
              </div>
              <div className="mt-1.5 flex items-center gap-1.5">
                {sseConnected ? (
                  <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
                ) : (
                  <span className="h-1.5 w-1.5 rounded-full bg-zinc-600" />
                )}
                <Mono className={sseConnected ? "text-emerald-400" : "text-zinc-400"}>
                  {sseConnected ? "streaming" : "disconnected"}
                </Mono>
              </div>
              <div className="mt-1 text-[10px] text-zinc-600">live events</div>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                <AlertTriangle className="h-3 w-3" /> Incidents
              </div>
              <Mono className="mt-1.5 block text-xl text-zinc-100">{incidents.length}</Mono>
              <div className="text-[10px] text-zinc-600">total tracked</div>
            </div>
          </div>
          <div className="border-t border-zinc-800 p-4">
            <div className="flex flex-wrap items-end gap-3">
              <div>
                <label className="mb-1 block text-[10px] uppercase tracking-wider text-zinc-500">Pause duration</label>
                <Select value={duration} onValueChange={setDuration}>
                  <SelectTrigger className="h-8 w-[120px] bg-zinc-950 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="15">15 minutes</SelectItem>
                    <SelectItem value="30">30 minutes</SelectItem>
                    <SelectItem value="60">1 hour</SelectItem>
                    <SelectItem value="240">4 hours</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <Button
                onClick={onToggle}
                disabled={acting}
                size="sm"
                className={cn(paused ? "bg-emerald-500 text-zinc-950 hover:bg-emerald-400" : "bg-amber-500 text-zinc-950 hover:bg-amber-400")}
              >
                {acting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
                {paused ? "Resume" : "Pause"}
              </Button>
              {actionError && <span className="text-[11px] text-rose-400">{actionError}</span>}
            </div>
            <p className="mt-2 text-[10px] text-zinc-600">
              Pause calls <Mono className="text-zinc-400">POST /admin/pause</Mono>. Resume calls <Mono className="text-zinc-400">POST /admin/resume</Mono>. Both require the API token when auth is enabled.
            </p>
          </div>
        </Panel>

        {/* Recent detections */}
        <Panel>
          <PanelHeader title="Recent Detections" sub="From the activity feed" />
          <div className="divide-y divide-zinc-800/60">
            {recentDetections.length === 0 ? (
              <div className="px-4 py-8 text-center text-xs text-zinc-600">No detection events yet</div>
            ) : (
              recentDetections.map((e) => (
                <div key={e.id} className="px-4 py-2.5">
                  <p className="text-[12px] text-zinc-300">{e.message}</p>
                  <p className="mt-0.5 font-mono text-[10px] text-zinc-600">{e.time}</p>
                </div>
              ))
            )}
          </div>
        </Panel>
      </div>

      {/* Webhooks */}
      <Panel>
        <PanelHeader
          title="Webhook Endpoints"
          sub="Sentinel accepts these webhook sources"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {webhooks.length} webhooks
            </Badge>
          }
        />
        <div className="grid grid-cols-1 gap-3 p-4 lg:grid-cols-3">
          {webhooks.map((w) => (
            <div key={w.path} className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="flex items-center gap-2">
                <Webhook className="h-3.5 w-3.5 text-cyan-400" />
                <span className="text-xs font-medium text-zinc-200">{w.label}</span>
              </div>
              <Mono className="mt-1 block text-[11px] text-cyan-400">{w.path}</Mono>
              <p className="mt-1.5 text-[11px] text-zinc-500">{w.description}</p>
            </div>
          ))}
        </div>
      </Panel>

      {/* Honest state note */}
      {paused && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-4 py-3">
          <EyeOff className="h-4 w-4 shrink-0 text-amber-400" />
          <p className="text-xs text-zinc-300">
            Sentinel API is unreachable. This could mean detection is paused, the pod crashed, or the network is down.
            Hit <Mono className="text-cyan-400">/healthz</Mono> in the API Explorer to verify.
          </p>
        </div>
      )}
      {!paused && (
        <div className="flex items-center gap-3 rounded-lg border border-emerald-500/20 bg-emerald-500/5 px-4 py-3">
          <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-400" />
          <p className="text-xs text-zinc-300">
            Sentinel is healthy. Detection events will stream in via SSE and appear in the activity feed.
          </p>
        </div>
      )}
    </div>
  );
}
