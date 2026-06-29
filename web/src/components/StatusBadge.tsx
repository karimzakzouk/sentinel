"use client";

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";

const STATUS_STYLES: Record<string, string> = {
  // incident statuses
  investigating: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  diagnosed: "border-violet-500/30 bg-violet-500/10 text-violet-400",
  fix_proposed: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  verifying: "border-blue-500/30 bg-blue-500/10 text-blue-400",
  pr_opened: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  merged: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  resolved: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  reverted: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  failed: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  auto_merged: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  // provider statuses
  healthy: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  tripped: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  cooling: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  // automerge queue states
  queued: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  waiting_ci: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  blocked: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  // sandbox
  passed: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  pass: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  verified: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  // severity
  critical: "border-rose-500/30 bg-rose-500/10 text-rose-400",
  high: "border-orange-500/30 bg-orange-500/10 text-orange-400",
  medium: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  low: "border-zinc-500/30 bg-zinc-500/10 text-zinc-400",
};

export function StatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const key = (status || "").toLowerCase();
  const style = STATUS_STYLES[key];
  return (
    <Badge
      variant="outline"
      className={cn(
        "font-mono uppercase tracking-wide text-[10px]",
        style || "border-zinc-700 bg-zinc-800/50 text-zinc-400",
        className,
      )}
    >
      {status || "unknown"}
    </Badge>
  );
}

/** A small coloured dot for inline status indication. */
const DOT_COLORS: Record<string, string> = {
  cyan: "bg-cyan-400",
  violet: "bg-violet-400",
  amber: "bg-amber-400",
  blue: "bg-blue-400",
  emerald: "bg-emerald-400",
  rose: "bg-rose-400",
  orange: "bg-orange-400",
  zinc: "bg-zinc-400",
};

const STATUS_DOT_COLOR: Record<string, string> = {
  healthy: "emerald",
  tripped: "rose",
  cooling: "amber",
  investigating: "cyan",
  diagnosed: "violet",
  fix_proposed: "amber",
  pr_opened: "amber",
  verifying: "blue",
  merged: "emerald",
  resolved: "emerald",
  auto_merged: "emerald",
  reverted: "rose",
  failed: "rose",
  queued: "cyan",
  waiting_ci: "amber",
  blocked: "rose",
  critical: "rose",
  high: "orange",
  medium: "amber",
  low: "zinc",
};

export function StatusDot({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const key = (status || "").toLowerCase();
  const colorName = STATUS_DOT_COLOR[key] || "zinc";
  return (
    <span
      className={cn(
        "inline-block size-2 rounded-full",
        DOT_COLORS[colorName],
        className,
      )}
    />
  );
}
