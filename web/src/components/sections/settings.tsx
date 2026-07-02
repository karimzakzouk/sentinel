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
import { updateRuntimeConfig } from "@/lib/api";
import type { RuntimeConfig } from "@/lib/types";
import { Mono } from "@/components/sentinel/common";
import {
  Brain,
  Clock,
  Target,
  FlaskConical,
  Activity,
  Eye,
  Boxes,
  GitBranch,
  MessageSquare,
  Container,
  HardDrive,
  Bell,
  Database,
  Server,
  Wrench,
  MessageCircle,
  Zap,
  RotateCcw,
  Save,
  Loader2,
  Lock,
  AlertCircle,
} from "lucide-react";

// ─── Field schema definitions ───────────────────────────────────────────────
// "live" fields map directly to a RuntimeConfig key (editable + saveable via
// PUT /settings). "startup" fields are read-only — they're set at boot time
// and require a server restart to change.

interface LiveField {
  kind: "live";
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

interface StartupField {
  kind: "startup";
  label: string;
  type: "text" | "password" | "url" | "csv" | "textarea";
  hint?: string;
}

type Field = LiveField | StartupField;

interface SettingsCardDef {
  id: string;
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  fields: Field[];
}

const cards: SettingsCardDef[] = [
  {
    id: "llm",
    title: "LLM",
    icon: Brain,
    fields: [
      { kind: "live", key: "llm_provider", label: "Provider", type: "select", options: ["openrouter", "openai", "anthropic", "groq", "nemotron", "ollama", "azure", "custom"], hint: "OpenAI-compatible provider name" },
      { kind: "live", key: "llm_base_url", label: "Base URL", type: "url", hint: "OpenAI-compatible endpoint" },
      { kind: "live", key: "llm_api_key", label: "API Key", type: "password" },
      { kind: "live", key: "llm_fast_model", label: "Fast Model", type: "text" },
      { kind: "live", key: "llm_frontier_model", label: "Frontier Model", type: "text" },
      { kind: "live", key: "llm_embedding_model", label: "Embedding Model", type: "text" },
      { kind: "live", key: "llm_max_retries", label: "Max Retries", type: "number", min: 0, max: 10, step: 1 },
      { kind: "live", key: "llm_retry_base_delay_ms", label: "Retry Base Delay", type: "number", min: 0, max: 10000, step: 100, unit: "ms" },
    ],
  },
  {
    id: "timeouts",
    title: "Timeouts",
    icon: Clock,
    fields: [
      { kind: "live", key: "investigator_timeout_sec", label: "Investigator", type: "number", min: 30, max: 600, step: 15, unit: "s" },
      { kind: "live", key: "evidence_gatherer_timeout_sec", label: "Evidence Collector", type: "number", min: 30, max: 600, step: 15, unit: "s" },
      { kind: "live", key: "analyst_timeout_sec", label: "Analyst", type: "number", min: 60, max: 900, step: 15, unit: "s" },
      { kind: "live", key: "fix_proposer_timeout_sec", label: "Fix Proposer", type: "number", min: 60, max: 1200, step: 30, unit: "s" },
    ],
  },
  {
    id: "confidence",
    title: "Confidence Thresholds",
    icon: Target,
    fields: [
      { kind: "live", key: "confidence_loop_back_threshold", label: "Loop-Back Threshold", type: "number", min: 0, max: 100, step: 1, unit: "%", hint: "Below this confidence, the analyst loops back to gather more evidence" },
    ],
  },
  {
    id: "sandbox",
    title: "Sandbox",
    icon: FlaskConical,
    fields: [
      { kind: "live", key: "sandbox_enabled", label: "Sandbox Enabled", type: "toggle" },
      { kind: "live", key: "sandbox_required", label: "Sandbox Required", type: "toggle", hint: "Block auto-merge if sandbox verification hasn't passed" },
      { kind: "live", key: "sandbox_max_retries", label: "Max Retries", type: "number", min: 0, max: 10, step: 1 },
      { kind: "live", key: "skip_sandbox_non_prod", label: "Skip Sandbox in Non-Prod", type: "toggle" },
    ],
  },
  {
    id: "observability",
    title: "Observability",
    icon: Activity,
    fields: [
      { kind: "live", key: "prometheus_url", label: "Prometheus URL", type: "url" },
      { kind: "live", key: "loki_url", label: "Loki URL", type: "url" },
      { kind: "live", key: "jaeger_url", label: "Jaeger URL", type: "url" },
      { kind: "live", key: "log_level", label: "Log Level", type: "select", options: ["debug", "info", "warn", "error"] },
    ],
  },
  {
    id: "github-app",
    title: "GitHub App",
    icon: GitBranch,
    fields: [
      { kind: "live", key: "github_app_id", label: "App ID", type: "text" },
      { kind: "live", key: "github_app_install_id", label: "Installation ID", type: "text" },
      { kind: "live", key: "github_app_private_key", label: "Private Key (PEM)", type: "password", hint: "Paste the full PEM contents — stored encrypted" },
    ],
  },
  // Startup-only cards (read-only in the UI)
  {
    id: "detection",
    title: "Detection (startup)",
    icon: Eye,
    fields: [
      { kind: "startup", label: "Detection Cooldown (sec)", type: "text", hint: "Set via DETECTION_COOLDOWN_SEC at boot" },
      { kind: "startup", label: "Detection Tail Lines", type: "text", hint: "Set via DETECTION_TAIL_LINES at boot" },
      { kind: "startup", label: "Max Concurrent Investigations", type: "text", hint: "Set via MAX_CONCURRENT_INVESTIGATIONS at boot" },
      { kind: "startup", label: "Max Pipeline Iterations", type: "text", hint: "Set via MAX_PIPELINE_ITERATIONS at boot" },
    ],
  },
  {
    id: "k8s",
    title: "Kubernetes (startup)",
    icon: Boxes,
    fields: [
      { kind: "startup", label: "Namespace", type: "text" },
      { kind: "startup", label: "Release Name", type: "text" },
      { kind: "startup", label: "Production Namespaces", type: "csv" },
      { kind: "startup", label: "Write Namespaces", type: "csv" },
      { kind: "startup", label: "Skip Namespaces", type: "csv" },
      { kind: "startup", label: "High-Risk Services", type: "csv" },
    ],
  },
  {
    id: "confidence-thresholds",
    title: "Confidence Thresholds (startup)",
    icon: Target,
    fields: [
      { kind: "startup", label: "Autonomous Action Threshold", type: "text", hint: "Set via CONFIDENCE_THRESHOLD_AUTONOMOUS at boot" },
      { kind: "startup", label: "Human Review Threshold", type: "text", hint: "Set via CONFIDENCE_THRESHOLD_HUMAN_REVIEW at boot" },
      { kind: "startup", label: "Loop-Back Threshold (startup)", type: "text", hint: "Set via CONFIDENCE_LOOP_BACK at boot" },
    ],
  },
  {
    id: "sandbox-startup",
    title: "Sandbox (startup)",
    icon: FlaskConical,
    fields: [
      { kind: "startup", label: "Sandbox Namespace Prefix", type: "text" },
      { kind: "startup", label: "Sandbox Probe Paths", type: "csv" },
      { kind: "startup", label: "Enable Traffic Mirroring", type: "text" },
    ],
  },
  {
    id: "automerge-startup",
    title: "Auto-Merge (startup)",
    icon: GitBranch,
    fields: [
      { kind: "startup", label: "Auto-Merge Enabled (startup)", type: "text" },
      { kind: "startup", label: "Max Per Hour", type: "text" },
      { kind: "startup", label: "Cooldown (min)", type: "text" },
      { kind: "startup", label: "Min Confidence", type: "text" },
      { kind: "startup", label: "Require CI", type: "text" },
      { kind: "startup", label: "CI Timeout (sec)", type: "text" },
      { kind: "startup", label: "Auto-Merge Namespaces", type: "csv" },
    ],
  },
  {
    id: "auto-remediation",
    title: "Auto-Remediation (startup)",
    icon: Wrench,
    fields: [
      { kind: "startup", label: "Auto-Remediation Enabled", type: "text" },
      { kind: "startup", label: "Scale-Fix Replicas", type: "text" },
      { kind: "startup", label: "Panic CPU Threshold (%)", type: "text" },
      { kind: "startup", label: "Panic Connections Threshold", type: "text" },
    ],
  },
  {
    id: "github",
    title: "GitHub (startup)",
    icon: GitBranch,
    fields: [
      { kind: "startup", label: "GitHub Token", type: "password" },
      { kind: "startup", label: "GitHub API URL", type: "url" },
      { kind: "startup", label: "Git Host", type: "text" },
      { kind: "startup", label: "Default Branch", type: "text" },
      { kind: "startup", label: "GitHub Webhook Secret", type: "password" },
      { kind: "startup", label: "Allowed Repos", type: "csv" },
    ],
  },
  {
    id: "slack",
    title: "Slack (startup)",
    icon: MessageSquare,
    fields: [
      { kind: "startup", label: "Slack Webhook URL", type: "url" },
      { kind: "startup", label: "Slack Bot Token", type: "password" },
      { kind: "startup", label: "Slack Bot User ID", type: "text" },
      { kind: "startup", label: "Slack Signing Secret", type: "password" },
    ],
  },
  {
    id: "kaniko",
    title: "Kaniko (startup)",
    icon: Container,
    fields: [
      { kind: "startup", label: "Registry", type: "text" },
      { kind: "startup", label: "Executor Image", type: "text" },
      { kind: "startup", label: "Init Image", type: "text" },
    ],
  },
  {
    id: "dockerhub",
    title: "Docker Hub (startup)",
    icon: HardDrive,
    fields: [
      { kind: "startup", label: "Username", type: "text" },
      { kind: "startup", label: "Token", type: "password" },
    ],
  },
  {
    id: "alertmanager",
    title: "Alertmanager (startup)",
    icon: Bell,
    fields: [
      { kind: "startup", label: "Webhook Secret", type: "password" },
    ],
  },
  {
    id: "storage",
    title: "Storage (startup)",
    icon: Database,
    fields: [
      { kind: "startup", label: "SQLite Path", type: "text" },
    ],
  },
  {
    id: "api",
    title: "API (startup)",
    icon: Server,
    fields: [
      { kind: "startup", label: "Host", type: "text" },
      { kind: "startup", label: "Admin Token", type: "password", hint: "SENTINEL_API_TOKEN — required for /admin/* and POST /settings" },
    ],
  },
  {
    id: "feedback",
    title: "Feedback Loop (startup)",
    icon: MessageCircle,
    fields: [
      { kind: "startup", label: "Outcome Watch Duration (min)", type: "text" },
      { kind: "startup", label: "Outcome Check Interval (sec)", type: "text" },
      { kind: "startup", label: "LLM Prior Weight", type: "text" },
    ],
  },
];

// ─── Field renderer ─────────────────────────────────────────────────────────

function FieldRenderer({
  field,
  cardId,
  value,
  onChange,
}: {
  field: Field;
  cardId: string;
  value: string | number | boolean | undefined;
  onChange: (v: string | number | boolean) => void;
}) {
  const id = `${cardId}-${field.label.replace(/\s+/g, "-").toLowerCase()}`;
  const isStartup = field.kind === "startup";

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <Label htmlFor={id} className="text-xs text-zinc-300">
          {field.label}
        </Label>
        <div className="flex items-center gap-1">
          {field.kind === "live" && (
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[9px] text-emerald-400">
              <Zap className="h-2.5 w-2.5" /> live
            </Badge>
          )}
          {isStartup && (
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[9px] text-amber-400">
              <RotateCcw className="h-2.5 w-2.5" /> startup
            </Badge>
          )}
        </div>
      </div>

      {isStartup ? (
        // Startup fields are read-only
        <Input
          id={id}
          type={field.type === "password" ? "password" : "text"}
          value={typeof value === "string" ? value : ""}
          readOnly
          placeholder="— set at boot —"
          className="h-8 bg-zinc-950 font-mono text-xs text-zinc-500"
        />
      ) : field.type === "text" || field.type === "password" || field.type === "url" || field.type === "number" ? (
        <Input
          id={id}
          type={field.type === "password" ? "password" : field.type === "url" ? "url" : field.type === "number" ? "number" : "text"}
          value={typeof value === "string" || typeof value === "number" ? String(value) : ""}
          onChange={(e) =>
            onChange(field.type === "number" ? Number(e.target.value) : e.target.value)
          }
          className="h-8 bg-zinc-950 font-mono text-xs"
        />
      ) : field.type === "select" ? (
        <Select value={String(value ?? "")} onValueChange={onChange}>
          <SelectTrigger id={id} size="sm" className="h-8 w-full border-zinc-800 bg-zinc-950 font-mono text-xs">
            <SelectValue placeholder="—" />
          </SelectTrigger>
          <SelectContent>
            {field.options?.map((o) => (
              <SelectItem key={o} value={o} className="font-mono text-xs">
                {o}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : field.type === "toggle" ? (
        <div className="flex items-center gap-2 pt-1">
          <Switch
            id={id}
            checked={Boolean(value)}
            onCheckedChange={onChange}
          />
          <span className="font-mono text-[10px] text-zinc-500">
            {value ? "enabled" : "disabled"}
          </span>
        </div>
      ) : null}

      {field.hint && <p className="text-[10px] text-zinc-600">{field.hint}</p>}
    </div>
  );
}

// ─── Card ───────────────────────────────────────────────────────────────────

function SettingsCard({
  card,
  values,
  onChange,
}: {
  card: SettingsCardDef;
  values: RuntimeConfig;
  onChange: (key: keyof RuntimeConfig, v: string | number | boolean) => void;
}) {
  const Icon = card.icon;
  const liveCount = card.fields.filter((f) => f.kind === "live").length;
  const startupCount = card.fields.filter((f) => f.kind === "startup").length;

  return (
    <Card className="gap-0 border-zinc-800 bg-zinc-900/50 py-0">
      <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
        <div className="flex items-center gap-2">
          <Icon className="h-4 w-4 text-cyan-400" />
          <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-200">{card.title}</h3>
        </div>
        <div className="flex items-center gap-1">
          {liveCount > 0 && (
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[9px] text-emerald-400">
              {liveCount} live
            </Badge>
          )}
          {startupCount > 0 && (
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[9px] text-amber-400">
              {startupCount} startup
            </Badge>
          )}
        </div>
      </div>
      <div className="space-y-3 px-4 py-4">
        {card.fields.map((field) => {
          const value =
            field.kind === "live"
              ? values[field.key]
              : undefined; // startup fields have no value from runtime config
          return (
            <FieldRenderer
              key={field.label}
              field={field}
              cardId={card.id}
              value={value}
              onChange={(v) => field.kind === "live" && onChange(field.key, v)}
            />
          );
        })}
      </div>
    </Card>
  );
}

// ─── Main section ───────────────────────────────────────────────────────────

export function SettingsSection() {
  const runtimeConfig = useSentinelStore((s) => s.runtimeConfig);
  const refreshSettings = useSentinelStore((s) => s.refreshSettings);
  const { toast } = useToast();

  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState<RuntimeConfig>({});
  const [apiToken, setApiToken] = useState<string>("");
  const [saving, setSaving] = useState(false);

  // Load runtime config on mount
  useEffect(() => {
    refreshSettings();
  }, [refreshSettings]);

  // When runtimeConfig loads, seed the draft
  useEffect(() => {
    if (runtimeConfig) {
      setDraft({ ...runtimeConfig });
    }
  }, [runtimeConfig]);

  // Load API token from localStorage (optional convenience)
  useEffect(() => {
    const t = typeof window !== "undefined" ? window.localStorage.getItem("sentinel_api_token") : null;
    if (t) setApiToken(t);
  }, []);

  const onApiTokenChange = (v: string) => {
    setApiToken(v);
    if (typeof window !== "undefined") {
      if (v) window.localStorage.setItem("sentinel_api_token", v);
      else window.localStorage.removeItem("sentinel_api_token");
    }
  };

  const filtered = useMemo(
    () => cards.filter((c) => c.title.toLowerCase().includes(search.toLowerCase())),
    [search],
  );

  const totalLive = cards.reduce((acc, c) => acc + c.fields.filter((f) => f.kind === "live").length, 0);
  const totalRestart = cards.reduce((acc, c) => acc + c.fields.filter((f) => f.kind === "startup").length, 0);

  const dirty = useMemo(() => {
    if (!runtimeConfig) return false;
    for (const k of Object.keys(draft) as Array<keyof RuntimeConfig>) {
      if (draft[k] !== runtimeConfig[k]) return true;
    }
    return false;
  }, [draft, runtimeConfig]);

  const onChange = (key: keyof RuntimeConfig, v: string | number | boolean) => {
    setDraft((d) => ({ ...d, [key]: v }));
  };

  const onSave = async () => {
    if (!runtimeConfig) return;
    setSaving(true);
    try {
      // Only send changed fields
      const diff: Partial<RuntimeConfig> = {};
      for (const k of Object.keys(draft) as Array<keyof RuntimeConfig>) {
        if (draft[k] !== runtimeConfig[k]) {
          // @ts-expect-error — partial assignment
          diff[k] = draft[k];
        }
      }
      if (Object.keys(diff).length === 0) {
        toast({ title: "No changes to save" });
        return;
      }
      const updated = await updateRuntimeConfig(diff);
      useSentinelStore.setState({ runtimeConfig: updated });
      setDraft({ ...updated });
      toast({
        title: "Settings saved",
        description: `${Object.keys(diff).length} field(s) updated via PUT /settings`,
      });
    } catch (err) {
      toast({
        title: "Save failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "destructive",
      });
    } finally {
      setSaving(false);
    }
  };

  const onReset = () => {
    if (runtimeConfig) setDraft({ ...runtimeConfig });
  };

  if (!runtimeConfig) {
    return (
      <div className="space-y-3 p-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="h-8 w-64 animate-pulse rounded bg-zinc-800/60" />
          <div className="flex gap-2">
            <div className="h-8 w-24 animate-pulse rounded bg-zinc-800/60" />
            <div className="h-8 w-24 animate-pulse rounded bg-zinc-800/60" />
          </div>
        </div>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="h-48 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-3 p-6">
      {/* Header strip */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Input
            placeholder="Filter settings..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 w-[220px] bg-zinc-900 font-mono text-xs"
          />
          <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
            {filtered.length} / {cards.length} cards
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[10px] text-emerald-400">
            <Zap className="h-3 w-3" /> {totalLive} hot-reload
          </Badge>
          <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[10px] text-amber-400">
            <RotateCcw className="h-3 w-3" /> {totalRestart} startup-only
          </Badge>
        </div>
      </div>

      {/* API token input (for /settings POST auth) */}
      <Card className="gap-0 border-amber-500/30 bg-amber-500/5 py-0">
        <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
          <div className="flex items-center gap-2">
            <Lock className="h-4 w-4 text-amber-400" />
            <div>
              <h3 className="text-xs font-semibold uppercase tracking-wider text-zinc-200">
                API Token (for saving)
              </h3>
              <p className="mt-0.5 text-[11px] text-zinc-500">
                Required when SENTINEL_API_TOKEN is set on the server. Sent as{" "}
                <Mono className="text-zinc-400">Authorization: Bearer …</Mono> with PUT /settings.
              </p>
            </div>
          </div>
          <Input
            type="password"
            placeholder="stl_admin_…"
            value={apiToken}
            onChange={(e) => onApiTokenChange(e.target.value)}
            className="h-8 w-[280px] bg-zinc-950 font-mono text-xs"
          />
        </div>
      </Card>

      {/* Save / reset bar */}
      <div className="flex items-center justify-between rounded-lg border border-zinc-800 bg-zinc-900/50 px-4 py-2">
        <div className="flex items-center gap-2 text-[11px] text-zinc-500">
          {dirty ? (
            <>
              <AlertCircle className="h-3 w-3 text-amber-400" />
              <span>You have unsaved changes.</span>
            </>
          ) : (
            <>
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
              <span>All changes saved.</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="h-8 border-zinc-700 bg-zinc-900 text-[11px] text-zinc-400 hover:text-zinc-200"
            onClick={onReset}
            disabled={!dirty || saving}
          >
            Reset
          </Button>
          <Button
            size="sm"
            className="h-8 bg-cyan-500 text-[11px] text-zinc-950 hover:bg-cyan-400"
            onClick={onSave}
            disabled={!dirty || saving}
          >
            {saving ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <Save className="h-3 w-3" />
            )}
            Save changes
          </Button>
        </div>
      </div>

      {/* Cards grid */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {filtered.map((card) => (
          <SettingsCard
            key={card.id}
            card={card}
            values={draft}
            onChange={onChange}
          />
        ))}
      </div>

      {/* Helper note */}
      <div className="flex items-start gap-3 rounded-lg border border-zinc-800 bg-zinc-900/50 px-4 py-3 text-[11px] text-zinc-500">
        <RotateCcw className="mt-0.5 h-4 w-4 shrink-0 text-amber-400" />
        <div>
          <p>
            <span className="font-semibold text-zinc-300">Live fields</span> are saved via{" "}
            <Mono className="text-cyan-400">PUT /settings</Mono> and apply immediately (no restart needed).
          </p>
          <p className="mt-1">
            <span className="font-semibold text-zinc-300">Startup fields</span> are read-only here — they're
            set via environment variables at boot time. Edit your deployment's env vars or{" "}
            <Mono className="text-zinc-400">values.yaml</Mono> to change them.
          </p>
        </div>
      </div>
    </div>
  );
}
