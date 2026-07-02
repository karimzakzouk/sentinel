"use client";

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import type { IncidentStatus, SandboxLevelStatus } from "@/lib/mock-data";

export function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

export function timeHMS(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-US", { hour12: false });
}

export function timeMDHM(iso: string): string {
  const d = new Date(iso);
  return `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

const statusMap: Record<IncidentStatus, { label: string; cls: string; dot?: boolean }> = {
  investigating: { label: "investigating", cls: "bg-amber-500/15 text-amber-400 border-amber-500/30", dot: true },
  verified: { label: "verified", cls: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30" },
  failed: { label: "failed", cls: "bg-rose-500/15 text-rose-400 border-rose-500/30" },
  escalated: { label: "escalated", cls: "bg-orange-500/15 text-orange-400 border-orange-500/30" },
  resolved: { label: "resolved", cls: "bg-cyan-500/15 text-cyan-400 border-cyan-500/30" },
};

export function StatusBadge({ status, className }: { status: IncidentStatus; className?: string }) {
  const cfg = statusMap[status];
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
  const color = value >= 85 ? "bg-emerald-500" : value >= 60 ? "bg-amber-500" : "bg-rose-500";
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <div className="h-1.5 w-16 overflow-hidden rounded-full bg-zinc-800">
        <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${value}%` }} />
      </div>
      <span className="font-mono text-[11px] text-zinc-400">{value}%</span>
    </div>
  );
}

const levelStyles: Record<SandboxLevelStatus, { cls: string; label: string }> = {
  pass: { cls: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30", label: "PASS" },
  fail: { cls: "bg-rose-500/15 text-rose-400 border-rose-500/30", label: "FAIL" },
  pending: { cls: "bg-amber-500/15 text-amber-400 border-amber-500/30", label: "PEND" },
  skip: { cls: "bg-zinc-700/40 text-zinc-500 border-zinc-700", label: "SKIP" },
};

export function LevelBadge({ status, level }: { status: SandboxLevelStatus; level: number }) {
  const cfg = levelStyles[status];
  return (
    <span className={cn("inline-flex h-5 min-w-[2.25rem] items-center justify-center rounded border px-1 font-mono text-[10px] font-semibold", cfg.cls)}>
      L{level}
    </span>
  );
}

export function LevelRow({ levels }: { levels: SandboxLevelStatus[] }) {
  return (
    <div className="flex items-center gap-1">
      {levels.map((lvl, i) => (
        <LevelBadge key={i} status={lvl} level={i + 1} />
      ))}
    </div>
  );
}

export function PulseDot({ color = "bg-amber-400", className }: { color?: string; className?: string }) {
  return (
    <span className={cn("relative flex h-2 w-2", className)}>
      <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-75", color)} />
      <span className={cn("relative inline-flex h-2 w-2 rounded-full", color)} />
    </span>
  );
}

export function Panel({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("rounded-lg border border-zinc-800 bg-zinc-900/50", className)}>{children}</div>
  );
}

export function PanelHeader({ title, sub, right }: { title: string; sub?: string; right?: React.ReactNode }) {
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

export function Mono({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("font-mono text-mono", className)}>{children}</span>;
}
