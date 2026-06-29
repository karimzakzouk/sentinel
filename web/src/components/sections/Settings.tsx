"use client";

import { useEffect, useState } from "react";
import {
  Cpu,
  Clock,
  Gauge,
  FlaskConical,
  GitMerge,
  Radar,
  Boxes,
  Activity,
  GitGraph,
  MessageSquare,
  Container,
  Eye,
  EyeOff,
  Save,
  RotateCcw,
  Loader2,
  Power,
  AlertCircle,
} from "lucide-react";
import {
  getRuntimeConfig,
  getStartupConfig,
  updateRuntimeConfig,
} from "@/lib/api";
import type { RuntimeConfig, StartupConfig } from "@/lib/types";
import { useToast } from "@/hooks/use-toast";
import { useFetch } from "@/hooks/use-fetch";
import { cn, parseCsv, joinCsv } from "@/lib/utils";
import {
  SectionHeader,
  Panel,
  Skeleton,
  ErrorState,
} from "@/components/ui-helpers";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

// ─── Field model ─────────────────────────────────────────────────────────────

type FieldType =
  | "text"
  | "password"
  | "number"
  | "slider"
  | "toggle"
  | "select"
  | "csv"
  | "textarea";

interface FieldDef {
  key: string;
  label: string;
  type: FieldType;
  runtime: boolean; // false ⇒ startup-only (read-only + "requires restart")
  placeholder?: string;
  options?: string[];
  min?: number;
  max?: number;
  step?: number;
  hint?: string;
  rows?: number;
}

interface CardDef {
  id: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  fields: FieldDef[];
}

