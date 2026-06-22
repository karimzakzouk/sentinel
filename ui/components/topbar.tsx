"use client";

import { useEffect, useState } from "react";
import { Bell, Search, LogOut } from "lucide-react";
import { useSession, signOut } from "next-auth/react";

export function TopBar() {
  const [time, setTime] = useState<string>("");
  const { data: session } = useSession();
  useEffect(() => {
    const tick = () =>
      setTime(new Date().toLocaleTimeString("en-US", { hour12: false }));
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <header className="border-b border-[var(--color-border)] bg-[var(--color-panel)] px-6 py-3 flex items-center justify-between flex-shrink-0">
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2 text-sm">
          <span className="live-dot" />
          <span className="text-[var(--color-text-dim)]">Live</span>
        </div>
        <div className="text-sm font-mono text-[var(--color-text-dim)]">
          {time} UTC
        </div>
      </div>
      <div className="flex items-center gap-3">
        <div className="relative">
          <Search size={14} className="absolute left-2 top-2 text-[var(--color-text-dim)]" />
          <input
            type="text"
            placeholder="Search incidents..."
            className="bg-[var(--color-bg)] border border-[var(--color-border)] rounded pl-7 pr-3 py-1 text-sm w-64 focus:outline-none focus:border-[var(--color-accent)]"
          />
        </div>
        <button className="relative p-2 hover:bg-[var(--color-panel-2)] rounded">
          <Bell size={14} />
          <span className="absolute top-1 right-1 w-2 h-2 bg-[var(--color-danger)] rounded-full" />
        </button>
        {session?.user && (
          <div className="flex items-center gap-2 text-xs">
            <span className="text-[var(--color-text-dim)]">
              {session.user.name || session.user.email}
            </span>
            <button
              onClick={() => signOut({ callbackUrl: "/login" })}
              className="p-1.5 hover:bg-[var(--color-panel-2)] rounded"
              title="Sign out"
            >
              <LogOut size={14} />
            </button>
          </div>
        )}
      </div>
    </header>
  );
}
