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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { StatusBadge, ConfidenceBar, LevelBadge, timeAgo, timeMDHM, Mono, Panel } from "@/components/sentinel/common";
import { useSentinel } from "@/lib/store";
import { incidents, type Incident, type AgentNode } from "@/lib/mock-data";
import {
  Search,
  Filter,
  ChevronRight,
  ChevronDown,
  FileCode2,
  GitBranch,
  AlertTriangle,
  CheckCircle2,
  XCircle,
  Clock,
  Brain,
  Terminal,
  Wrench,
  GitPullRequest,
  Activity,
} from "lucide-react";
import { Prism as SyntaxHighlighter } from "react-syntax-highlighter";
import { vscDarkPlus } from "react-syntax-highlighter/dist/esm/styles/prism";
import { cn } from "@/lib/utils";

const statusOptions = [
  { value: "all", label: "All statuses" },
  { value: "investigating", label: "Investigating" },
  { value: "verified", label: "Verified" },
  { value: "failed", label: "Failed" },
  { value: "escalated", label: "Escalated" },
  { value: "resolved", label: "Resolved" },
];

function AgentTraceNode({ node, depth = 0 }: { node: AgentNode; depth?: number }) {
  const expanded = useSentinel((s) => s.traceExpandedNodes.has(node.id));
  const toggle = useSentinel((s) => s.toggleTraceNode);
  const hasChildren = (node.children?.length ?? 0) > 0;

  const statusColor =
    node.status === "completed"
      ? "text-emerald-400"
      : node.status === "running"
      ? "text-cyan-400"
      : "text-rose-400";

  const statusIcon =
    node.status === "completed" ? <CheckCircle2 className="h-3 w-3" /> :
    node.status === "running" ? (
      <span className="relative flex h-3 w-3 items-center justify-center">
        <span className="absolute inline-flex h-2 w-2 animate-ping rounded-full bg-cyan-400 opacity-75" />
        <span className="relative h-2 w-2 rounded-full bg-cyan-400" />
      </span>
    ) : <XCircle className="h-3 w-3" />;

  return (
    <div className="select-none">
      <div
        className={cn(
          "group flex items-center gap-2 rounded px-2 py-1.5 hover:bg-zinc-800/40",
          expanded && "bg-zinc-800/30"
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => hasChildren && toggle(node.id)}
      >
        {hasChildren ? (
          expanded ? <ChevronDown className="h-3 w-3 text-zinc-500" /> : <ChevronRight className="h-3 w-3 text-zinc-500" />
        ) : (
          <span className="w-3" />
        )}
        <span className={statusColor}>{statusIcon}</span>
        <span className="text-xs text-zinc-200">{node.name}</span>
        <Badge variant="outline" className="ml-1 font-mono text-[10px] text-zinc-400 border-zinc-700 bg-zinc-900">
          {node.agent}
        </Badge>
        <span className="ml-auto flex items-center gap-3 font-mono text-[10px] text-zinc-500">
          <span>{node.duration}</span>
          <span>{(node.tokens / 1000).toFixed(1)}k tok</span>
          <span className="text-cyan-400">${node.cost.toFixed(3)}</span>
        </span>
      </div>
      {expanded && node.llmOutput && (
        <div
          className="mt-1 mb-2 space-y-2 rounded-md border border-zinc-800 bg-zinc-950 p-2.5"
          style={{ marginLeft: `${depth * 16 + 24}px` }}
        >
          {node.llmInput && (
            <div>
              <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
                <Terminal className="h-3 w-3" /> LLM Input
              </div>
              <p className="font-mono text-[11px] leading-relaxed text-zinc-400 line-clamp-2">{node.llmInput}</p>
            </div>
          )}
          <div>
            <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
              <Brain className="h-3 w-3" /> LLM Output
            </div>
            <pre className="max-h-60 overflow-y-auto whitespace-pre-wrap font-mono text-[11px] leading-relaxed text-zinc-300 scrollbar-thin">
              {node.llmOutput}
            </pre>
          </div>
        </div>
      )}
      {expanded && hasChildren && (
        <div className="border-l border-zinc-800 ml-3">
          {node.children!.map((c) => (
            <AgentTraceNode key={c.id} node={c} depth={depth + 1} />
          ))}
        </div>
      )}
    </div>
  );
}

function IncidentDetail({ incident }: { incident: Incident }) {
  return (
    <div className="space-y-5 px-4 py-4">
      {/* Crash location */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <FileCode2 className="h-3 w-3" /> Crash Location
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
          <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-[11px]">
            <div className="text-zinc-500">File</div>
            <div className="font-mono text-zinc-200">{incident.crashFile}:{incident.crashLine}</div>
            <div className="text-zinc-500">Function</div>
            <div className="font-mono text-zinc-200">{incident.crashFunction}()</div>
            <div className="text-zinc-500">Error type</div>
            <div className="font-mono text-rose-400">{incident.errorType}</div>
            <div className="text-zinc-500">Namespace</div>
            <div className="font-mono text-zinc-200">{incident.namespace}</div>
          </div>
        </div>
      </section>

      {/* Error excerpt */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <AlertTriangle className="h-3 w-3" /> Error Excerpt
        </h4>
        <div className="overflow-hidden rounded-md border border-zinc-800">
          <SyntaxHighlighter
            language="python"
            style={vscDarkPlus}
            customStyle={{
              margin: 0,
              background: "#09090B",
              padding: "12px",
              fontSize: "11px",
              lineHeight: "1.5",
            }}
            showLineNumbers
            lineNumberStyle={{ color: "#3F3F46", paddingRight: "12px", minWidth: "2em" }}
          >
            {incident.errorExcerpt}
          </SyntaxHighlighter>
        </div>
      </section>

      {/* Root cause */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Brain className="h-3 w-3" /> Root Cause
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
          <p className="text-[12px] leading-relaxed text-zinc-300">{incident.rootCause}</p>
          <div className="mt-2 flex items-center gap-2 border-t border-zinc-800 pt-2">
            <span className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</span>
            <ConfidenceBar value={incident.rootCauseConfidence} />
          </div>
        </div>
      </section>

      {/* Hypotheses */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <GitBranch className="h-3 w-3" /> Hypotheses ({incident.hypotheses.length})
        </h4>
        <div className="space-y-1.5">
          {incident.hypotheses.map((h) => (
            <div
              key={h.id}
              className={cn(
                "flex items-center gap-3 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2",
                h.falsified && "opacity-50"
              )}
            >
              <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                {h.id}
              </Badge>
              <span className={cn("flex-1 text-[12px] text-zinc-300", h.falsified && "line-through")}>
                {h.statement}
              </span>
              {h.falsified ? (
                <Badge variant="outline" className="border-rose-500/30 bg-rose-500/10 font-mono text-[10px] text-rose-400">
                  falsified
                </Badge>
              ) : (
                <ConfidenceBar value={h.confidence} />
              )}
            </div>
          ))}
        </div>
      </section>

      {/* Fix proposal */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Wrench className="h-3 w-3" /> Fix Proposal
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-3">
          <p className="text-[12px] leading-relaxed text-zinc-300">{incident.fixProposal.description}</p>
          {incident.fixProposal.filesChanged.length > 0 && (
            <div className="mt-3 space-y-2">
              {incident.fixProposal.filesChanged.map((f) => (
                <div key={f.path} className="overflow-hidden rounded border border-zinc-800">
                  <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900 px-2.5 py-1.5">
                    <Mono className="text-[11px] text-zinc-300">{f.path}</Mono>
                    <div className="flex items-center gap-2 text-[10px] font-mono">
                      <span className="text-emerald-400">+{f.additions}</span>
                      <span className="text-rose-400">−{f.deletions}</span>
                    </div>
                  </div>
                  <SyntaxHighlighter
                    language="typescript"
                    style={vscDarkPlus}
                    customStyle={{
                      margin: 0,
                      background: "#09090B",
                      padding: "10px",
                      fontSize: "10.5px",
                      lineHeight: "1.45",
                    }}
                  >
                    {f.diff}
                  </SyntaxHighlighter>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {/* Sandbox levels */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <CheckCircle2 className="h-3 w-3" /> Sandbox Verification (L1–L5)
        </h4>
        <div className="flex items-center gap-2">
          {incident.sandboxLevels.map((lvl, i) => (
            <LevelBadge key={i} status={lvl} level={i + 1} />
          ))}
        </div>
      </section>

      {/* Agent trace */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Activity className="h-3 w-3" /> Agent Trace
        </h4>
        <div className="rounded-md border border-zinc-800 bg-zinc-950 p-1">
          <AgentTraceNode node={incident.agentTrace} />
        </div>
      </section>

      {/* Events timeline */}
      <section>
        <h4 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
          <Clock className="h-3 w-3" /> Events Timeline
        </h4>
        <div className="relative pl-4">
          <div className="absolute left-1.5 top-0 bottom-0 w-px bg-zinc-800" />
          {incident.events.map((e, i) => (
            <div key={i} className="relative mb-2.5 last:mb-0">
              <span className="absolute -left-[10px] top-1 h-2 w-2 rounded-full border border-zinc-700 bg-zinc-900" />
              <div className="flex items-baseline gap-2">
                <span className="font-mono text-[10px] text-zinc-600">{new Date(e.time).toLocaleTimeString("en-US", { hour12: false })}</span>
                <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                  {e.phase}
                </Badge>
                <span className="text-[11px] text-zinc-300">{e.message}</span>
              </div>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

export function IncidentsSection() {
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [serviceFilter, setServiceFilter] = useState<string>("all");
  const selectedId = useSentinel((s) => s.selectedIncidentId);
  const setSelectedIncident = useSentinel((s) => s.setSelectedIncident);
  const drawerOpen = useSentinel((s) => s.drawerOpen);
  const setDrawerOpen = useSentinel((s) => s.setDrawerOpen);

  const services = useMemo(() => ["all", ...Array.from(new Set(incidents.map((i) => i.service)))], []);

  const filtered = useMemo(() => {
    return incidents.filter((i) => {
      if (statusFilter !== "all" && i.status !== statusFilter) return false;
      if (serviceFilter !== "all" && i.service !== serviceFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          i.id.toLowerCase().includes(q) ||
          i.service.toLowerCase().includes(q) ||
          i.errorType.toLowerCase().includes(q) ||
          i.crashFile.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [search, statusFilter, serviceFilter]);

  const selected = incidents.find((i) => i.id === selectedId) ?? null;

  return (
    <div className="p-6">
      <Panel>
        {/* Filter bar */}
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-4 py-3">
          <div className="relative flex-1 min-w-[200px]">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-zinc-500" />
            <Input
              placeholder="Search incidents, services, error types..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="h-8 bg-zinc-900 pl-8 font-mono text-xs"
            />
          </div>
          <Select value={statusFilter} onValueChange={setStatusFilter}>
            <SelectTrigger size="sm" className="h-8 w-[150px] border-zinc-800 bg-zinc-900 font-mono text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {statusOptions.map((o) => (
                <SelectItem key={o.value} value={o.value} className="font-mono text-xs">{o.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={serviceFilter} onValueChange={setServiceFilter}>
            <SelectTrigger size="sm" className="h-8 w-[170px] border-zinc-800 bg-zinc-900 font-mono text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {services.map((s) => (
                <SelectItem key={s} value={s} className="font-mono text-xs">{s === "all" ? "All services" : s}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Badge variant="outline" className="h-8 gap-1 border-zinc-700 bg-zinc-900 px-2 font-mono text-[11px] text-zinc-400">
            <Filter className="h-3 w-3" />
            {filtered.length} / {incidents.length}
          </Badge>
        </div>

        {/* Table */}
        <div className="overflow-x-auto scrollbar-thin">
          <Table>
            <TableHeader>
              <TableRow className="border-zinc-800 hover:bg-transparent">
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">ID</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Service</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Status</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Error Type</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Confidence</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">PR</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">TTF</TableHead>
                <TableHead className="h-8 px-3 text-[10px] font-semibold uppercase tracking-wider text-zinc-500">Created</TableHead>
                <TableHead className="h-8 px-3" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((inc) => (
                <TableRow
                  key={inc.id}
                  onClick={() => setSelectedIncident(inc.id)}
                  className="cursor-pointer border-zinc-800/60 hover:bg-zinc-800/30"
                >
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-cyan-400">#{inc.id}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <div className="flex flex-col">
                      <span className="text-xs text-zinc-200">{inc.service}</span>
                      <Mono className="text-[10px] text-zinc-500">{inc.namespace}</Mono>
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-2"><StatusBadge status={inc.status} /></TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-rose-400">{inc.errorType.split(":")[0]}</Mono>
                  </TableCell>
                  <TableCell className="px-3 py-2"><ConfidenceBar value={inc.confidence} /></TableCell>
                  <TableCell className="px-3 py-2">
                    {inc.prNumber ? (
                      <Mono className="text-[11px] text-cyan-400">#{inc.prNumber}</Mono>
                    ) : (
                      <span className="text-[11px] text-zinc-600">—</span>
                    )}
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    {inc.timeToFix ? (
                      <Mono className="text-[11px] text-zinc-300">{inc.timeToFix}</Mono>
                    ) : (
                      <span className="text-[11px] text-zinc-600">—</span>
                    )}
                  </TableCell>
                  <TableCell className="px-3 py-2">
                    <Mono className="text-[11px] text-zinc-400">{timeMDHM(inc.created)}</Mono>
                    <div className="text-[10px] text-zinc-600">{timeAgo(inc.created)}</div>
                  </TableCell>
                  <TableCell className="px-3 py-2 text-right">
                    <ChevronRight className="h-3.5 w-3.5 text-zinc-600" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </Panel>

      {/* Drawer */}
      <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
        <SheetContent side="right" className="w-full overflow-y-auto border-zinc-800 bg-zinc-950 p-0 sm:max-w-2xl scrollbar-thin">
          {selected && (
            <>
              <SheetHeader className="border-b border-zinc-800 p-4">
                <div className="flex items-start justify-between pr-6">
                  <div className="flex flex-col gap-1.5">
                    <SheetTitle className="flex items-center gap-2 font-mono text-base text-zinc-50">
                      <AlertTriangle className="h-4 w-4 text-amber-400" />
                      {selected.id}
                    </SheetTitle>
                    <div className="flex flex-wrap items-center gap-2">
                      <StatusBadge status={selected.status} />
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-300">
                        {selected.service}
                      </Badge>
                      <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
                        {selected.namespace}
                      </Badge>
                      {selected.prNumber && (
                        <Badge variant="outline" className="font-mono text-[10px] border-cyan-500/30 bg-cyan-500/10 text-cyan-400">
                          <GitPullRequest className="h-3 w-3" /> PR #{selected.prNumber}
                        </Badge>
                      )}
                    </div>
                  </div>
                </div>
              </SheetHeader>
              <IncidentDetail incident={selected} />
              <div className="border-t border-zinc-800 p-4">
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" className="h-8 border-zinc-700 bg-zinc-900 text-xs text-zinc-300 hover:bg-zinc-800">
                    Pause investigation
                  </Button>
                  <Button variant="outline" size="sm" className="h-8 border-zinc-700 bg-zinc-900 text-xs text-zinc-300 hover:bg-zinc-800">
                    Escalate to human
                  </Button>
                  <Button size="sm" className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400">
                    Trigger sandbox run
                  </Button>
                </div>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