const CARDS: CardDef[] = [
  {
    id: "llm",
    title: "LLM Configuration",
    description: "Provider, models & retry behaviour",
    icon: Cpu,
    fields: [
      { key: "llm_provider", label: "Provider", type: "text", runtime: true, placeholder: "openai" },
      { key: "llm_base_url", label: "Base URL", type: "text", runtime: true, placeholder: "https://api.openai.com/v1" },
      { key: "llm_api_key", label: "API Key", type: "password", runtime: true, placeholder: "sk-…" },
      { key: "llm_fast_model", label: "Fast Model", type: "text", runtime: true, placeholder: "gpt-4o-mini" },
      { key: "llm_frontier_model", label: "Frontier Model", type: "text", runtime: true, placeholder: "gpt-4o" },
      { key: "llm_embedding_model", label: "Embedding Model", type: "text", runtime: true, placeholder: "text-embedding-3-small" },
      { key: "llm_max_retries", label: "Max Retries", type: "number", runtime: true, min: 0, max: 20 },
      { key: "llm_retry_base_delay_ms", label: "Retry Base Delay (ms)", type: "number", runtime: true, min: 0, max: 60000 },
    ],
  },
  {
    id: "timeouts",
    title: "Agent Timeouts",
    description: "Per-agent & pipeline deadlines (seconds)",
    icon: Clock,
    fields: [
      { key: "investigator_timeout_sec", label: "Investigator", type: "number", runtime: true, min: 1, max: 3600, hint: "seconds" },
      { key: "evidence_gatherer_timeout_sec", label: "Evidence Gatherer", type: "number", runtime: true, min: 1, max: 3600, hint: "seconds" },
      { key: "analyst_timeout_sec", label: "Analyst", type: "number", runtime: true, min: 1, max: 3600, hint: "seconds" },
      { key: "fix_proposer_timeout_sec", label: "Fix Proposer", type: "number", runtime: true, min: 1, max: 3600, hint: "seconds" },
      { key: "investigation_timeout", label: "Investigation (total)", type: "number", runtime: false, min: 1, max: 7200, hint: "seconds · requires restart" },
    ],
  },
  {
    id: "confidence",
    title: "Confidence Thresholds",
    description: "Autonomous action & review boundaries",
    icon: Gauge,
    fields: [
      { key: "confidence_threshold_autonomous", label: "Autonomous Action", type: "slider", runtime: false, min: 0, max: 1, step: 0.05, hint: "above this confidence, Sentinel auto-merges" },
      { key: "confidence_threshold_human_review", label: "Human Review", type: "slider", runtime: false, min: 0, max: 1, step: 0.05, hint: "below this, require human approval" },
      { key: "confidence_loop_back_threshold", label: "Loop Back", type: "slider", runtime: true, min: 0, max: 1, step: 0.05, hint: "re-investigate if below this" },
    ],
  },
  {
    id: "sandbox",
    title: "Sandbox",
    description: "Verification gates & probe paths",
    icon: FlaskConical,
    fields: [
      { key: "sandbox_enabled", label: "Enabled", type: "toggle", runtime: true },
      { key: "sandbox_required", label: "Required", type: "toggle", runtime: true },
      { key: "sandbox_max_retries", label: "Max Retries", type: "number", runtime: true, min: 0, max: 10 },
      { key: "skip_sandbox_non_prod", label: "Skip in Non-Prod", type: "toggle", runtime: true },
      { key: "sandbox_probe_paths", label: "Probe Paths", type: "csv", runtime: false, placeholder: "/health, /ready, /pay", hint: "comma-separated · requires restart" },
    ],
  },
  {
    id: "automerge",
    title: "Auto-merge",
    description: "Gated autonomous merge policy",
    icon: GitMerge,
    fields: [
      { key: "auto_merge_enabled", label: "Enabled", type: "toggle", runtime: false },
      { key: "auto_merge_max_per_hour", label: "Max Per Hour", type: "number", runtime: false, min: 0, max: 100 },
      { key: "auto_merge_cooldown_min", label: "Cooldown (min)", type: "number", runtime: false, min: 0, max: 1440 },
      { key: "auto_merge_min_confidence", label: "Min Confidence", type: "slider", runtime: false, min: 0, max: 1, step: 0.05 },
      { key: "auto_merge_require_ci", label: "Require CI", type: "toggle", runtime: false },
      { key: "auto_merge_ci_timeout_sec", label: "CI Timeout (sec)", type: "number", runtime: false, min: 0, max: 7200 },
      { key: "auto_merge_namespaces", label: "Namespaces", type: "csv", runtime: false, placeholder: "prod, prod-us" },
    ],
  },
  {
    id: "detection",
    title: "Detection",
    description: "Anomaly detection cadence & concurrency",
    icon: Radar,
    fields: [
      { key: "detection_cooldown_sec", label: "Cooldown (sec)", type: "number", runtime: false, min: 0, max: 3600 },
      { key: "detection_tail_lines", label: "Tail Lines", type: "number", runtime: false, min: 0, max: 100000 },
      { key: "max_concurrent_investigations", label: "Max Concurrent Investigations", type: "number", runtime: false, min: 1, max: 100 },
      { key: "max_pipeline_iterations", label: "Max Pipeline Iterations", type: "number", runtime: false, min: 1, max: 20 },
    ],
  },
  {
    id: "kubernetes",
    title: "Kubernetes",
    description: "Namespace policy & high-risk services",
    icon: Boxes,
    fields: [
      { key: "production_namespaces", label: "Production Namespaces", type: "csv", runtime: false, placeholder: "prod, prod-us" },
      { key: "write_namespaces", label: "Write Namespaces", type: "csv", runtime: false, placeholder: "prod, staging" },
      { key: "skip_namespaces", label: "Skip Namespaces", type: "csv", runtime: false, placeholder: "kube-system" },
      { key: "high_risk_services", label: "High-Risk Services", type: "csv", runtime: false, placeholder: "payment-api, auth" },
    ],
  },
  {
    id: "observability",
    title: "Observability",
    description: "Prometheus, Loki, Jaeger & log level",
    icon: Activity,
    fields: [
      { key: "prometheus_url", label: "Prometheus URL", type: "text", runtime: true, placeholder: "http://prometheus:9090" },
      { key: "loki_url", label: "Loki URL", type: "text", runtime: true, placeholder: "http://loki:3100" },
      { key: "jaeger_url", label: "Jaeger URL", type: "text", runtime: true, placeholder: "http://jaeger:16686" },
      { key: "log_level", label: "Log Level", type: "select", runtime: true, options: ["DEBUG", "INFO", "WARN", "ERROR"] },
    ],
  },
  {
    id: "github",
    title: "GitHub App",
    description: "App credentials for PR operations",
    icon: GitGraph,
    fields: [
      { key: "github_app_id", label: "App ID", type: "text", runtime: true },
      { key: "github_app_install_id", label: "Install ID", type: "text", runtime: true },
      { key: "github_app_private_key", label: "Private Key (PEM)", type: "textarea", runtime: true, rows: 6, hint: "RSA private key, password-masked" },
    ],
  },
  {
    id: "slack",
    title: "Slack",
    description: "Notifications & bot integration",
    icon: MessageSquare,
    fields: [
      { key: "slack_webhook", label: "Webhook URL", type: "password", runtime: false },
      { key: "slack_bot_token", label: "Bot Token", type: "password", runtime: false, placeholder: "xoxb-…" },
      { key: "slack_bot_user_id", label: "Bot User ID", type: "text", runtime: false },
      { key: "slack_signing_secret", label: "Signing Secret", type: "password", runtime: false },
    ],
  },
  {
    id: "kaniko",
    title: "Kaniko",
    description: "Image builder registry & executor",
    icon: Container,
    fields: [
      { key: "kaniko_registry", label: "Registry", type: "text", runtime: false, placeholder: "gcr.io/my-project" },
      { key: "kaniko_executor_image", label: "Executor Image", type: "text", runtime: false },
      { key: "kaniko_init_image", label: "Init Image", type: "text", runtime: false },
      { key: "docker_hub_username", label: "Docker Hub Username", type: "text", runtime: false },
      { key: "docker_hub_token", label: "Docker Hub Token", type: "password", runtime: false },
    ],
  },
];

