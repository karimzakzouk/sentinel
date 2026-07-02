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
import {
  Panel,
  PanelHeader,
  Mono,
  EmptyState,
} from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import {
  Search,
  Terminal,
  MessageSquare,
  Inbox,
  CheckCircle2,
} from "lucide-react";
import { cn } from "@/lib/utils";

// 19 chatops commands — hardcoded (matches internal/chatops/commands.go +
// commands_more.go). These are static definitions and don't come from the API.
interface ChatopsCommand {
  command: string;
  syntax: string;
  description: string;
  permission: "Read" | "Write" | "Destructive";
}

const chatopsCommands: ChatopsCommand[] = [
  { command: "incident list", syntax: "incident list [--status <status>] [--service <svc>]", description: "List incidents with optional filters", permission: "Read" },
  { command: "incident show", syntax: "incident show <id>", description: "Show details for a specific incident", permission: "Read" },
  { command: "incident pause", syntax: "incident pause <id>", description: "Pause autonomous remediation for an incident", permission: "Write" },
  { command: "incident resume", syntax: "incident resume <id>", description: "Resume autonomous remediation", permission: "Write" },
  { command: "incident escalate", syntax: "incident escalate <id> [--to <user>]", description: "Escalate incident to human review", permission: "Write" },
  { command: "investigation status", syntax: "investigation status <id>", description: "Show current investigation phase", permission: "Read" },
  { command: "investigation trigger", syntax: "investigation trigger <service> [--namespace <ns>]", description: "Manually trigger investigation", permission: "Write" },
  { command: "sandbox run", syntax: "sandbox run <incident-id> [--level <1-5>]", description: "Trigger a sandbox verification run", permission: "Write" },
  { command: "sandbox status", syntax: "sandbox status <run-id>", description: "Show sandbox run status", permission: "Read" },
  { command: "pr list", syntax: "pr list [--status <status>]", description: "List recent PRs", permission: "Read" },
  { command: "pr merge", syntax: "pr merge <pr-number>", description: "Force-merge a PR bypassing gates", permission: "Destructive" },
  { command: "pr revert", syntax: "pr revert <pr-number> [--reason <text>]", description: "Revert a merged PR", permission: "Destructive" },
  { command: "provider list", syntax: "provider list", description: "List LLM providers and status", permission: "Read" },
  { command: "provider trip", syntax: "provider trip <name>", description: "Manually trip a provider circuit breaker", permission: "Write" },
  { command: "provider reset", syntax: "provider reset <name>", description: "Reset a tripped circuit breaker", permission: "Write" },
  { command: "detection pause", syntax: "detection pause [--duration <m>]", description: "Pause autonomous detection", permission: "Write" },
  { command: "detection resume", syntax: "detection resume", description: "Resume autonomous detection", permission: "Write" },
  { command: "automerge queue", syntax: "automerge queue", description: "Show current auto-merge queue", permission: "Read" },
  { command: "automerge drain", syntax: "automerge drain", description: "Drain the auto-merge queue (rejects all)", permission: "Destructive" },
];

const permStyles: Record<ChatopsCommand["permission"], string> = {
  Read: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  Write: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  Destructive: "border-rose-500/30 bg-rose-500/10 text-rose-400",
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
            sub={`${chatopsCommands.length} chatops commands available to operators`}
            right={
              <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                {filtered.length} / {chatopsCommands.length}
              </Badge>
            }
          />
          {/* Filter bar */}
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
                      : "border-zinc-800 bg-zinc-900 text-zinc-500 hover:text-zinc-300",
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
                the webhook URL, bot token, and signing secret. Once connected, operators can invoke the 19
                commands above directly from Slack via <Mono className="text-cyan-400">/sentinel &lt;command&gt;</Mono>.
              </p>
            </div>
            <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
              <div className="mb-1 flex items-center gap-1 text-[10px] uppercase tracking-wider text-zinc-500">
                <Terminal className="h-2.5 w-2.5" /> Webhook endpoint
              </div>
              <Mono className="text-[11px] text-cyan-400">POST /slack/events</Mono>
              <p className="mt-1 text-[10px] text-zinc-500">
                Receives Slack Events API callbacks. Verifies the signing secret.
              </p>
            </div>
          </div>
        </Panel>
      </div>

      {/* Audit log — not available via API yet */}
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
            title="Command audit log available when incidents occur"
            description="The Sentinel API does not yet expose a chatops audit endpoint. Once an operator runs a /sentinel command, the audit log will be populated here from the activity feed."
            icon={Inbox}
            className="border-0 bg-transparent py-0"
          />
          <div className="mx-auto mt-4 max-w-md rounded-md border border-cyan-500/20 bg-cyan-500/5 p-3 text-center">
            <CheckCircle2 className="mx-auto mb-1 h-4 w-4 text-cyan-400" />
            <p className="text-[11px] text-zinc-400">
              Live SSE events from <Mono className="text-cyan-400">/events</Mono> will surface command activity in real time as a stopgap.
            </p>
          </div>
        </div>
      </Panel>
    </div>
  );
}
