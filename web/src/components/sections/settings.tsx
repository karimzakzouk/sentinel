"use client";

import { useEffect, useMemo, useState } from "react";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useToast } from "@/hooks/use-toast";
import { useSentinelStore } from "@/lib/store";
import type { RuntimeConfig } from "@/lib/types";
import { Mono, ErrorState } from "@/components/sentinel/common";
import {
  Brain,
  Clock,
  FlaskConical,
  Activity,
  Server,
  Save,
  Loader2,
  Lock,
  AlertCircle,
  CheckCircle2,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface LiveField {
  key: keyof RuntimeConfig;
  label: string;
  type: "text" | "password" | "url" | "number" | "toggle" | "select";
  options?: string[];
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  hint?: string;
}

interface SettingsCardDef {
  id: string;
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  fields: LiveField[];
}

const cards: SettingsCardDef[] = [
  {
    id: "llm",
    title: "LLM",
    icon: Brain,
    fields: [
      { key: "llm_provider", label: "Provider", type: "select", options: ["openrouter", "openai", "anthropic", "groq", "nemotron", "ollama", "azure", "custom"], hint: "OpenAI-compatible provider name" },
      { key: "llm_base_url", label: "Base URL", type: "url", hint: "OpenAI-compatible endpoint" },
      { key: "llm_api_key", label: "API Key", type: "password" },
      { key: "llm_fast_model", label: "Fast Model", type: "text" },
      { key: "llm_frontier_model", label: "Frontier Model", type: "text" },
      { key: "llm_embedding_model", label: "Embedding Model", type: "text" },
      { key: "llm_max_retries", label: "Max Retries", type: "number", min: 0, max: 10, step: 1 },
      { key: "llm_retry_base_delay_ms", label: "Retry Base Delay", type: "number", min: 0, max: 10000, step: 100, unit: "ms" },
    ],
  },
  {
    id: "timeouts",
    title: "Timeouts",
    icon: Clock,
    fields: [
      { key: "investigator_timeout_sec", label: "Investigator", type: "number", min: 5, max: 600, step: 5, unit: "s" },
      { key: "evidence_gatherer_timeout_sec", label: "Evidence Gatherer", type: "number", min: 5, max: 600, step: 5, unit: "s" },
      { key: "analyst_timeout_sec", label: "Analyst", type: "number", min: 5, max: 600, step: 5, unit: "s" },
      { key: "fix_proposer_timeout_sec", label: "Fix Proposer", type: "number", min: 5, max: 600, step: 5, unit: "s" },
    ],
  },
  {
    id: "sandbox",
    title: "Sandbox",
    icon: FlaskConical,
    fields: [
      { key: "sandbox_enabled", label: "Enabled", type: "toggle" },
      { key: "sandbox_required", label: "Required for merge", type: "toggle" },
      { key: "sandbox_max_retries", label: "Max Retries", type: "number", min: 0, max: 5, step: 1 },
      { key: "skip_sandbox_non_prod", label: "Skip in non-prod", type: "toggle" },
    ],
  },
  {
    id: "confidence",
    title: "Confidence",
    icon: Activity,
    fields: [
      { key: "confidence_loop_back_threshold", label: "Loop-back Threshold", type: "number", min: 0, max: 1, step: 0.05, hint: "Below this confidence, the analyst loops back to the investigator" },
    ],
  },
  {
    id: "observability",
    title: "Observability",
    icon: Server,
    fields: [
      { key: "prometheus_url", label: "Prometheus URL", type: "url" },
      { key: "loki_url", label: "Loki URL", type: "url" },
      { key: "jaeger_url", label: "Jaeger URL", type: "url" },
      { key: "log_level", label: "Log Level", type: "select", options: ["debug", "info", "warn", "error"] },
    ],
  },
];

export function SettingsSection() {
  const runtimeConfig = useSentinelStore((s) => s.runtimeConfig);
  const refreshSettings = useSentinelStore((s) => s.refreshSettings);
  const saveSettings = useSentinelStore((s) => s.saveSettings);
  const { toast } = useToast();
  const [draft, setDraft] = useState<RuntimeConfig | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    refreshSettings().catch((err: unknown) => {
      setError(err instanceof Error ? err.message : "Failed to fetch /settings");
    });
  }, [refreshSettings]);

  useEffect(() => {
    if (runtimeConfig && !draft) setDraft({ ...runtimeConfig });
  }, [runtimeConfig, draft]);

  const dirty = useMemo(() => {
    if (!draft || !runtimeConfig) return false;
    return JSON.stringify(draft) !== JSON.stringify(runtimeConfig);
  }, [draft, runtimeConfig]);

  const updateField = (key: keyof RuntimeConfig, value: unknown) => {
    setDraft((prev) => (prev ? { ...prev, [key]: value } : prev));
  };

  const onSave = async () => {
    if (!draft) return;
    setSaving(true);
    setError(null);
    try {
      await saveSettings(draft);
      toast({ title: "Settings saved", description: "Runtime configuration updated. Some changes may require a restart." });
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Save failed";
      setError(msg);
      toast({ title: "Save failed", description: msg });
    } finally {
      setSaving(false);
    }
  };

  if (error && !runtimeConfig) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={() => refreshSettings()} />
      </div>
    );
  }

  if (!draft) {
    return (
      <div className="space-y-3 p-6">
        <div className="h-20 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
        <div className="h-64 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-xs text-zinc-500">Runtime configuration from </span>
          <Mono className="text-cyan-400">/settings</Mono>
          <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
            POST /settings
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          {dirty && (
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[10px] text-amber-400">
              unsaved
            </Badge>
          )}
          <Button
            onClick={onSave}
            disabled={!dirty || saving}
            size="sm"
            className="bg-cyan-500 text-zinc-950 hover:bg-cyan-400 disabled:opacity-50"
          >
            {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
            Save
          </Button>
        </div>
      </div>

      {error && (
        <div className="flex items-center gap-2 rounded-md border border-rose-500/30 bg-rose-500/5 p-2">
          <AlertCircle className="h-3.5 w-3.5 shrink-0 text-rose-400" />
          <p className="font-mono text-[11px] text-rose-300">{error}</p>
        </div>
      )}

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        {cards.map((card) => {
          const Icon = card.icon;
          return (
            <Card key={card.id} className="gap-0 border-zinc-800 bg-zinc-900/50 py-0">
              <div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-2.5">
                <Icon className="h-3.5 w-3.5 text-cyan-400" />
                <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-300">{card.title}</h3>
              </div>
              <div className="space-y-3 p-4">
                {card.fields.map((f) => (
                  <div key={String(f.key)} className="grid grid-cols-2 items-center gap-3">
                    <div>
                      <Label className="text-[11px] text-zinc-400">{f.label}</Label>
                      {f.hint && <p className="mt-0.5 text-[10px] text-zinc-600">{f.hint}</p>}
                    </div>
                    <div>
                      {f.type === "toggle" ? (
                        <Switch
                          checked={!!draft[f.key]}
                          onCheckedChange={(v) => updateField(f.key, v)}
                        />
                      ) : f.type === "select" ? (
                        <Select
                          value={String(draft[f.key] ?? "")}
                          onValueChange={(v) => updateField(f.key, v)}
                        >
                          <SelectTrigger className="h-8 bg-zinc-950 text-xs">
                            <SelectValue placeholder={f.label} />
                          </SelectTrigger>
                          <SelectContent>
                            {f.options?.map((o) => (
                              <SelectItem key={o} value={o}>{o}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      ) : f.type === "password" ? (
                        <Input
                          type="password"
                          value={String(draft[f.key] ?? "")}
                          onChange={(e) => updateField(f.key, e.target.value)}
                          className="h-8 bg-zinc-950 font-mono text-xs"
                        />
                      ) : (
                        <div className="relative">
                          <Input
                            type={f.type === "number" ? "number" : "text"}
                            value={String(draft[f.key] ?? "")}
                            onChange={(e) =>
                              updateField(
                                f.key,
                                f.type === "number" ? Number(e.target.value) : e.target.value,
                              )
                            }
                            min={f.min}
                            max={f.max}
                            step={f.step}
                            className={cn("h-8 bg-zinc-950 font-mono text-xs", f.unit && "pr-10")}
                          />
                          {f.unit && (
                            <span className="absolute right-2 top-1/2 -translate-y-1/2 font-mono text-[10px] text-zinc-600">
                              {f.unit}
                            </span>
                          )}
                        </div>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </Card>
          );
        })}
      </div>

      <Card className="gap-0 border-zinc-800 bg-zinc-900/50 py-0">
        <div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-2.5">
          <Lock className="h-3.5 w-3.5 text-amber-400" />
          <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-300">Startup-only configuration</h3>
        </div>
        <div className="p-4 text-[11px] text-zinc-500">
          <p>
            Settings like <Mono className="text-zinc-300">SENTINEL_API_TOKEN</Mono>,{" "}
            <Mono className="text-zinc-300">GITHUB_TOKEN</Mono>,{" "}
            <Mono className="text-zinc-300">SQLITE_PATH</Mono>, and the GitHub App credentials are read at
            startup. To change them, edit the Helm <Mono className="text-zinc-300">values.yaml</Mono> or the
            deployment env vars and restart the pod.
          </p>
          <div className="mt-3 flex items-center gap-2 rounded-md border border-cyan-500/20 bg-cyan-500/5 p-2">
            <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-cyan-400" />
            <p>
              The GitHub App can be configured live via <Mono className="text-cyan-400">POST /settings/github-app</Mono> —
              see API Explorer.
            </p>
          </div>
        </div>
      </Card>
    </div>
  );
}
