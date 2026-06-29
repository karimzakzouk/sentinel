import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// ─── Formatting helpers ──────────────────────────────────────────────────────

/** Format an ISO date string / epoch as a relative "5m ago" string. */
export function timeAgo(input: string | number | Date | undefined | null): string {
  if (input == null) return "—";
  const d = typeof input === "number" ? new Date(input) : new Date(input);
  const ts = d.getTime();
  if (Number.isNaN(ts)) return "—";
  const diff = Date.now() - ts;
  const sec = Math.floor(diff / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 7) return `${day}d ago`;
  return d.toLocaleDateString();
}

/** Format a number of seconds as "1m 23s" / "45s". */
export function formatDuration(seconds: number | undefined | null): string {
  if (seconds == null || Number.isNaN(seconds)) return "—";
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rem = s % 60;
  if (m < 60) return rem ? `${m}m ${rem}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h}h ${mm}m` : `${h}h`;
}

/** Format a USD cost. */
export function formatCost(usd: number | undefined | null): string {
  if (usd == null || Number.isNaN(usd)) return "$0.00";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  if (usd < 1) return `$${usd.toFixed(3)}`;
  return `$${usd.toFixed(2)}`;
}

/** Format a 0-1 confidence as a percentage. */
export function formatConfidence(c: number | undefined | null): string {
  if (c == null || Number.isNaN(c)) return "—";
  return `${Math.round(c * 100)}%`;
}

/** Truncate a string with an ellipsis. */
export function truncate(s: string | undefined | null, n: number): string {
  if (!s) return "";
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

/** Shorten an incident / object ID for display (keep first 8 chars). */
export function shortId(id: string | undefined | null): string {
  if (!id) return "—";
  return id.length > 12 ? id.slice(0, 8) : id;
}

/** Parse a CSV string into a trimmed array. */
export function parseCsv(s: string | undefined | null): string[] {
  if (!s) return [];
  return s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}

/** Join an array into a CSV string. */
export function joinCsv(arr: string[] | undefined | null): string {
  if (!arr || arr.length === 0) return "";
  return arr.join(", ");
}
