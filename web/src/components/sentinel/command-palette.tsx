"use client";

import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from "@/components/ui/command";
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
  Play,
  Pause,
  Search,
} from "lucide-react";
import { useSentinelStore, type SectionId } from "@/lib/store";
import { useToast } from "@/hooks/use-toast";

const sections: { id: SectionId; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { id: "dashboard", label: "Dashboard", icon: LayoutDashboard },
  { id: "incidents", label: "Incidents", icon: AlertTriangle },
  { id: "live", label: "Live Investigation", icon: Radio },
  { id: "sandbox", label: "Sandbox", icon: FlaskConical },
  { id: "prs", label: "Pull Requests", icon: GitPullRequest },
  { id: "providers", label: "Providers", icon: Cpu },
  { id: "automerge", label: "Auto-Merge", icon: Merge },
  { id: "detection", label: "Detection", icon: Eye },
  { id: "chatops", label: "Chatops", icon: MessageSquare },
  { id: "metrics", label: "Metrics", icon: BarChart3 },
  { id: "settings", label: "Settings", icon: Settings },
  { id: "api", label: "API Explorer", icon: Terminal },
];

export function CommandPalette() {
  const open = useSentinelStore((s) => s.commandOpen);
  const setOpen = useSentinelStore((s) => s.setCommandOpen);
  const setActiveSection = useSentinelStore((s) => s.setActiveSection);
  const setSelectedIncident = useSentinelStore((s) => s.setSelectedIncident);
  const incidents = useSentinelStore((s) => s.incidents);
  const pauseAutonomous = useSentinelStore((s) => s.pauseAutonomous);
  const resumeAutonomous = useSentinelStore((s) => s.resumeAutonomous);
  const healthy = useSentinelStore((s) => s.healthy);
  const { toast } = useToast();

  const paused = !healthy;

  const goTo = (id: SectionId) => {
    setActiveSection(id);
    setOpen(false);
  };

  const onTogglePause = async () => {
    try {
      if (paused) {
        await resumeAutonomous();
        toast({ title: "Detection resumed", description: "Autonomous detection is running" });
      } else {
        await pauseAutonomous(30);
        toast({ title: "Detection paused", description: "Autonomous detection paused for 30m" });
      }
    } catch (err) {
      toast({
        title: "Action failed",
        description: err instanceof Error ? err.message : "Unknown error",
      });
    }
    setOpen(false);
  };

  return (
    <CommandDialog open={open} onOpenChange={setOpen}>
      <CommandInput placeholder="Type a command or search..." />
      <CommandList>
        <CommandEmpty>No results found.</CommandEmpty>

        <CommandGroup heading="Navigation">
          {sections.map((s) => {
            const Icon = s.icon;
            return (
              <CommandItem key={s.id} onSelect={() => goTo(s.id)}>
                <Icon className="h-4 w-4 text-zinc-400" />
                <span>Go to {s.label}</span>
              </CommandItem>
            );
          })}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="Search Incidents">
          {incidents.slice(0, 8).map((inc) => (
            <CommandItem
              key={inc.incident_id}
              onSelect={() => {
                setSelectedIncident(inc.incident_id);
                setActiveSection("incidents");
                setOpen(false);
              }}
            >
              <Search className="h-4 w-4 text-zinc-400" />
              <span className="font-mono text-xs text-zinc-300">{inc.incident_id.slice(0, 8)}</span>
              <span className="text-zinc-500">
                — {inc.cluster?.service ?? "unknown"} · {inc.status}
              </span>
            </CommandItem>
          ))}
          {incidents.length === 0 && (
            <CommandItem disabled>
              <span className="text-xs text-zinc-600">No incidents yet</span>
            </CommandItem>
          )}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="Actions">
          <CommandItem onSelect={onTogglePause}>
            {paused ? <Play className="h-4 w-4 text-emerald-400" /> : <Pause className="h-4 w-4 text-amber-400" />}
            <span>{paused ? "Resume detection" : "Pause detection (30m)"}</span>
            <CommandShortcut>⌘P</CommandShortcut>
          </CommandItem>
          <CommandItem
            onSelect={() => {
              setActiveSection("live");
              setOpen(false);
            }}
          >
            <Radio className="h-4 w-4 text-cyan-400" />
            <span>Open live investigation</span>
            <CommandShortcut>⌘I</CommandShortcut>
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}