type FormState = Record<string, string | number | boolean | undefined>;

export function Settings({ refreshKey }: { refreshKey: number }) {
  const runtimeFetch = useFetch<RuntimeConfig>(
    () => getRuntimeConfig(),
    [refreshKey],
    { intervalMs: 60000 },
  );
  const startupFetch = useFetch<StartupConfig>(
    () => getStartupConfig(),
    [refreshKey],
    { intervalMs: 60000 },
  );

  const [form, setForm] = useState<FormState>({});
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [loaded, setLoaded] = useState(false);
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const { toast } = useToast();

  // Initialise form once both configs arrive.
  useEffect(() => {
    if (runtimeFetch.data && startupFetch.data && !loaded) {
      setForm({ ...runtimeFetch.data, ...startupFetch.data });
      setLoaded(true);
    }
  }, [runtimeFetch.data, startupFetch.data, loaded]);

  // Reload form when explicitly refreshed.
  useEffect(() => {
    if (loaded && runtimeFetch.data && startupFetch.data) {
      setForm({ ...runtimeFetch.data, ...startupFetch.data });
      setDirty(new Set());
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshKey]);

  function setField(key: string, value: string | number | boolean | undefined) {
    setForm((prev) => ({ ...prev, [key]: value }));
    setDirty((prev) => {
      const next = new Set(prev);
      next.add(key);
      return next;
    });
  }

  async function saveCard(card: CardDef) {
    const runtimeFields = card.fields.filter((f) => f.runtime);
    const payload: Record<string, unknown> = {};
    for (const f of runtimeFields) {
      // Only send fields the user actually changed (Partial PUT semantics).
      if (dirty.has(f.key)) {
        payload[f.key] = form[f.key];
      }
    }
    if (Object.keys(payload).length === 0) {
      toast({
        title: "Nothing to save",
        description: "No changes in this section.",
      });
      return;
    }
    setSavingCard(card.id);
    try {
      const updated = await updateRuntimeConfig(payload as Partial<RuntimeConfig>);
      setForm((prev) => ({ ...prev, ...updated }));
      setDirty((prev) => {
        const next = new Set(prev);
        runtimeFields.forEach((f) => next.delete(f.key));
        return next;
      });
      toast({
        title: "Saved",
        description: `${card.title} updated — hot-reloaded.`,
      });
    } catch (err) {
      toast({
        title: "Save failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      });
    } finally {
      setSavingCard(null);
    }
  }

  function resetCard(card: CardDef) {
    if (!runtimeFetch.data || !startupFetch.data) return;
    const merged = { ...runtimeFetch.data, ...startupFetch.data };
    setForm((prev) => {
      const next = { ...prev };
      for (const f of card.fields) {
        next[f.key] = merged[f.key as keyof typeof merged];
      }
      return next;
    });
    setDirty((prev) => {
      const next = new Set(prev);
      card.fields.forEach((f) => next.delete(f.key));
      return next;
    });
  }

  const loading = runtimeFetch.loading || startupFetch.loading;
  const error = runtimeFetch.error || startupFetch.error;

  if (loading && !loaded) {
    return (
      <div className="flex flex-col gap-6">
        <SectionHeader title="Settings" description="Comprehensive runtime & startup configuration" />
        <div className="grid gap-4 lg:grid-cols-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-64 w-full" />
          ))}
        </div>
      </div>
    );
  }

  if (error && !loaded) {
    return (
      <div className="flex flex-col gap-6">
        <SectionHeader title="Settings" description="Comprehensive runtime & startup configuration" />
        <ErrorState
          message={error}
          onRetry={() => {
            runtimeFetch.refetch();
            startupFetch.refetch();
          }}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <SectionHeader
        title="Settings"
        description="Comprehensive runtime & startup configuration"
      >
        <div className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-1.5">
          <span className="size-2 rounded-full bg-cyan-500" />
          <span className="text-xs text-muted-foreground">
            {dirty.size} unsaved {dirty.size === 1 ? "change" : "changes"}
          </span>
        </div>
      </SectionHeader>

      <div className="grid gap-4 lg:grid-cols-2">
        {CARDS.map((card) => (
          <SettingsCard
            key={card.id}
            card={card}
            form={form}
            dirty={dirty}
            saving={savingCard === card.id}
            onField={setField}
            onSave={() => saveCard(card)}
            onReset={() => resetCard(card)}
          />
        ))}
      </div>
    </div>
  );
}

// ─── Card ────────────────────────────────────────────────────────────────────

function SettingsCard({
  card,
  form,
  dirty,
  saving,
  onField,
  onSave,
  onReset,
}: {
  card: CardDef;
  form: FormState;
  dirty: Set<string>;
  saving: boolean;
  onField: (key: string, value: string | number | boolean | undefined) => void;
  onSave: () => void;
  onReset: () => void;
}) {
  const Icon = card.icon;
  const hasRuntime = card.fields.some((f) => f.runtime);
  const hasStartup = card.fields.some((f) => !f.runtime);
  const cardDirty = card.fields.some((f) => dirty.has(f.key));
  const runtimeDirty = card.fields.some(
    (f) => f.runtime && dirty.has(f.key),
  );

  return (
    <Panel
      className="flex flex-col"
      bodyClassName="flex flex-1 flex-col gap-4"
      action={
        <div className="flex items-center gap-2">
          {hasStartup && (
            <span className="inline-flex items-center gap-1 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-amber-400">
              <AlertCircle className="size-2.5" />
              restart
            </span>
          )}
          {hasRuntime && (
            <span className="inline-flex items-center gap-1 rounded border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-emerald-400">
              <Power className="size-2.5" />
              hot
            </span>
          )}
        </div>
      }
      title={
        <span className="flex items-center gap-2">
          <Icon className="size-4 text-cyan-400" />
          {card.title}
        </span>
      }
      description={card.description}
    >
      <div className="grid gap-3">
        {card.fields.map((f) => (
          <FieldRow
            key={f.key}
            field={f}
            value={form[f.key]}
            onChange={(v) => onField(f.key, v)}
          />
        ))}
      </div>

      <div className="mt-auto flex items-center justify-end gap-2 border-t border-zinc-800 pt-3">
        <Button
          variant="ghost"
          size="sm"
          onClick={onReset}
          disabled={!cardDirty || saving}
          className="text-muted-foreground hover:text-zinc-200"
        >
          <RotateCcw className="size-3.5" />
          Reset
        </Button>
        {hasRuntime ? (
          <Button
            size="sm"
            onClick={onSave}
            disabled={!runtimeDirty || saving}
            className="border border-cyan-500/40 bg-cyan-500/10 text-cyan-400 hover:bg-cyan-500/20"
          >
            {saving ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <Save className="size-3.5" />
            )}
            Save
          </Button>
        ) : (
          <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <AlertCircle className="size-3 text-amber-400" />
            All fields require restart
          </span>
        )}
      </div>
    </Panel>
  );
}

