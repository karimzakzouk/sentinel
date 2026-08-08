"use client";

import * as React from "react";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";

// ─── Time / number formatting ────────────────────────────────────────────────
// Re-exported from lib/utils so section components have a single import for
// both UI atoms and formatting helpers.

export {
  cn,
  timeAgo,
  timeMDHM,
  formatDuration,
  formatCost,
  formatTokens,
  formatConfidence,
  truncate,
  shortId,
} from "@/lib/utils";

// ─── Status badges ───────────────────────────────────────────────────────────

const statusMap: Record<string, { label: string; cls: string; dot?: boolean }> = {
  investigating: { label: "investigating", cls: "bg-amber-500/15 text-amber-400 border-amber-500/30", dot: true },
  root_caused: { label: "root_caused", cls: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30" },
  fix_proposed: { label: "fix_proposed", cls: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30" },
  governance_review: { label: "governance_review", cls: "bg-violet-500/15 text-violet-400 border-violet-500/30" },
  approved: { label: "approved", cls: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30" },
  executing: { label: "executing", cls: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30", dot: true },
  verified: { label: "verified", cls: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30" },
  failed: { label: "failed", cls: "bg-rose-500/15 text-rose-400 border-rose-500/30" },
  escalated: { label: "escalated", cls: "bg-orange-500/15 text-orange-400 border-orange-500/30" },
  rejected: { label: "rejected", cls: "bg-rose-500/15 text-rose-400 border-rose-500/30" },
  resolved: { label: "resolved", cls: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30" },
  detected: { label: "detected", cls: "bg-zinc-500/15 text-zinc-300 border-zinc-500/30" },
};

export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const cfg = statusMap[status] ?? {
    label: status || "unknown",
    cls: "bg-zinc-700/40 text-zinc-400 border-zinc-700",
  };
  return (
    <Badge variant="outline" className={cn("font-mono text-[11px] lowercase", cfg.cls, className)}>
      {cfg.dot && (
        <span className="relative mr-1 flex h-1.5 w-1.5">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-amber-400 opacity-75" />
          <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-amber-400" />
        </span>
      )}
      {cfg.label}
    </Badge>
  );
}

export function ConfidenceBar({ value, className }: { value: number; className?: string }) {
  const v = value > 1 ? value : value * 100;
  const color = v >= 85 ? "bg-emerald-500" : v >= 60 ? "bg-amber-500" : "bg-rose-500";
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <div className="h-1.5 w-16 overflow-hidden rounded-full bg-zinc-800">
        <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${Math.min(100, Math.max(0, v))}%` }} />
      </div>
      <span className="font-mono text-[11px] text-zinc-400">{Math.round(v)}%</span>
    </div>
  );
}

// ─── Misc UI atoms ───────────────────────────────────────────────────────────

export function PulseDot({ color = "bg-amber-400", className }: { color?: string; className?: string }) {
  return (
    <span className={cn("relative flex h-2 w-2", className)}>
      <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-75", color)} />
      <span className={cn("relative inline-flex h-2 w-2 rounded-full", color)} />
    </span>
  );
}

export function Panel({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("rounded-lg border border-zinc-800 bg-zinc-900/50", className)}>{children}</div>;
}

export function PanelHeader({
  title,
  sub,
  right,
}: {
  title: string;
  sub?: string;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-2.5">
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-300">{title}</h3>
        {sub && <p className="mt-0.5 text-[11px] text-zinc-500">{sub}</p>}
      </div>
      {right}
    </div>
  );
}

export function Mono({
  children,
  className,
  title,
}: {
  children: React.ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <span className={cn("font-mono text-mono", className)} title={title}>
      {children}
    </span>
  );
}

// ─── Loading / empty / error helpers (used by every section) ────────────────

export function SkeletonRow({ className }: { className?: string }) {
  return <div className={cn("h-8 animate-pulse rounded bg-zinc-800/60", className)} />;
}

export function PanelSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <Panel>
      <div className="space-y-2 p-4">
        {Array.from({ length: rows }).map((_, i) => (
          <SkeletonRow key={i} />
        ))}
      </div>
    </Panel>
  );
}

export function EmptyState({
  title,
  description,
  icon: Icon,
  className,
}: {
  title: string;
  description?: string;
  icon?: React.ComponentType<{ className?: string }>;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-zinc-800 bg-zinc-900/30 px-6 py-12 text-center",
        className,
      )}
    >
      {Icon && (
        <div className="flex size-12 items-center justify-center rounded-full border border-zinc-800 bg-zinc-900">
          <Icon className="size-5 text-zinc-500" />
        </div>
      )}
      <div className="space-y-1">
        <p className="text-sm font-medium text-zinc-200">{title}</p>
        {description && <p className="mx-auto max-w-sm text-xs text-zinc-500">{description}</p>}
      </div>
    </div>
  );
}

export function ErrorState({
  message,
  onRetry,
  className,
}: {
  message: string;
  onRetry?: () => void;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-3 rounded-lg border border-rose-500/30 bg-rose-500/5 px-6 py-8 text-center",
        className,
      )}
    >
      <p className="font-mono text-xs text-rose-300">{message}</p>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-rose-500/40 px-3 py-1.5 text-xs text-rose-300 transition-colors hover:bg-rose-500/10"
        >
          Retry
        </button>
      )}
    </div>
  );
}
