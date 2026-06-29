"use client";

import { cn, formatConfidence } from "@/lib/utils";

/** Colour a 0-1 confidence value: green ≥0.7, amber 0.4-0.7, red <0.4. */
function confidenceColor(v: number): string {
  if (v >= 0.7) return "bg-emerald-500";
  if (v >= 0.4) return "bg-amber-500";
  return "bg-rose-500";
}

function confidenceTextColor(v: number): string {
  if (v >= 0.7) return "text-emerald-400";
  if (v >= 0.4) return "text-amber-400";
  return "text-rose-400";
}

export function ConfidenceBar({
  value,
  label,
  showValue = true,
  className,
}: {
  value: number;
  label?: string;
  showValue?: boolean;
  className?: string;
}) {
  const v = Math.max(0, Math.min(1, value || 0));
  const pct = Math.round(v * 100);
  return (
    <div className={cn("w-full", className)}>
      {(label || showValue) && (
        <div className="mb-1 flex items-center justify-between gap-2">
          {label && (
            <span className="truncate text-xs text-muted-foreground">
              {label}
            </span>
          )}
          {showValue && (
            <span
              className={cn(
                "font-mono text-xs tabular-nums",
                confidenceTextColor(v),
              )}
            >
              {formatConfidence(v)}
            </span>
          )}
        </div>
      )}
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-zinc-800">
        <div
          className={cn("h-full rounded-full transition-all", confidenceColor(v))}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}