// ─── Field row ───────────────────────────────────────────────────────────────

function FieldRow({
  field,
  value,
  onChange,
}: {
  field: FieldDef;
  value: string | number | boolean | undefined;
  onChange: (v: string | number | boolean | undefined) => void;
}) {
  return (
    <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-[200px_1fr] sm:items-center">
      <label
        htmlFor={field.key}
        className="flex items-center gap-1.5 text-xs font-medium text-zinc-300"
      >
        {field.label}
        {!field.runtime && (
          <span
            className="rounded bg-amber-500/10 px-1 py-0.5 text-[9px] uppercase text-amber-400"
            title="Startup-only — requires restart"
          >
            restart
          </span>
        )}
      </label>
      <div className="space-y-1">
        <FieldInput field={field} value={value} onChange={onChange} />
        {field.hint && (
          <p className="text-[10px] text-muted-foreground">{field.hint}</p>
        )}
      </div>
    </div>
  );
}

function FieldInput({
  field,
  value,
  onChange,
}: {
  field: FieldDef;
  value: string | number | boolean | undefined;
  onChange: (v: string | number | boolean | undefined) => void;
}) {
  const disabled = !field.runtime;

  switch (field.type) {
    case "text":
      return (
        <Input
          id={field.key}
          type="text"
          value={(value as string) ?? ""}
          placeholder={field.placeholder}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          className="font-mono text-xs"
        />
      );

    case "password":
      return (
        <SecretInput
          id={field.key}
          value={(value as string) ?? ""}
          placeholder={field.placeholder}
          disabled={disabled}
          onChange={onChange}
        />
      );

    case "number":
      return (
        <Input
          id={field.key}
          type="number"
          value={value != null ? String(value) : ""}
          placeholder={field.placeholder}
          min={field.min}
          max={field.max}
          disabled={disabled}
          onChange={(e) =>
            onChange(e.target.value === "" ? undefined : Number(e.target.value))
          }
          className="font-mono text-xs"
        />
      );

    case "slider":
      return (
        <Slider
          value={typeof value === "number" ? value : 0}
          min={field.min ?? 0}
          max={field.max ?? 1}
          step={field.step ?? 0.05}
          disabled={disabled}
          onChange={onChange}
        />
      );

    case "toggle":
      return (
        <Toggle
          checked={!!value}
          disabled={disabled}
          onChange={onChange}
        />
      );

    case "select":
      return (
        <Select
          id={field.key}
          value={(value as string) ?? ""}
          options={field.options || []}
          disabled={disabled}
          onChange={onChange}
        />
      );

    case "csv":
      return (
        <CsvInput
          id={field.key}
          value={Array.isArray(value) ? (value as string[]) : parseCsv(value as string)}
          placeholder={field.placeholder}
          disabled={disabled}
          onChange={(arr) => onChange(joinCsv(arr))}
        />
      );

    case "textarea":
      return (
        <SecretTextarea
          id={field.key}
          value={(value as string) ?? ""}
          rows={field.rows ?? 4}
          disabled={disabled}
          onChange={onChange}
        />
      );

    default:
      return null;
  }
}

