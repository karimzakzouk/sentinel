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
import { useSentinel, startLiveSimulation, type SectionId } from "@/lib/store";
import { incidents } from "@/lib/mock-data";
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
  const open = useSentinel((s) => s.commandOpen);
  const setOpen = useSentinel((s) => s.setCommandOpen);
  const setActiveSection = useSentinel((s) => s.setActiveSection);
  const setSelectedIncident = useSentinel((s) => s.setSelectedIncident);
  const toggleDetection = useSentinel((s) => s.toggleDetection);
  const detectionPaused = useSentinel((s) => s.detectionPaused);
  const { toast } = useToast();

  // Ensure simulation runs once on the client
  if (typeof window !== "undefined") startLiveSimulation();

  const goTo = (id: SectionId) => {
    setActiveSection(id);
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
          {incidents.slice(0, 6).map((inc) => (
            <CommandItem
              key={inc.id}
              onSelect={() => {
                setSelectedIncident(inc.id);
                setActiveSection("incidents");
                setOpen(false);
              }}
            >
              <Search className="h-4 w-4 text-zinc-400" />
              <span className="font-mono text-xs text-zinc-300">{inc.id}</span>
              <span className="text-zinc-500">— {inc.service} · {inc.errorType}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="Actions">
          <CommandItem
            onSelect={() => {
              toggleDetection();
              toast({
                title: detectionPaused ? "Detection resumed" : "Detection paused",
                description: detectionPaused ? "Autonomous detection is now running" : "Autonomous detection paused for 30m",
              });
              setOpen(false);
            }}
          >
            {detectionPaused ? <Play className="h-4 w-4 text-emerald-400" /> : <Pause className="h-4 w-4 text-amber-400" />}
            <span>{detectionPaused ? "Resume detection" : "Pause detection"}</span>
            <CommandShortcut>⌘P</CommandShortcut>
          </CommandItem>
          <CommandItem
            onSelect={() => {
              setActiveSection("live");
              setOpen(false);
            }}
          >
            <Radio className="h-4 w-4 text-cyan-400" />
            <span>Trigger new investigation</span>
            <CommandShortcut>⌘I</CommandShortcut>
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}
