"use client";

import { useEffect, useRef } from "react";
import { motion } from "framer-motion";
import { Panel, PanelHeader, PulseDot, Mono, ConfidenceBar } from "@/components/sentinel/common";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { useSentinel } from "@/lib/store";
import { pipelinePhases, incidents } from "@/lib/mock-data";
import { Prism as SyntaxHighlighter } from "react-syntax-highlighter";
import { vscDarkPlus } from "react-syntax-highlighter/dist/esm/styles/prism";
import {
  CheckCircle2,
  Circle,
  Loader2,
  Terminal,
  GitPullRequest,
  Brain,
  Activity,
  Clock,
  DollarSign,
  Zap,
} from "lucide-react";
import { cn } from "@/lib/utils";

function PipelineStepper() {
  const phaseIdx = useSentinel((s) => s.livePhaseIndex);
  const progress = useSentinel((s) => s.livePhaseProgress);

  return (
    <Panel>
      <PanelHeader
        title="Investigation Pipeline"
        sub="INC-7A3F9B2C · checkout-api · payments-prod"
        right={
          <Badge variant="outline" className="border-cyan-500/30 bg-cyan-500/10 font-mono text-[10px] text-cyan-300">
            <PulseDot color="bg-cyan-400" className="h-1.5 w-1.5" />
            live
          </Badge>
        }
      />
      <div className="px-4 py-4">
        <div className="flex items-center gap-1 overflow-x-auto scrollbar-thin">
          {pipelinePhases.map((phase, i) => {
            const done = i < phaseIdx;
            const current = i === phaseIdx;
            const upcoming = i > phaseIdx;
            return (
              <div key={phase} className="flex flex-1 items-center">
                <div className="flex flex-col items-center gap-1.5">
                  <div
                    className={cn(
                      "relative flex h-8 w-8 items-center justify-center rounded-full border",
                      done && "border-emerald-500/40 bg-emerald-500/15 text-emerald-400",
                      current && "border-cyan-500 bg-cyan-500/20 text-cyan-300",
                      upcoming && "border-zinc-700 bg-zinc-900 text-zinc-600"
                    )}
                  >
                    {done ? (
                      <CheckCircle2 className="h-4 w-4" />
                    ) : current ? (
                      <>
                        <motion.span
                          animate={{ scale: [1, 1.15, 1] }}
                          transition={{ duration: 1.5, repeat: Infinity }}
                          className="absolute inset-0 rounded-full border-2 border-cyan-400/60"
                        />
                        <Loader2 className="h-4 w-4 animate-spin" />
                      </>
                    ) : (
                      <Circle className="h-4 w-4" />
                    )}
                  </div>
                  <span
                    className={cn(
                      "whitespace-nowrap text-[10px] font-medium",
                      done && "text-emerald-400",
                      current && "text-cyan-300",
                      upcoming && "text-zinc-600"
                    )}
                  >
                    {phase}
                  </span>
                </div>
                {i < pipelinePhases.length - 1 && (
                  <div className="mx-1 h-px flex-1 bg-zinc-800">
                    <div
                      className={cn("h-px transition-all duration-500", done ? "bg-emerald-500/60" : current ? "bg-cyan-500/60" : "bg-transparent")}
                      style={{ width: current ? `${progress}%` : done ? "100%" : "0%" }}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
        <div className="mt-3 flex items-center justify-between text-[11px]">
          <span className="text-zinc-500">
            Current phase: <span className="font-mono text-cyan-300">{pipelinePhases[phaseIdx]}</span>
          </span>
          <span className="font-mono text-zinc-400">{progress.toFixed(0)}% complete</span>
        </div>
      </div>
    </Panel>
  );
}

function LiveConsole() {
  const lines = useSentinel((s) => s.consoleLines);
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [lines]);

  return (
    <Panel className="flex flex-col">
      <PanelHeader
        title="Live Sandbox Console"
        sub="snd-7a3f9b2c · canary-7a3f9b2c"
        right={
          <div className="flex items-center gap-2">
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
              <PulseDot color="bg-emerald-400" className="h-1.5 w-1.5" />
              streaming
            </Badge>
            <span className="font-mono text-[10px] text-zinc-500">{lines.length} lines</span>
          </div>
        }
      />
      <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-2">
        <div className="flex items-center gap-2 text-[10px] text-zinc-500">
          <Terminal className="h-3 w-3" />
          <span className="font-mono">sustained-window: L5 in progress</span>
        </div>
        <div className="flex items-center gap-2">
          <span className="font-mono text-[10px] text-zinc-500">2m 14s / 5m</span>
          <div className="h-1.5 w-24 overflow-hidden rounded-full bg-zinc-800">
            <div className="h-full rounded-full bg-cyan-500 transition-all" style={{ width: "45%" }} />
          </div>
        </div>
      </div>
      <div
        ref={scrollRef}
        className="max-h-[420px] min-h-[280px] overflow-y-auto bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed scrollbar-thin"
      >
        {lines.length === 0 && (
          <div className="text-zinc-600">$ waiting for sandbox output...</div>
        )}
        {lines.map((line, i) => (
          <motion.div
            key={i}
            initial={{ opacity: 0, x: -4 }}
            animate={{ opacity: 1, x: 0 }}
            className="flex gap-2"
          >
            <span className="shrink-0 text-zinc-700">
              {new Date(line.ts).toLocaleTimeString("en-US", { hour12: false })}
            </span>
            <span
              className={cn(
                "shrink-0 font-semibold",
                line.level === 1 && "text-cyan-400",
                line.level === 2 && "text-amber-400",
                line.level === 3 && "text-emerald-400",
                line.level === 4 && "text-purple-400",
                line.level === 5 && "text-rose-400"
              )}
            >
              [L{line.level}]
            </span>
            <span
              className={cn(
                "text-zinc-300",
                line.text.includes("✓") && "text-emerald-300",
                line.text.includes("PASS") && "text-emerald-300 font-semibold",
                line.text.includes("FAIL") && "text-rose-300 font-semibold",
                line.text.includes("Promoting") && "text-cyan-300 font-semibold"
              )}
            >
              {line.text}
            </span>
          </motion.div>
        ))}
        <div className="mt-1 flex gap-2">
          <span className="text-cyan-400">$</span>
          <span className="animate-blink text-zinc-400">▊</span>
        </div>
      </div>
    </Panel>
  );
}

function AgentTraceTree() {
  const incident = incidents[0]; // The active incident
  return (
    <Panel>
      <PanelHeader
        title="Agent Trace Tree"
        sub="LLM calls with tokens, cost, duration"
        right={
          <div className="flex items-center gap-2 font-mono text-[10px] text-zinc-500">
            <Zap className="h-3 w-3 text-cyan-400" />
            <span className="text-cyan-300">2 agents running</span>
          </div>
        }
      />
      <div className="px-2 py-2">
        <TraceNode node={incident.agentTrace} depth={0} />
      </div>
      <div className="border-t border-zinc-800 px-4 py-2.5">
        <div className="grid grid-cols-3 gap-3 text-[11px]">
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <Clock className="h-3 w-3" /> Duration
            </div>
            <Mono className="text-zinc-300">4m 12s</Mono>
          </div>
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <Activity className="h-3 w-3" /> Tokens
            </div>
            <Mono className="text-zinc-300">39.8k</Mono>
          </div>
          <div>
            <div className="flex items-center gap-1 text-zinc-500">
              <DollarSign className="h-3 w-3" /> Cost
            </div>
            <Mono className="text-cyan-300">$0.056</Mono>
          </div>
        </div>
      </div>
    </Panel>
  );
}

function TraceNode({ node, depth }: { node: any; depth: number }) {
  const expanded = useSentinel((s) => s.traceExpandedNodes.has(node.id));
  const toggle = useSentinel((s) => s.toggleTraceNode);
  const hasChildren = (node.children?.length ?? 0) > 0;

  return (
    <div>
      <div
        className="flex items-center gap-2 rounded px-2 py-1.5 hover:bg-zinc-800/40 cursor-pointer"
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => toggle(node.id)}
      >
        <span className="text-zinc-500">
          {hasChildren ? (expanded ? "▼" : "▶") : "·"}
        </span>
        <span
          className={cn(
            "h-1.5 w-1.5 rounded-full",
            node.status === "completed" && "bg-emerald-400",
            node.status === "running" && "bg-cyan-400 animate-pulse-dot",
            node.status === "failed" && "bg-rose-400"
          )}
        />
        <span className="text-xs text-zinc-200">{node.name}</span>
        <Badge variant="outline" className="font-mono text-[10px] border-zinc-700 bg-zinc-900 text-zinc-400">
          {node.agent}
        </Badge>
        <span className="ml-auto flex items-center gap-2 font-mono text-[10px] text-zinc-500">
          <span>{node.duration}</span>
          <span className="text-cyan-400">${node.cost.toFixed(3)}</span>
        </span>
      </div>
      {expanded && (
        <div
          className="ml-6 mb-2 rounded-md border border-zinc-800 bg-zinc-950 p-2.5"
          style={{ marginLeft: `${depth * 16 + 24}px` }}
        >
          {node.llmInput && (
            <div className="mb-2">
              <div className="mb-1 flex items-center gap-1 text-[9px] uppercase tracking-wider text-zinc-500">
                <Terminal className="h-2.5 w-2.5" /> Input
              </div>
              <p className="font-mono text-[10.5px] leading-relaxed text-zinc-400 line-clamp-2">{node.llmInput}</p>
            </div>
          )}
          {node.llmOutput && (
            <div>
              <div className="mb-1 flex items-center gap-1 text-[9px] uppercase tracking-wider text-zinc-500">
                <Brain className="h-2.5 w-2.5" /> Output
              </div>
              <pre className="max-h-48 overflow-y-auto whitespace-pre-wrap font-mono text-[10.5px] leading-relaxed text-zinc-300 scrollbar-thin">
                {node.llmOutput}
              </pre>
            </div>
          )}
        </div>
      )}
      {hasChildren && (
        <div className="border-l border-zinc-800 ml-3">
          {node.children.map((c: any) => (
            <TraceNode key={c.id} node={c} depth={depth + 1} />
          ))}
        </div>
      )}
    </div>
  );
}

function FixDiffViewer() {
  const incident = incidents[0];
  return (
    <Panel>
      <PanelHeader
        title="Fix Diff"
        sub={`${incident.fixProposal.filesChanged[0]?.path ?? ""} · +${incident.fixProposal.filesChanged[0]?.additions ?? 0} −${incident.fixProposal.filesChanged[0]?.deletions ?? 0}`}
        right={
          <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
            <GitPullRequest className="h-3 w-3" />
            PR #4825
          </Badge>
        }
      />
      <div className="overflow-x-auto">
        <SyntaxHighlighter
          language="python"
          style={vscDarkPlus}
          customStyle={{
            margin: 0,
            background: "#09090B",
            padding: "12px",
            fontSize: "11px",
            lineHeight: "1.55",
          }}
          showLineNumbers
          lineNumberStyle={{ color: "#3F3F46", paddingRight: "12px", minWidth: "2em" }}
        >
          {incident.fixProposal.filesChanged[0]?.diff ?? ""}
        </SyntaxHighlighter>
      </div>
    </Panel>
  );
}

export function LiveInvestigationSection() {
  const livePhaseIndex = useSentinel((s) => s.livePhaseIndex);
  const livePhaseProgress = useSentinel((s) => s.livePhaseProgress);
  const incident = incidents[0];

  return (
    <div className="space-y-3 p-6">
      {/* Header strip with phase status */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Incident</div>
            <div className="mt-1 flex items-center gap-2">
              <Mono className="text-sm text-cyan-400">{incident.id}</Mono>
              <PulseDot color="bg-amber-400" className="h-2 w-2" />
            </div>
            <div className="mt-1 text-[11px] text-zinc-400">{incident.service} · {incident.namespace}</div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Phase</div>
            <div className="mt-1 flex items-center gap-2">
              <Loader2 className="h-3.5 w-3.5 animate-spin text-cyan-400" />
              <span className="font-mono text-sm text-cyan-300">{pipelinePhases[livePhaseIndex]}</span>
            </div>
            <div className="mt-2"><Progress value={livePhaseProgress} className="h-1 bg-zinc-800" /></div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Confidence</div>
            <div className="mt-1"><ConfidenceBar value={incident.confidence} /></div>
            <div className="mt-1 text-[11px] text-zinc-500">autonomous threshold: 85%</div>
          </div>
        </Panel>
        <Panel className="lg:col-span-1">
          <div className="px-4 py-3">
            <div className="text-[10px] uppercase tracking-wider text-zinc-500">Elapsed</div>
            <Mono className="mt-1 block text-sm text-zinc-200">4m 12s</Mono>
            <div className="mt-1 text-[11px] text-zinc-500">est. remaining: 1m 48s</div>
          </div>
        </Panel>
      </div>

      <PipelineStepper />

      {/* Main grid: trace tree + live console */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <AgentTraceTree />
        <LiveConsole />
      </div>

      <FixDiffViewer />
    </div>
  );
}