// ─── Input primitives ────────────────────────────────────────────────────────

function SecretInput({
  id,
  value,
  placeholder,
  disabled,
  onChange,
}: {
  id: string;
  value: string;
  placeholder?: string;
  disabled?: boolean;
  onChange: (v: string) => void;
}) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className="relative">
      <Input
        id={id}
        type={revealed ? "text" : "password"}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="pr-9 font-mono text-xs"
      />
      <button
        type="button"
        onClick={() => setRevealed((r) => !r)}
        className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground transition-colors hover:text-zinc-200"
        tabIndex={-1}
      >
        {revealed ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
      </button>
    </div>
  );
}

function SecretTextarea({
  id,
  value,
  rows,
  disabled,
  onChange,
}: {
  id: string;
  value: string;
  rows: number;
  disabled?: boolean;
  onChange: (v: string) => void;
}) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className="space-y-1">
      <div className="relative">
        <textarea
          id={id}
          value={revealed ? value : value ? "••••••••••••••••" : ""}
          rows={rows}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          onFocus={() => setRevealed(true)}
          onBlur={() => setRevealed(false)}
          placeholder="-----BEGIN RSA PRIVATE KEY-----"
          className="w-full resize-y rounded-md border border-zinc-800 bg-zinc-900/60 px-3 py-2 font-mono text-[11px] text-zinc-200 outline-none transition-colors focus:border-cyan-500/50 disabled:cursor-not-allowed disabled:opacity-60 scrollbar-sentinel"
        />
        <button
          type="button"
          onClick={() => setRevealed((r) => !r)}
          className="absolute right-2 top-2 text-muted-foreground transition-colors hover:text-zinc-200"
          tabIndex={-1}
        >
          {revealed ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
        </button>
      </div>
      <p className="text-[10px] text-muted-foreground">
        {revealed ? "Showing value — click eye to mask" : "Masked — focus field or click eye to reveal"}
      </p>
    </div>
  );
}

