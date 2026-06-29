"use client";

import { cn } from "@/lib/utils";

// ─── Simple CSS bar charts (no chart library — keeps the static bundle small) ─

export type BarDatum = {
  label: string;
  value: number;
  color?: string; // tailwind bg-* class
  sublabel?: string;
};

const DEFAULT_COLORS = [
  "bg-cyan-500",
  "bg-emerald-500",
  "bg-amber-500",
  "bg-rose-500",
  "bg-violet-500",
  "bg-blue-500",
  "bg-orange-500",
];

/** Horizontal bar chart — one row per datum. */
export function BarChartH({
  data,
  unit = "",
  className,
  valueFormatter,
}: {
  data: BarDatum[];
  unit?: string;
  className?: string;
  valueFormatter?: (v: number) => string;
}) {
  const max = Math.max(1, ...data.map((d) => d.value));
  if (data.length === 0) {
    return (
      <p className="py-6 text-center text-xs text-muted-foreground">
        No data
      </p>
    );
  }
  return (
    <div className={cn("flex flex-col gap-2.5", className)}>
      {data.map((d, i) => {
        const pct = Math.max(2, (d.value / max) * 100);
        const color = d.color || DEFAULT_COLORS[i % DEFAULT_COLORS.length];
        return (
          <div key={`${d.label}-${i}`} className="flex items-center gap-3">
            <div className="w-28 shrink-0 truncate text-right font-mono text-xs text-muted-foreground">
              {d.label}
            </div>
            <div className="relative h-5 flex-1 overflow-hidden rounded bg-zinc-800/60">
              <div
                className={cn("h-full rounded transition-all", color)}
                style={{ width: `${pct}%` }}
              />
            </div>
            <div className="w-16 shrink-0 text-right font-mono text-xs tabular-nums text-zinc-200">
              {valueFormatter ? valueFormatter(d.value) : `${d.value}${unit}`}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/** Stacked horizontal bar — segments sum to a total. */
export function StackedBar({
  segments,
  className,
  total,
}: {
  segments: BarDatum[];
  className?: string;
  total?: number;
}) {
  const sum = total ?? segments.reduce((a, s) => a + s.value, 0);
  if (sum === 0) {
    return (
      <div
        className={cn(
          "h-4 w-full rounded bg-zinc-800/60",
          className,
        )}
      />
    );
  }
  return (
    <div
      className={cn(
        "flex h-4 w-full overflow-hidden rounded bg-zinc-800/60",
        className,
      )}
    >
      {segments.map((s, i) => {
        const pct = (s.value / sum) * 100;
        if (pct <= 0) return null;
        const color = s.color || DEFAULT_COLORS[i % DEFAULT_COLORS.length];
        return (
          <div
            key={`${s.label}-${i}`}
            className={cn("h-full transition-all", color)}
            style={{ width: `${pct}%` }}
            title={`${s.label}: ${s.value}`}
          />
        );
      })}
    </div>
  );
}

/** Vertical bar chart — columns side by side. */
export function BarChartV({
  data,
  unit = "",
  className,
  height = 160,
  valueFormatter,
}: {
  data: BarDatum[];
  unit?: string;
  className?: string;
  height?: number;
  valueFormatter?: (v: number) => string;
}) {
  const max = Math.max(1, ...data.map((d) => d.value));
  if (data.length === 0) {
    return (
      <p className="py-6 text-center text-xs text-muted-foreground">
        No data
      </p>
    );
  }
  return (
    <div
      className={cn("flex items-end gap-2", className)}
      style={{ height }}
    >
      {data.map((d, i) => {
        const pct = Math.max(2, (d.value / max) * 100);
        const color = d.color || DEFAULT_COLORS[i % DEFAULT_COLORS.length];
        return (
          <div
            key={`${d.label}-${i}`}
            className="flex flex-1 flex-col items-center justify-end gap-1"
            style={{ height: "100%" }}
          >
            <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
              {valueFormatter ? valueFormatter(d.value) : `${d.value}${unit}`}
            </span>
            <div
              className={cn("w-full min-w-[8px] rounded-t transition-all", color)}
              style={{ height: `${pct}%`, minHeight: 2 }}
              title={`${d.label}: ${d.value}`}
            />
            <span className="w-full truncate text-center font-mono text-[10px] text-muted-foreground">
              {d.label}
            </span>
          </div>
        );
      })}
    </div>
  );
}

/** A simple legend. */
export function ChartLegend({
  items,
  className,
}: {
  items: BarDatum[];
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap gap-x-4 gap-y-1.5", className)}>
      {items.map((it, i) => {
        const color = it.color || DEFAULT_COLORS[i % DEFAULT_COLORS.length];
        return (
          <div key={`${it.label}-${i}`} className="flex items-center gap-1.5">
            <span className={cn("size-2.5 rounded-sm", color)} />
            <span className="text-xs text-muted-foreground">{it.label}</span>
            {it.value > 0 && (
              <span className="font-mono text-xs text-zinc-300">{it.value}</span>
            )}
          </div>
        );
      })}
    </div>
  );
}
