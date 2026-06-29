"use client";

import { useState } from "react";
import { Menu, RefreshCw, Pause, Play, Radio } from "lucide-react";
import type { SectionId } from "@/components/Sidebar";
import { Sidebar } from "@/components/Sidebar";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { useToast } from "@/hooks/use-toast";
import { useSSE } from "@/hooks/use-sse";
import { pauseAutonomous, resumeAutonomous } from "@/lib/api";
import { cn } from "@/lib/utils";

import { Dashboard } from "@/components/sections/Dashboard";
import { Incidents } from "@/components/sections/Incidents";
import { Providers } from "@/components/sections/Providers";
import { AutoMerge } from "@/components/sections/AutoMerge";
import { Sandbox } from "@/components/sections/Sandbox";
import { Metrics } from "@/components/sections/Metrics";
import { Chatops } from "@/components/sections/Chatops";
import { Settings } from "@/components/sections/Settings";

const VERSION = "v4.4.0";

export default function Page() {
  const [section, setSection] = useState<SectionId>("dashboard");
  const [refreshKey, setRefreshKey] = useState(0);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [paused, setPaused] = useState(false);
  const [pauseLoading, setPauseLoading] = useState(false);
  const { toast } = useToast();
  const { status: sseStatus } = useSSE();

  async function handlePauseToggle() {
    setPauseLoading(true);
    try {
      if (paused) {
        await resumeAutonomous();
        setPaused(false);
        toast({ title: "Resumed", description: "Autonomous mode is active." });
      } else {
        await pauseAutonomous(60);
        setPaused(true);
        toast({
          title: "Paused",
          description: "Autonomous mode paused for 60 minutes.",
        });
      }
    } catch (err) {
      toast({
        title: paused ? "Resume failed" : "Pause failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      });
    } finally {
      setPauseLoading(false);
    }
  }

  function handleRefresh() {
    setRefreshKey((k) => k + 1);
  }

  function selectSection(id: SectionId) {
    setSection(id);
    setMobileNavOpen(false);
  }

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      {/* Desktop sidebar */}
      <aside className="hidden shrink-0 lg:block">
        <Sidebar active={section} onSelect={selectSection} />
      </aside>

      {/* Mobile sidebar */}
      <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <SheetContent
          side="left"
          className="w-[220px] border-zinc-800 bg-sidebar p-0 sm:max-w-[220px]"
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Navigation</SheetTitle>
          </SheetHeader>
          <Sidebar active={section} onSelect={selectSection} />
        </SheetContent>
      </Sheet>

      {/* Main column */}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* Header */}
        <header className="flex h-16 shrink-0 items-center gap-3 border-b border-zinc-800 bg-background/80 px-4 backdrop-blur lg:px-6">
          <Button
            variant="ghost"
            size="icon"
            className="lg:hidden"
            onClick={() => setMobileNavOpen(true)}
            aria-label="Open navigation"
          >
            <Menu className="size-5" />
          </Button>

          <div className="flex items-center gap-2">
            <h2 className="font-mono text-sm font-semibold uppercase tracking-wide text-zinc-200">
              {section}
            </h2>
            <span
              className={cn(
                "flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] uppercase tracking-wide",
                sseStatus === "open"
                  ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                  : sseStatus === "error"
                    ? "border-rose-500/30 bg-rose-500/10 text-rose-400"
                    : "border-amber-500/30 bg-amber-500/10 text-amber-400",
              )}
            >
              <Radio
                className={cn(
                  "size-2.5",
                  sseStatus === "open" && "animate-sentinel-pulse",
                )}
              />
              {sseStatus === "open" ? "live" : sseStatus === "error" ? "offline" : "connecting"}
            </span>
          </div>

          <div className="ml-auto flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={handlePauseToggle}
              disabled={pauseLoading}
              className={cn(
                "border-zinc-800",
                paused
                  ? "border-amber-500/40 bg-amber-500/10 text-amber-400 hover:bg-amber-500/20"
                  : "text-zinc-300 hover:text-zinc-100",
              )}
            >
              {pauseLoading ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : paused ? (
                <Play className="size-3.5" />
              ) : (
                <Pause className="size-3.5" />
              )}
              <span className="hidden sm:inline">
                {paused ? "Resume" : "Pause"}
              </span>
            </Button>

            <Button
              variant="ghost"
              size="icon"
              onClick={handleRefresh}
              aria-label="Refresh"
              className="text-muted-foreground hover:text-zinc-100"
            >
              <RefreshCw className="size-4" />
            </Button>

            <div className="hidden items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 sm:flex">
              <span className="size-1.5 rounded-full bg-cyan-500" />
              <span className="font-mono text-xs text-muted-foreground">
                {VERSION}
              </span>
            </div>
          </div>
        </header>

        {/* Section content */}
        <main className="flex-1 overflow-y-auto scrollbar-sentinel p-4 lg:p-6">
          <div className="mx-auto max-w-7xl">
            {section === "dashboard" && <Dashboard refreshKey={refreshKey} />}
            {section === "incidents" && <Incidents refreshKey={refreshKey} />}
            {section === "providers" && <Providers refreshKey={refreshKey} />}
            {section === "automerge" && <AutoMerge refreshKey={refreshKey} />}
            {section === "sandbox" && <Sandbox refreshKey={refreshKey} />}
            {section === "metrics" && <Metrics refreshKey={refreshKey} />}
            {section === "chatops" && <Chatops />}
            {section === "settings" && <Settings refreshKey={refreshKey} />}
          </div>
        </main>
      </div>
    </div>
  );
}
