"use client";

import { useMemo, useState } from "react";
import { Terminal, Search, ShieldCheck, ShieldAlert, ShieldQuestion, Copy } from "lucide-react";
import { cn } from "@/lib/utils";
import { SectionHeader, Panel } from "@/components/ui-helpers";
import { useToast } from "@/hooks/use-toast";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type Perm = "read" | "write" | "admin";

interface ChatCommand {
  name: string;
  description: string;
  usage: string;
  example: string;
  permission: Perm;
}

const COMMANDS: ChatCommand[] = [
  {
    name: "help",
    description: "List available commands (filtered by your permission)",
    usage: "/sentinel help",
    example: "/sentinel help",
    permission: "read",
  },
  {
    name: "status",
    description: "Re-render the in-place status comment with current state",
    usage: "/sentinel status",
    example: "/sentinel status",
    permission: "read",
  },
  {
    name: "explain",
    description: "Re-explain the root cause in plain English",
    usage: "/sentinel explain",
    example: "/sentinel explain",
    permission: "read",
  },
  {
    name: "trace",
    description: "Show the full agent reasoning trace (hypotheses, evidence, decisions)",
    usage: "/sentinel trace",
    example: "/sentinel trace",
    permission: "read",
  },
  {
    name: "retry-sandbox",
    description: "Re-run sandbox verification on the current branch state",
    usage: "/sentinel retry-sandbox",
    example: "/sentinel retry-sandbox",
    permission: "write",
  },
  {
    name: "alternatives",
    description:
      "Generate 3 alternative fix approaches (LLM re-runs FixProposer at higher temperature)",
    usage: "/sentinel alternatives",
    example: "/sentinel alternatives",
    permission: "write",
  },
  {
    name: "apply",
    description:
      "After /sentinel alternatives, switch the PR to the chosen alternative (A, B, or C)",
    usage: "/sentinel apply <A|B|C>",
    example: "/sentinel apply B",
    permission: "write",
  },
  {
    name: "hold",
    description:
      "Block auto-merge (keeps approval). Use when you want to review before merge.",
    usage: "/sentinel hold",
    example: "/sentinel hold",
    permission: "write",
  },
  {
    name: "approve",
    description:
      "Approve for auto-merge (required when safety policy returns RequireHumanApproval or for high-risk services)",
    usage: "/sentinel approve",
    example: "/sentinel approve",
    permission: "write",
  },
  {
    name: "no-merge",
    description:
      "Remove PR from auto-merge queue entirely (stronger than hold)",
    usage: "/sentinel no-merge",
    example: "/sentinel no-merge",
    permission: "write",
  },
  {
    name: "merge",
    description:
      "Merge now through Sentinel (evaluates gates, merges if pass, starts monitoring). If CI pending, queues for merge-on-green.",
    usage: "/sentinel merge",
    example: "/sentinel merge",
    permission: "write",
  },
  {
    name: "investigate",
    description:
      "Full re-investigation from scratch (ignores this fix, re-clones, re-runs pipeline)",
    usage: "/sentinel investigate",
    example: "/sentinel investigate",
    permission: "write",
  },
  {
    name: "retest",
    description:
      "Re-run LLM pipeline keeping the same root cause (use when LLM got cause right but fix is bad)",
    usage: "/sentinel retest",
    example: "/sentinel retest",
    permission: "write",
  },
  {
    name: "verify",
    description: "Add extra probe paths to sandbox verification",
    usage: "/sentinel verify <paths>",
    example: "/sentinel verify /pay /api/users",
    permission: "write",
  },
  {
    name: "revert",
    description:
      "Revert this PR (if merged). Calls kubectl rollout undo on the affected deployment.",
    usage: "/sentinel revert",
    example: "/sentinel revert",
    permission: "admin",
  },
  {
    name: "rollback",
    description: "Rollback a specific deployment (kubectl rollout undo)",
    usage: "/sentinel rollback <deployment> [--namespace <ns>]",
    example: "/sentinel rollback payment-api --namespace prod",
    permission: "admin",
  },
  {
    name: "pause",
    description: "Pause Sentinel's autonomous mode globally (default 60min)",
    usage: "/sentinel pause [duration]",
    example: "/sentinel pause 30m",
    permission: "admin",
  },
  {
    name: "resume",
    description: "Resume autonomous mode (undoes /sentinel pause)",
    usage: "/sentinel resume",
    example: "/sentinel resume",
    permission: "admin",
  },
  {
    name: "override",
    description:
      "Force-pass a check (sandbox/ci/cooldown). Admin only. Recorded in audit log.",
    usage: "/sentinel override <sandbox|ci|cooldown>",
    example: "/sentinel override cooldown",
    permission: "admin",
  },
];

const PERM_META: Record<
  Perm,
  { label: string; color: string; icon: React.ComponentType<{ className?: string }> }
