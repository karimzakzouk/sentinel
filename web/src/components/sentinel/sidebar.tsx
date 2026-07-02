"use client";

import {
  LayoutDashboard,
  AlertTriangle,
  Radio,
  FlaskConical,
  GitPullRequest,
  Cpu,
  Merge,
  Eye,
  MessageSquare,
  BarChart3,
  Settings,
  Terminal,
  Shield,
  Command,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useSentinel, type SectionId } from "@/lib/store";
import { PulseDot } from "@/components/sentinel/common";

interface NavItem {
  id: SectionId;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  badge?: string;
  pulse?: boolean;
}

const nav: NavItem[] = [
  { id: "dashboard", label: "Dashboard", icon: LayoutDashboard },
  { id: "incidents", label: "Incidents", icon: AlertTriangle, badge: "2" },
  { id: "live", label: "Live Investigation", icon: Radio, pulse: true },
  { id: "sandbox", label: "Sandbox", icon: FlaskConical },
  { id: "prs", label: "Pull Requests", icon: GitPullRequest, badge: "7" },
  { id: "providers", label: "Providers", icon: Cpu },
  { id: "automerge", label: "Auto-Merge", icon: Merge, badge: "1" },
  { id: "detection", label: "Detection", icon: Eye },
  { id: "chatops", label: "Chatops", icon: MessageSquare },
  { id: "metrics", label: "Metrics", icon: BarChart3 },
  { id: "settings", label: "Settings", icon: Settings },
  { id: "api", label: "API Explorer", icon: Terminal },
];

export function Sidebar() {
  const activeSection = useSentinel((s) => s.activeSection);
  const setActiveSection = useSentinel((s) => s.setActiveSection);
  const setCommandOpen = useSentinel((s) => s.setCommandOpen);

  return (
    <aside className="flex h-screen w-60 flex-col border-r border-zinc-800 bg-zinc-950">
      {/* Brand */}
      <div className="flex h-14 items-center gap-2.5 border-b border-zinc-800 px-4">
        <div className="flex h-7 w-7 items-center justify-center rounded-md bg-cyan-500/10 ring-1 ring-cyan-500/30">
          <Shield className="h-4 w-4 text-cyan-400" />
        </div>
        <div className="flex flex-col leading-tight">
          <span className="text-sm font-semibold tracking-tight text-zinc-50">Sentinel</span>
          <span className="font-mono text-[10px] text-zinc-500">v2.4.1 · prod</span>
        </div>
      </div>

      {/* Search trigger */}
      <div className="px-3 pt-3">
        <button
          onClick={() => setCommandOpen(true)}
          className="flex w-full items-center justify-between rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-left text-xs text-zinc-500 transition-colors hover:border-zinc-700 hover:bg-zinc-900"
        >
          <span className="flex items-center gap-2">
            <Command className="h-3 w-3" />
            Search...
          </span>
          <kbd className="rounded border border-zinc-700 bg-zinc-800 px-1 font-mono text-[10px] text-zinc-400">⌘K</kbd>
        </button>
      </div>

      {/* Nav */}
      <nav className="flex-1 overflow-y-auto px-2 py-3 scrollbar-thin">
        <ul className="space-y-0.5">
          {nav.map((item) => {
            const Icon = item.icon;
            const active = activeSection === item.id;
            return (
              <li key={item.id}>
                <button
                  onClick={() => setActiveSection(item.id)}
                  className={cn(
                    "group flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors",
                    active
                      ? "bg-cyan-500/10 text-cyan-300 ring-1 ring-cyan-500/20"
                      : "text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200"
                  )}
                >
                  <span className="relative">
                    <Icon className={cn("h-4 w-4", active && "text-cyan-400")} />
                    {item.pulse && (
                      <span className="absolute -right-0.5 -top-0.5">
                        <PulseDot color="bg-cyan-400" className="h-1.5 w-1.5" />
                      </span>
                    )}
                  </span>
                  <span className="flex-1 text-left">{item.label}</span>
                  {item.badge && (
                    <span
                      className={cn(
                        "rounded px-1.5 py-0.5 font-mono text-[10px] font-medium",
                        active ? "bg-cyan-500/20 text-cyan-300" : "bg-zinc-800 text-zinc-400"
                      )}
                    >
                      {item.badge}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      </nav>

      {/* Footer status */}
      <div className="border-t border-zinc-800 p-3">
        <div className="space-y-2 rounded-md bg-zinc-900/50 p-2.5">
          <div className="flex items-center justify-between text-[11px]">
            <span className="text-zinc-500">Cluster</span>
            <span className="flex items-center gap-1.5 font-mono text-emerald-400">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
              prod-us-east-1
            </span>
          </div>
          <div className="flex items-center justify-between text-[11px]">
            <span className="text-zinc-500">Autonomous</span>
            <span className="flex items-center gap-1.5 font-mono text-emerald-400">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse-dot" />
              running
            </span>
          </div>
          <div className="flex items-center justify-between text-[11px]">
            <span className="text-zinc-500">Uptime</span>
            <span className="font-mono text-zinc-300">42d 18h</span>
          </div>
        </div>
      </div>
    </aside>
  );
}
