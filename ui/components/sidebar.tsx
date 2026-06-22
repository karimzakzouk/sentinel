"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Shield, Activity, FileText, BookOpen, Settings, GitBranch } from "lucide-react";

const NAV = [
  { href: "/", label: "Dashboard", icon: Activity },
  { href: "/incidents", label: "Incidents", icon: Shield },
  { href: "/policies", label: "Policies", icon: FileText },
  { href: "/runbooks", label: "Runbooks", icon: BookOpen },
  { href: "/settings", label: "Settings", icon: Settings },
];

export function Sidebar() {
  const pathname = usePathname();
  return (
    <aside className="w-56 flex-shrink-0 border-r border-[var(--color-border)] bg-[var(--color-panel)] flex flex-col">
      <div className="p-4 border-b border-[var(--color-border)]">
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded bg-[var(--color-accent)] flex items-center justify-center">
            <span className="text-[var(--color-bg)] font-bold text-sm">S</span>
          </div>
          <div>
            <div className="font-semibold text-sm">Sentinel</div>
            <div className="text-[10px] text-[var(--color-text-dim)] uppercase tracking-wider">
              Autonomous SRE
            </div>
          </div>
        </div>
      </div>

      <nav className="flex-1 py-2">
        {NAV.map((item) => {
          const Icon = item.icon;
          const active = pathname === item.href ||
            (item.href !== "/" && pathname.startsWith(item.href));
          return (
            <Link
              key={item.href}
              href={item.href}
              className={`nav-item ${active ? "active" : ""}`}
            >
              <Icon size={14} />
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="p-4 border-t border-[var(--color-border)] text-[11px] text-[var(--color-text-dim)]">
        <div className="flex items-center gap-2 mb-2">
          <GitBranch size={12} />
          <span>v1.0.0</span>
        </div>
        <div className="font-mono">
          <div className="flex items-center gap-2">
            <span className="live-dot" />
            <span>Operator: Running</span>
          </div>
        </div>
      </div>
    </aside>
  );
}
