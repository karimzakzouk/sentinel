"use client";

import {
  Activity,
  AlertOctagon,
  BarChart3,
  GitMerge,
  Bot,
  LayoutDashboard,
  Server,
  FlaskConical,
  Terminal,
  Settings,
  ShieldCheck,
} from "lucide-react";
import { cn } from "@/lib/utils";

export type SectionId =
  | "dashboard"
  | "incidents"
  | "providers"
  | "automerge"
  | "sandbox"
  | "metrics"
  | "chatops"
  | "settings";

export interface NavItem {
  id: SectionId;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  description: string;
}

export const NAV_ITEMS: NavItem[] = [
  { id: "dashboard", label: "Dashboard", icon: LayoutDashboard, description: "Overview" },
  { id: "incidents", label: "Incidents", icon: AlertOctagon, description: "Investigations" },
  { id: "providers", label: "Providers", icon: Server, description: "LLM pool" },
  { id: "automerge", label: "Auto-merge", icon: GitMerge, description: "Queue & gates" },
  { id: "sandbox", label: "Sandbox", icon: FlaskConical, description: "Verifications" },
  { id: "metrics", label: "Metrics", icon: BarChart3, description: "Prometheus" },
  { id: "chatops", label: "Chatops", icon: Terminal, description: "/sentinel cmds" },
  { id: "settings", label: "Settings", icon: Settings, description: "Configuration" },
];

export function SentinelLogo({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center gap-2.5", className)}>
      <div className="flex size-8 items-center justify-center rounded-lg border border-cyan-500/40 bg-cyan-500/10">
        <ShieldCheck className="size-5 text-cyan-400" />
      </div>
      <div className="flex flex-col leading-none">
        <span className="font-mono text-sm font-bold tracking-tight text-zinc-50">
          SENTINEL
        </span>
        <span className="text-[10px] text-muted-foreground">
          Autonomous AI SRE
        </span>
      </div>
    </div>
  );
}

export function Sidebar({
  active,
  onSelect,
  className,
}: {
  active: SectionId;
  onSelect: (id: SectionId) => void;
  className?: string;
}) {
  return (
    <nav
      className={cn(
        "flex h-full w-[220px] flex-col border-r border-zinc-800 bg-sidebar",
        className,
      )}
      aria-label="Primary"
    >
      <div className="flex h-16 items-center border-b border-zinc-800 px-4">
        <SentinelLogo />
      </div>
      <div className="flex-1 overflow-y-auto p-3 scrollbar-sentinel">
        <ul className="flex flex-col gap-1">
          {NAV_ITEMS.map((item) => {
            const Icon = item.icon;
            const isActive = active === item.id;
            return (
              <li key={item.id}>
                <button
                  type="button"
                  onClick={() => onSelect(item.id)}
                  aria-current={isActive ? "page" : undefined}
                  className={cn(
                    "group flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-sm transition-colors",
                    isActive
                      ? "border border-cyan-500/30 bg-cyan-500/10 text-cyan-400"
                      : "border border-transparent text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-100",
                  )}
                >
                  <Icon
                    className={cn(
                      "size-4 shrink-0",
                      isActive ? "text-cyan-400" : "text-zinc-500 group-hover:text-zinc-300",
                    )}
                  />
                  <span className="flex-1 truncate font-medium">{item.label}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </div>
      <div className="border-t border-zinc-800 p-3">
        <div className="flex items-center gap-2 rounded-lg bg-zinc-900/60 px-3 py-2 text-xs text-muted-foreground">
          <Activity className="size-3.5 text-cyan-500" />
          <span className="font-mono">v4.4.0</span>
        </div>
      </div>
    </nav>
  );
}