> = {
  read: { label: "Read", color: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400", icon: ShieldQuestion },
  write: { label: "Write", color: "border-amber-500/30 bg-amber-500/10 text-amber-400", icon: ShieldCheck },
  admin: { label: "Admin", color: "border-rose-500/30 bg-rose-500/10 text-rose-400", icon: ShieldAlert },
};

const FILTERS: Array<{ id: Perm | "all"; label: string }> = [
  { id: "all", label: "All" },
  { id: "read", label: "Read" },
  { id: "write", label: "Write" },
  { id: "admin", label: "Admin" },
];

export function Chatops() {
  const [query, setQuery] = useState("");
  const [permFilter, setPermFilter] = useState<Perm | "all">("all");
  const { toast } = useToast();

  const filtered = useMemo(() => {
    return COMMANDS.filter((c) => {
      if (permFilter !== "all" && c.permission !== permFilter) return false;
      if (query) {
        const q = query.toLowerCase();
        return (
          c.name.toLowerCase().includes(q) ||
          c.description.toLowerCase().includes(q) ||
          c.usage.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [query, permFilter]);

  const counts = useMemo(() => {
    return {
      all: COMMANDS.length,
      read: COMMANDS.filter((c) => c.permission === "read").length,
      write: COMMANDS.filter((c) => c.permission === "write").length,
      admin: COMMANDS.filter((c) => c.permission === "admin").length,
    };
  }, []);

  function copyUsage(usage: string) {
    if (navigator.clipboard) {
      navigator.clipboard.writeText(usage).then(
        () => toast({ title: "Copied", description: usage }),
        () => toast({ title: "Copy failed", variant: "destructive" }),
      );
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Chatops"
        description="Slash commands for PR comments — 19 /sentinel commands"
      />

      {/* Filters */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="flex items-center gap-2 overflow-x-auto scrollbar-sentinel">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              onClick={() => setPermFilter(f.id)}
              className={cn(
                "shrink-0 rounded-md border px-2.5 py-1 text-[11px] font-medium uppercase tracking-wide transition-colors",
                permFilter === f.id
                  ? "border-cyan-500/40 bg-cyan-500/10 text-cyan-400"
                  : "border-zinc-800 text-muted-foreground hover:text-zinc-200",
              )}
            >
              {f.label}
              <span className="ml-1.5 font-mono text-[10px] opacity-70">
                {counts[f.id]}
              </span>
            </button>
          ))}
        </div>
        <div className="relative sm:ml-auto sm:w-72">
          <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search commands…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="pl-8"
          />
        </div>
      </div>

      {/* Command table */}
      <Panel bodyClassName="p-0">
        <div className="overflow-x-auto scrollbar-sentinel">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="text-muted-foreground">Command</TableHead>
                <TableHead className="text-muted-foreground">Description</TableHead>
                <TableHead className="text-muted-foreground">Usage</TableHead>
                <TableHead className="text-muted-foreground">Example</TableHead>
                <TableHead className="text-muted-foreground">Permission</TableHead>
                <TableHead className="w-8" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((c) => {
                const meta = PERM_META[c.permission];
                const Icon = meta.icon;
                return (
                  <TableRow key={c.name} className="border-zinc-800 align-top">
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Terminal className="size-3.5 text-cyan-400" />
                        <span className="font-mono text-xs font-semibold text-zinc-100">
                          {c.name}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="max-w-sm text-xs text-zinc-300">
                      {c.description}
                    </TableCell>
                    <TableCell>
                      <code className="rounded bg-zinc-800/60 px-1.5 py-0.5 font-mono text-[11px] text-cyan-400">
                        {c.usage}
                      </code>
                    </TableCell>
                    <TableCell>
                      <code className="font-mono text-[11px] text-muted-foreground">
                        {c.example}
                      </code>
                    </TableCell>
                    <TableCell>
                      <span
                        className={cn(
                          "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] uppercase tracking-wide",
                          meta.color,
                        )}
                      >
                        <Icon className="size-2.5" />
                        {meta.label}
                      </span>
                    </TableCell>
                    <TableCell>
                      <button
                        type="button"
                        onClick={() => copyUsage(c.usage)}
                        className="rounded p-1 text-muted-foreground transition-colors hover:bg-zinc-800 hover:text-zinc-200"
                        title="Copy usage"
                      >
                        <Copy className="size-3.5" />
                      </button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
        {filtered.length === 0 && (
          <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
            <Search className="size-3.5" />
            No commands match “{query}”.
          </div>
        )}
      </Panel>

      {/* Legend */}
      <Panel title="Permission Levels" description="how Sentinel authorises slash commands">
        <div className="grid gap-3 sm:grid-cols-3">
          {(["read", "write", "admin"] as const).map((p) => {
            const meta = PERM_META[p];
            const Icon = meta.icon;
            return (
              <div
                key={p}
                className="flex items-start gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3"
              >
                <Icon className={cn("mt-0.5 size-4", meta.color.split(" ").pop())} />
                <div>
                  <p className={cn("text-xs font-semibold", meta.color.split(" ").pop())}>
                    {meta.label}
                  </p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {p === "read" && "Anyone on the PR can run these read-only commands."}
                    {p === "write" && "Requires write access to the repository."}
                    {p === "admin" && "Admin only. Destructive actions are audit-logged."}
                  </p>
                </div>
              </div>
            );
          })}
        </div>
      </Panel>
    </div>
  );
}
