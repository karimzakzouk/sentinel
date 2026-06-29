"use client";

import {
  AlertOctagon,
  GitMerge,
  Server,
  Activity,
  Cog,
  type LucideIcon,
} from "lucide-react";
import { cn, timeAgo, shortId } from "@/lib/utils";
import type { SSEEventItem } from "@/hooks/use-sse";

const TYPE_META: Record<
  string,
  { icon: LucideIcon; color: string; label: string }
> = {
  incident: { icon: AlertOctagon, color: "text-rose-400", label: "Incident" },
  pipeline: { icon: Activity, color: "text-cyan-400", label: "Pipeline" },
  provider: { icon: Server, color: "text-amber-400", label: "Provider" },
  automerge: { icon: GitMerge, color: "text-emerald-400", label: "Auto-merge" },
  config: { icon: Cog, color: "text-violet-400", label: "Config" },
};

function describe(ev: SSEEventItem): string {
  const d = ev.data || {};
  const parts: string[] = [];
  const action =
    (d.action as string) ||
    (d.event as string) ||
    (d.status as string) ||
    ev.type;
  if (action) parts.push(String(action));
  const svc = (d.service as string) || (d.cluster_service as string);
  if (svc) parts.push(svc);
  const iid = (d.incident_id as string) || (d.id as string);
  if (iid) parts.push(`#${shortId(iid)}`);
  const reason = (d.reason as string) || (d.last_trip_reason as string);
  if (reason) parts.push(`— ${reason}`);
  if (parts.length === 0) {
    try {
      return JSON.stringify(d).slice(0, 80);
    } catch {
      return ev.type;
    }
  }
  return parts.join(" ");
}

export function ActivityFeed({
  events,
  className,
  emptyHint = "Waiting for events…",
  maxHeight = "max-h-96",
}: {
  events: SSEEventItem[];
  className?: string;
  emptyHint?: string;
  maxHeight?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col gap-1 overflow-y-auto scrollbar-sentinel pr-1",
        maxHeight,
        className,
      )}
    >
      {events.length === 0 ? (
        <div className="flex items-center gap-2 rounded-md border border-dashed border-zinc-800 px-3 py-6 text-xs text-muted-foreground">
          <span className="size-1.5 animate-sentinel-pulse rounded-full bg-cyan-500" />
          {emptyHint}
        </div>
      ) : (
        events.map((ev) => {
          const meta = TYPE_META[ev.type] || {
            icon: Activity,
            color: "text-zinc-400",
            label: ev.type,
          };
          const Icon = meta.icon;
          return (
            <div
              key={ev.id}
              className="flex items-start gap-2.5 rounded-md border border-zinc-800/60 bg-zinc-900/40 px-3 py-2 transition-colors hover:border-zinc-700"
            >
              <Icon className={cn("mt-0.5 size-3.5 shrink-0", meta.color)} />
              <div className="min-w-0 flex-1">
                <p className="truncate font-mono text-xs text-zinc-200">
                  {describe(ev)}
                </p>
                <div className="mt-0.5 flex items-center gap-2">
                  <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                    {meta.label}
                  </span>
                  <span className="text-[10px] text-muted-foreground/70">
                    {timeAgo(ev.timestamp)}
                  </span>
                </div>
              </div>
            </div>
          );
        })
      )}
    </div>
  );
}
