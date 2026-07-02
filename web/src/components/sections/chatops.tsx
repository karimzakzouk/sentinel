"use client";

import { useMemo, useState } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { Panel, PanelHeader, Mono, timeAgo, timeMDHM } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { chatopsCommands, chatopsAudit, type ChatopsCommand } from "@/lib/mock-data";
import { Search, Terminal, CheckCircle2, XCircle, Ban, MessageSquare } from "lucide-react";
import { cn } from "@/lib/utils";

const permStyles: Record<ChatopsCommand["permission"], string> = {
  Read: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  Write: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  Destructive: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

const resultStyles: Record<string, { color: string; icon: React.ComponentType<{ className?: string }> }> = {
  success: { color: "text-emerald-400", icon: CheckCircle2 },
  error: { color: "text-rose-400", icon: XCircle },
  denied: { color: "text-amber-400", icon: Ban },
};

export function ChatopsSection() {
  const [search, setSearch] = useState("");
  const [permFilter, setPermFilter] = useState<string>("all");

  const filtered = useMemo(() => {
    return chatopsCommands.filter((c) => {
      if (permFilter !== "all" && c.permission !== permFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          c.command.toLowerCase().includes(q) ||
          c.description.toLowerCase().includes(q) ||
          c.syntax.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [search, permFilter]);

  return (
    <div className="space-y-3 p-6">
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* Command reference */}
        <Panel className="lg:col-span-2">
          <PanelHeader
            title="Command Reference"
            sub="19 chatops commands available to operators"
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {filtered.length} / {chatopsCommands.length}
              </Badge>
            }
          />
          {/* Filter bar */}
          <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-4 py-2.5">
            <div className="relative flex-1 min-w-[200px]">
              <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-zinc-500" />
              <Input
                placeholder="Search commands..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-8 bg-zinc-900 pl-8 font-mono text-xs"
              />
            </div>
            <div className="flex gap-1">
              {["all", "Read", "Write", "Destructive"].map((p) => (
                <button
                  key={p}
                  onClick={() => setPermFilter(p)}
                  className={cn(
                    "rounded border px-2 py-1 font-mono text-[10px] transition-colors",
                    permFilter === p
                      ? p === "Read"
                        ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-400"
                        : p === "Write"
                        ? "border-amber-500/40 bg-amber-500/10 text-amber-400"
                        : p === "Destructive"
                        ? "border-rose-500/40 bg-rose-500/10 text-rose-400"
                        : "border-zinc-700 bg-zinc-800 text-zinc-200"
                      : "border-zinc-800 bg-zinc-900 text-zinc-500 hover:text-zinc-300"
                  )}
                >
                  {p}
                </button>
              ))}
            </div>
          </div>
          <div className="overflow-x-auto scrollbar-thin">
            <Table>
              <TableHeader>
                <TableRow className="border-zinc-800 hover:bg-transparent">
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Command</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Syntax</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Description</TableHead>
                  <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Permission</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((c) => (
                  <TableRow key={c.command} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <div className="flex items-center gap-1.5">
                        <Terminal className="h-3 w-3 text-zinc-500" />
                        <Mono className="text-[11px] text-cyan-400">{c.command}</Mono>
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2 max-w-[280px]">
                      <Mono className="block truncate text-[11px] text-zinc-400">{c.syntax}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 text-[12px] text-zinc-300">{c.description}</TableCell>
                    <TableCell className="px-3 py-2">
                      <Badge variant="outline" className={cn("font-mono text-[10px]", permStyles[c.permission])}>
                        {c.permission}
                      </Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </Panel>

        {/* Slack integration status */}
        <Panel>
          <PanelHeader title="Slack Integration" sub="Workspace bot status" />
          <div className="px-4 py-4 space-y-3">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <MessageSquare className="h-4 w-4 text-zinc-500" />
                <span className="text-xs text-zinc-300">#sre-oncall</span>
              </div>
              <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
                connected
              </Badge>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3 space-y-2 text-[11px]">
              <div className="flex justify-between">
                <span className="text-zinc-500">Bot user</span>
                <Mono className="text-zinc-300">@sentinel-bot</Mono>
              </div>
              <div className="flex justify-between">
                <span className="text-zinc-500">Channel</span>
                <Mono className="text-zinc-300">#sre-oncall</Mono>
              </div>
              <div className="flex justify-between">
                <span className="text-zinc-500">Workspace</span>
                <Mono className="text-zinc-300">acme.slack.com</Mono>
              </div>
              <div className="flex justify-between">
                <span className="text-zinc-500">Messages (24h)</span>
                <Mono className="text-cyan-400">142</Mono>
              </div>
              <div className="flex justify-between">
                <span className="text-zinc-500">Commands (24h)</span>
                <Mono className="text-cyan-400">28</Mono>
              </div>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="mb-1 flex items-center gap-1 text-[10px] uppercase tracking-wider text-zinc-500">
                <Terminal className="h-2.5 w-2.5" /> Last command
              </div>
              <Mono className="text-[11px] text-zinc-300">incident list --status investigating</Mono>
              <div className="mt-1 flex items-center gap-1.5">
                <CheckCircle2 className="h-3 w-3 text-emerald-400" />
                <span className="font-mono text-[10px] text-emerald-400">success</span>
                <span className="ml-auto font-mono text-[10px] text-zinc-600">{timeAgo(chatopsAudit[0].time)}</span>
              </div>
            </div>
          </div>
        </Panel>
      </div>

      {/* Audit log */}
      <Panel>
        <PanelHeader
          title="Command Audit Log"
          sub="Who ran what, when, with what result"
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              {chatopsAudit.length} entries (24h)
            </Badge>
          }
        />
        <div className="overflow-x-auto scrollbar-thin">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">User</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Command</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Args</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Result</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Time</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {chatopsAudit.map((a, i) => {
                const r = resultStyles[a.result];
                const Icon = r.icon;
                return (
                  <TableRow key={i} className="border-zinc-800/60 hover:bg-zinc-800/30">
                    <TableCell className="px-3 py-2">
                      <div className="flex items-center gap-2">
                        <div className="flex h-5 w-5 items-center justify-center rounded-full bg-zinc-800 font-mono text-[9px] uppercase text-zinc-300">
                          {a.user.slice(0, 2)}
                        </div>
                        <Mono className="text-[11px] text-zinc-200">{a.user}</Mono>
                      </div>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-cyan-400">{a.command}</Mono>
                    </TableCell>
                    <TableCell className="px-3 py-2 max-w-[280px]">
                      {a.args ? (
                        <Mono className="block truncate text-[11px] text-zinc-400">{a.args}</Mono>
                      ) : (
                        <span className="text-[11px] text-zinc-600">—</span>
                      )}
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <span className={cn("flex items-center gap-1 font-mono text-[11px]", r.color)}>
                        <Icon className="h-3 w-3" />
                        {a.result}
                      </span>
                    </TableCell>
                    <TableCell className="px-3 py-2">
                      <Mono className="text-[11px] text-zinc-400">{timeMDHM(a.time)}</Mono>
                      <div className="text-[10px] text-zinc-600">{timeAgo(a.time)}</div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      </Panel>
    </div>
  );
}