function Slider({
  value,
  min,
  max,
  step,
  disabled,
  onChange,
}: {
  value: number;
  min: number;
  max: number;
  step: number;
  disabled?: boolean;
  onChange: (v: number) => void;
}) {
  return (
    <div className="flex items-center gap-3">
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(parseFloat(e.target.value))}
        className="slider-cyan flex-1 disabled:opacity-50"
      />
      <span className="w-14 text-right font-mono text-xs tabular-nums text-cyan-400">
        {value.toFixed(2)}
      </span>
    </div>
  );
}

function Toggle({
  checked,
  disabled,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => !disabled && onChange(!checked)}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
        checked
          ? "border-cyan-500/50 bg-cyan-500/30"
          : "border-zinc-700 bg-zinc-800",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      <span
        className={cn(
          "inline-block size-3.5 rounded-full transition-transform",
          checked ? "translate-x-4 bg-cyan-400" : "translate-x-0.5 bg-zinc-400",
        )}
      />
    </button>
  );
}

function Select({
  id,
  value,
  options,
  disabled,
  onChange,
}: {
  id: string;
  value: string;
  options: string[];
  disabled?: boolean;
  onChange: (v: string) => void;
}) {
  return (
    <select
      id={id}
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      className={cn(
        "h-9 w-full rounded-md border border-zinc-800 bg-zinc-900 px-3 text-sm text-zinc-200 outline-none transition-colors focus:border-cyan-500/50",
        disabled && "cursor-not-allowed opacity-60",
      )}
    >
      <option value="">— select —</option>
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}

function CsvInput({
  id,
  value,
  placeholder,
  disabled,
  onChange,
}: {
  id: string;
  value: string[];
  placeholder?: string;
  disabled?: boolean;
  onChange: (arr: string[]) => void;
}) {
  const [text, setText] = useState(joinCsv(value));
  useEffect(() => {
    setText(joinCsv(value));
  }, [value]);
  return (
    <Input
      id={id}
      type="text"
      value={text}
      placeholder={placeholder}
      disabled={disabled}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parseCsv(e.target.value));
      }}
      className="font-mono text-xs"
    />
  );
}
