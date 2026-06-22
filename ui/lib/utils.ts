import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function statusBadgeClass(status: string): string {
  switch (status) {
    case "verified":
    case "resolved":
    case "approved":
      return "badge-ok";
    case "investigating":
    case "executing":
    case "governance_review":
    case "fix_proposed":
    case "root_caused":
      return "badge-info";
    case "failed":
    case "rejected":
      return "badge-danger";
    case "escalated":
    case "detected":
      return "badge-warn";
    default:
      return "badge-neutral";
  }
}

export function severityBadgeClass(severity: string): string {
  switch (severity) {
    case "critical":
      return "badge-danger";
    case "warning":
      return "badge-warn";
    case "info":
      return "badge-info";
    default:
      return "badge-neutral";
  }
}

export function timeAgo(iso: string): string {
  const then = new Date(iso).getTime();
  const now = Date.now();
  const diff = Math.floor((now - then) / 1000);
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}
