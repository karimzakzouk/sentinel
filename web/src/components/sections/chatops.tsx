"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { Panel, PanelHeader, Mono, EmptyState, ErrorState } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { useSentinelStore } from "@/lib/store";
import type { ChatopsCommandMeta } from "@/lib/types";
import { Search, Terminal, MessageSquare, Inbox, RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";

// v5 hardcoded 19 chatops commands. v1 fetches them from /api/meta so the
// list never drifts from the Go registry in internal/chatops/commands*.go.

const tierStyle: Record<string, string> = {
  Informational: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  Operational: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  Destructive: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

const permStyle: Record<string, string> = {
  read: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  write: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  admin: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

export function ChatopsSection() {
  const apiMeta = useSentinelStore((s) => s.apiMeta);
  const refreshApiMeta = useSentinelStore((s) => s.refreshApiMeta);
  const [search, setSearch] = useState("");
  const [tierFilter, setTierFilter] = useState<string>("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    // If the meta isn't loaded yet, kick off a fetch. The store's
    // refreshApiMeta swallows errors silently; we surface them here so the
    // chatops section can show a retry button.
    if (apiMeta) {
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        await refreshApiMeta();
        if (!cancelled) {
          setError(null);
          setLoading(false);
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to fetch /api/meta");
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [apiMeta, refreshApiMeta]);

  const commands: ChatopsCommandMeta[] = useMemo(() => apiMeta?.commands ?? [], [apiMeta]);

  const filtered = useMemo(() => {
    return commands.filter((c) => {
      if (tierFilter !== "all" && c.tier !== tierFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          c.name.toLowerCase().includes(q) ||
          c.description.toLowerCase().includes(q) ||
          c.usage.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [commands, search, tierFilter]);

  if (loading) {
    return (
      <div className="space-y-3 p-6">
        <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={() => refreshApiMeta()} />
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Command Reference"
            sub={`${commands.length} chatops commands — fetched from /api/meta`}
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {filtered.length} / {commands.length}
              </Badge>
            }
          />
          <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-4 py-2.5">
            <div className="relative min-w-[200px] flex-1">
              <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-zinc-500" />
              <Input
                placeholder="Search commands..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-8 bg-zinc-900 pl-8 font-mono text-xs"
              />
            </div>
            <div className="flex gap-1">
              {["all", "Informational", "Operational", "Destructive"].map((p) => (
                <button
                  key={p}
                  onClick={() => setTierFilter(p)}
                  className={cn(
                    "rounded border px-2 py-1 font-mono text-[10px] transition-colors",
                    tierFilter === p
                      ? p === "Informational"
                        ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-400"
                        : p === "Operational"
                          ? "border-amber-500/40 bg-amber-500/10 text-amber-400"
                          : p === "Destructive"
                            ? "border-rose-500/40 bg-rose-500/10 text-rose-400"
                            : "border-zinc-700 bg-zinc-800 text-zinc-200"
                      : "border-zinc-800 bg-zinc-900 text-zinc-500 hover:text-zinc-300",
                  )}
                >
                  {p}
                </button>
              ))}
            </div>
            <button
              onClick={() => refreshApiMeta()}
              className="rounded border border-zinc-800 bg-zinc-900 px-2 py-1 text-[10px] text-zinc-400 hover:bg-zinc-800"
              title="Refresh from /api/meta"
            >
              <RefreshCw className="h-3 w-3" />
            </button>
          </div>
          <div className="overflow-x-auto scrollbar-thin">
            {commands.length === 0 ? (
              <div className="px-4 py-12">
                <EmptyState
                  title="No commands available"
                  description="The /api/meta endpoint returned an empty command list. Ensure the Sentinel backend is running and the chatops registry is initialized."
                  icon={Inbox}
                  className="border-0 bg-transparent py-0"
                />
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="border-zinc-800 hover:bg-transparent">
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Command</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Usage</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Description</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Tier</TableHead>
                    <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Permission</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {filtered.map((c) => (
                    <TableRow key={c.name} className="border-zinc-800/60 hover:bg-zinc-800/30">
                      <TableCell className="px-3 py-2">
                        <div className="flex items-center gap-1.5">
                          <Terminal className="h-3 w-3 text-zinc-500" />
                          <Mono className="text-[11px] text-cyan-400">{c.name}</Mono>
                          {c.cancelable && (
                            <span className="rounded border border-zinc-700 bg-zinc-900 px-1 font-mono text-[9px] text-zinc-500">
                              cancelable
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="max-w-[280px] px-3 py-2">
                        <Mono className="block truncate text-[11px] text-zinc-400">{c.usage}</Mono>
                      </TableCell>
                      <TableCell className="px-3 py-2 text-[12px] text-zinc-300">{c.description}</TableCell>
                      <TableCell className="px-3 py-2">
                        <Badge variant="outline" className={cn("font-mono text-[10px]", tierStyle[c.tier] ?? "border-zinc-700 bg-zinc-900 text-zinc-400")}>
                          {c.tier}
                        </Badge>
                      </TableCell>
                      <TableCell className="px-3 py-2">
                        <Badge variant="outline" className={cn("font-mono text-[10px]", permStyle[c.permission] ?? "border-zinc-700 bg-zinc-900 text-zinc-400")}>
                          {c.permission}
                        </Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </Panel>

        <Panel>
          <PanelHeader title="Slack Integration" sub="Workspace bot" />
          <div className="space-y-3 p-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <MessageSquare className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">Slack bot</span>
              </div>
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-500">
                not configured
              </Badge>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3 text-[11px] text-zinc-500">
              <p>
                Configure the Slack bot in <Mono className="text-zinc-300">Settings → Slack</Mono> by setting
                the webhook URL, bot token, and signing secret. Once connected, operators can invoke any of
                the {commands.length} commands above directly from Slack via{" "}
                <Mono className="text-cyan-400">/sentinel &lt;command&gt;</Mono>.
              </p>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="mb-1 flex items-center gap-1 text-[10px] uppercase tracking-wider text-zinc-500">
                <Terminal className="h-2.5 w-2.5" /> Webhook endpoint
              </div>
              <Mono className="text-[11px] text-cyan-400">POST /slack/events</Mono>
              <p className="mt-1 text-[10px] text-zinc-500">Receives Slack Events API callbacks. Verifies the signing secret.</p>
            </div>
          </div>
        </Panel>
      </div>

      <Panel>
        <PanelHeader
          title="Command Audit Log"
          sub="Who ran what, when, with what result"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              API: not yet available
            </Badge>
          }
        />
        <div className="px-4 py-12">
          <EmptyState
            title="Audit log not exposed by the API"
            description="The Sentinel API does not yet expose a chatops audit endpoint. Once an operator runs a /sentinel command, the audit log will be populated here from the activity feed."
            icon={Inbox}
            className="border-0 bg-transparent py-0"
          />
        </div>
      </Panel>
    </div>
  );
}
