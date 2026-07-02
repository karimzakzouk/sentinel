"use client";

import { useState } from "react";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Slider } from "@/components/ui/slider";
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
import { Brain, Clock, Target, FlaskConical, Merge, Eye, Boxes, Activity, GitBranch, MessageSquare, Container, HardDrive, Bell, Database, Server, Wrench, MessageCircle, Zap, RotateCcw } from "lucide-react";
import { cn } from "@/lib/utils";

interface FieldBase {
  label: string;
  live?: boolean;
  restart?: boolean;
  hint?: string;
}

interface TextField extends FieldBase {
  type: "text" | "password" | "url" | "csv";
  defaultValue: string;
  placeholder?: string;
}

interface TextareaField extends FieldBase {
  type: "textarea";
  defaultValue: string;
}

interface SelectField extends FieldBase {
  type: "select";
  defaultValue: string;
  options: string[];
}

interface SliderField extends FieldBase {
  type: "slider";
  defaultValue: number;
  min: number;
  max: number;
  step?: number;
  unit?: string;
}

interface ToggleField extends FieldBase {
  type: "toggle";
  defaultValue: boolean;
}

interface MultiSelectField extends FieldBase {
  type: "multiselect";
  defaultValue: string[];
  options: string[];
}

type Field = TextField | TextareaField | SelectField | SliderField | ToggleField | MultiSelectField;

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
      { type: "select", label: "Provider", defaultValue: "openrouter", options: ["openrouter", "openai", "anthropic", "groq", "nemotron"], live: true },
      { type: "url", label: "Base URL", defaultValue: "https://openrouter.ai/api/v1", live: true, hint: "OpenAI-compatible endpoint" },
      { type: "password", label: "API Key", defaultValue: "sk-or-v1-9f2a••••••••••••••••", live: true },
      { type: "text", label: "Fast Model", defaultValue: "anthropic/claude-haiku-3.5", live: true },
      { type: "text", label: "Frontier Model", defaultValue: "anthropic/claude-sonnet-4.5", live: true },
      { type: "text", label: "Embedding Model", defaultValue: "text-embedding-3-small", restart: true },
    ],
  },
  {
    id: "timeouts",
    title: "Timeouts",
    icon: Clock,
    fields: [
      { type: "slider", label: "Investigator", defaultValue: 180, min: 30, max: 600, step: 15, unit: "s", live: true },
      { type: "slider", label: "Evidence Collector", defaultValue: 120, min: 30, max: 600, step: 15, unit: "s", live: true },
      { type: "slider", label: "Analyst", defaultValue: 240, min: 60, max: 900, step: 15, unit: "s", live: true },
      { type: "slider", label: "Fix Proposer", defaultValue: 300, min: 60, max: 1200, step: 30, unit: "s", live: true },
      { type: "slider", label: "Total Investigation", defaultValue: 900, min: 300, max: 3600, step: 60, unit: "s", live: true },
    ],
  },
  {
    id: "confidence",
    title: "Confidence Thresholds",
    icon: Target,
    fields: [
      { type: "slider", label: "Autonomous Action", defaultValue: 85, min: 50, max: 99, step: 1, unit: "%", live: true },
      { type: "slider", label: "Human Review", defaultValue: 60, min: 30, max: 90, step: 1, unit: "%", live: true },
      { type: "slider", label: "Loop Back Threshold", defaultValue: 40, min: 10, max: 80, step: 1, unit: "%", live: true },
      { type: "slider", label: "Prior Weight", defaultValue: 30, min: 0, max: 100, step: 5, unit: "%", live: true },
    ],
  },
  {
    id: "sandbox",
    title: "Sandbox",
    icon: FlaskConical,
    fields: [
      { type: "toggle", label: "Auto-Run on Fix", defaultValue: true, live: true },
      { type: "toggle", label: "Require L5 Pass", defaultValue: true, live: true },
      { type: "toggle", label: "Parallel Levels", defaultValue: false, restart: true },
      { type: "slider", label: "Sustained Window", defaultValue: 5, min: 1, max: 30, step: 1, unit: "m", live: true },
      { type: "csv", label: "Allowed Repos", defaultValue: "acme/checkout-api,acme/auth-service,acme/search-indexer", live: true },
    ],
  },
  {
    id: "automerge",
    title: "Auto-Merge",
    icon: Merge,
    fields: [
      { type: "toggle", label: "Enable Auto-Merge", defaultValue: true, live: true },
      { type: "slider", label: "Max Per Hour", defaultValue: 3, min: 1, max: 20, step: 1, unit: "/hr", live: true },
      { type: "slider", label: "Cooldown Duration", defaultValue: 30, min: 5, max: 240, step: 5, unit: "m", live: true },
      { type: "slider", label: "Diff Size Limit", defaultValue: 500, min: 50, max: 2000, step: 50, unit: "lines", live: true },
      { type: "slider", label: "Confidence Required", defaultValue: 85, min: 50, max: 99, step: 1, unit: "%", live: true },
      { type: "toggle", label: "Require Code Review", defaultValue: false, live: true },
      { type: "multiselect", label: "Required Gates", defaultValue: ["ci-green", "sandbox-l5", "confidence-threshold"], options: ["ci-green", "code-review", "sandbox-l5", "confidence-threshold", "diff-size", "no-new-deps", "static-analysis", "license-scan", "rate-limit", "cooldown-clear", "branch-protection"], live: true },
    ],
  },
  {
    id: "detection",
    title: "Detection",
    icon: Eye,
    fields: [
      { type: "slider", label: "Error Rate Threshold", defaultValue: 1, min: 0, max: 10, step: 0.1, unit: "%", live: true },
      { type: "slider", label: "Min Errors", defaultValue: 5, min: 1, max: 100, step: 1, live: true },
      { type: "slider", label: "Window Size", defaultValue: 60, min: 10, max: 600, step: 10, unit: "s", live: true },
      { type: "slider", label: "Cooldown", defaultValue: 300, min: 60, max: 3600, step: 60, unit: "s", live: true },
    ],
  },
  {
    id: "k8s",
    title: "Kubernetes",
    icon: Boxes,
    fields: [
      { type: "csv", label: "Namespaces", defaultValue: "payments-prod,identity-prod,search-prod,notify-prod,billing-prod,media-prod", restart: true },
      { type: "csv", label: "Services", defaultValue: "checkout-api,auth-service,search-indexer,notifications-worker,billing-cron,image-resizer", restart: true },
      { type: "csv", label: "Excluded Pods", defaultValue: "kafka-*,zookeeper-*,prometheus-*", restart: true },
      { type: "csv", label: "Labels Required", defaultValue: "app.kubernetes.io/name,app.kubernetes.io/team", restart: true },
    ],
  },
  {
    id: "observability",
    title: "Observability",
    icon: Activity,
    fields: [
      { type: "url", label: "Loki URL", defaultValue: "http://loki.observability.svc:3100", restart: true },
      { type: "url", label: "Tempo URL", defaultValue: "http://tempo.observability.svc:3200", restart: true },
      { type: "url", label: "Prometheus URL", defaultValue: "http://prometheus.observability.svc:9090", restart: true },
      { type: "select", label: "Log Level", defaultValue: "info", options: ["debug", "info", "warn", "error"], live: true },
    ],
  },
  {
    id: "github",
    title: "GitHub",
    icon: GitBranch,
    fields: [
      { type: "password", label: "Token", defaultValue: "ghp_9f2a••••••••••••••••••••••", restart: true },
      { type: "url", label: "API URL", defaultValue: "https://api.github.com", restart: true },
      { type: "text", label: "Git Host", defaultValue: "github.com", restart: true },
      { type: "text", label: "Default Branch", defaultValue: "main", live: true },
      { type: "password", label: "Webhook Secret", defaultValue: "whsec_••••••••••••", restart: true },
      { type: "csv", label: "Allowed Repos", defaultValue: "acme/checkout-api,acme/auth-service,acme/search-indexer,acme/notifications-worker,acme/billing-cron,acme/image-resizer", restart: true },
    ],
  },
  {
    id: "github-app",
    title: "GitHub App",
    icon: GitBranch,
    fields: [
      { type: "text", label: "App ID", defaultValue: "284719", restart: true },
      { type: "text", label: "Installation ID", defaultValue: "39281047", restart: true },
      { type: "textarea", label: "Private Key (PEM)", defaultValue: "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA9f2a8B7c...\n[truncated for display]\n-----END RSA PRIVATE KEY-----", restart: true },
    ],
  },
  {
    id: "slack",
    title: "Slack",
    icon: MessageSquare,
    fields: [
      { type: "url", label: "Webhook URL", defaultValue: "https://hooks.slack.com/services/T0/B0/••••", restart: true },
      { type: "password", label: "Bot Token", defaultValue: "xoxb-9f2a••••••••••••", restart: true },
      { type: "text", label: "Bot User ID", defaultValue: "U047KB9F2A", restart: true },
      { type: "password", label: "Signing Secret", defaultValue: "8f7e6d5c••••••••••••", restart: true },
    ],
  },
  {
    id: "kaniko",
    title: "Kaniko",
    icon: Container,
    fields: [
      { type: "text", label: "Registry", defaultValue: "registry.acme.io", restart: true },
      { type: "text", label: "Executor Image", defaultValue: "gcr.io/kaniko-project/executor:v1.23.2", restart: true },
      { type: "text", label: "Init Image", defaultValue: "gcr.io/kaniko-project/warmer:v1.23.2", restart: true },
    ],
  },
  {
    id: "dockerhub",
    title: "Docker Hub",
    icon: HardDrive,
    fields: [
      { type: "text", label: "Username", defaultValue: "acme-bot", restart: true },
      { type: "password", label: "Token", defaultValue: "dckr_pat_9f2a••••••••••", restart: true },
    ],
  },
  {
    id: "alertmanager",
    title: "Alertmanager",
    icon: Bell,
    fields: [
      { type: "password", label: "Webhook Secret", defaultValue: "am_9f2a••••••••••", restart: true },
    ],
  },
  {
    id: "storage",
    title: "Storage",
    icon: Database,
    fields: [
      { type: "text", label: "SQLite Path", defaultValue: "/var/lib/sentinel/sentinel.db", restart: true },
    ],
  },
  {
    id: "api",
    title: "API",
    icon: Server,
    fields: [
      { type: "text", label: "Host", defaultValue: "0.0.0.0", restart: true },
      { type: "text", label: "Port", defaultValue: "8080", restart: true },
      { type: "password", label: "Admin Token", defaultValue: "stl_admin_9f2a••••••••••", restart: true },
    ],
  },
  {
    id: "auto-remediation",
    title: "Auto-Remediation",
    icon: Wrench,
    fields: [
      { type: "toggle", label: "Enable Auto-Remediation", defaultValue: true, live: true },
      { type: "text", label: "Max Concurrent", defaultValue: "3", live: true },
      { type: "text", label: "Rollback Window", defaultValue: "300s", live: true },
      { type: "text", label: "Notification Channel", defaultValue: "#sre-oncall", live: true },
    ],
  },
  {
    id: "feedback",
    title: "Feedback Loop",
    icon: MessageCircle,
    fields: [
      { type: "url", label: "Feedback Webhook", defaultValue: "https://feedback.acme.io/sentinel", live: true },
      { type: "text", label: "Min Samples", defaultValue: "10", live: true },
    ],
  },
];

function FieldRenderer({ field, cardId }: { field: Field; cardId: string }) {
  const id = `${cardId}-${field.label.replace(/\s+/g, "-").toLowerCase()}`;
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <Label htmlFor={id} className="text-xs text-zinc-300">{field.label}</Label>
        <div className="flex items-center gap-1">
          {field.live && (
            <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 font-mono text-[9px] text-emerald-400">
              <Zap className="h-2.5 w-2.5" /> live
            </Badge>
          )}
          {field.restart && (
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[9px] text-amber-400">
              <RotateCcw className="h-2.5 w-2.5" /> restart
            </Badge>
          )}
        </div>
      </div>
      {field.type === "text" || field.type === "password" || field.type === "url" || field.type === "csv" ? (
        <Input
          id={id}
          type={field.type === "password" ? "password" : field.type === "url" ? "url" : "text"}
          defaultValue={field.defaultValue}
          placeholder={field.placeholder}
          className="h-8 bg-zinc-950 font-mono text-xs"
        />
      ) : field.type === "textarea" ? (
        <Textarea
          id={id}
          defaultValue={field.defaultValue}
          className="min-h-[80px] bg-zinc-950 font-mono text-xs"
        />
      ) : field.type === "select" ? (
        <Select defaultValue={field.defaultValue}>
          <SelectTrigger id={id} size="sm" className="h-8 w-full border-zinc-800 bg-zinc-950 font-mono text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {field.options.map((o) => (
              <SelectItem key={o} value={o} className="font-mono text-xs">{o}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : field.type === "slider" ? (
        <div className="space-y-1">
          <Slider
            id={id}
            defaultValue={[field.defaultValue]}
            min={field.min}
            max={field.max}
            step={field.step ?? 1}
          />
          <div className="flex items-center justify-between font-mono text-[10px] text-zinc-500">
            <span>{field.min}{field.unit}</span>
            <span className="text-cyan-400">{field.defaultValue}{field.unit}</span>
            <span>{field.max}{field.unit}</span>
          </div>
        </div>
      ) : field.type === "toggle" ? (
        <div className="flex items-center gap-2 pt-1">
          <Switch id={id} defaultChecked={field.defaultValue} />
          <span className="font-mono text-[10px] text-zinc-500">{field.defaultValue ? "enabled" : "disabled"}</span>
        </div>
      ) : field.type === "multiselect" ? (
        <div className="flex flex-wrap gap-1 rounded-md border border-zinc-800 bg-zinc-950 p-2">
          {field.options.map((opt) => {
            const selected = field.defaultValue.includes(opt);
            return (
              <button
                key={opt}
                className={cn(
                  "rounded border px-1.5 py-0.5 font-mono text-[10px] transition-colors",
                  selected
                    ? "border-cyan-500/40 bg-cyan-500/10 text-cyan-400"
                    : "border-zinc-700 bg-zinc-900 text-zinc-500 hover:text-zinc-300"
                )}
              >
                {opt}
              </button>
            );
          })}
        </div>
      ) : null}
      {field.hint && <p className="text-[10px] text-zinc-600">{field.hint}</p>}
    </div>
  );
}

function SettingsCard({ card }: { card: SettingsCardDef }) {
  const { toast } = useToast();
  const Icon = card.icon;
  const liveCount = card.fields.filter((f) => f.live).length;
  const restartCount = card.fields.filter((f) => f.restart).length;

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
          {restartCount > 0 && (
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[9px] text-amber-400">
              {restartCount} restart
            </Badge>
          )}
        </div>
      </div>
      <div className="space-y-3 px-4 py-4">
        {card.fields.map((field) => (
          <FieldRenderer key={field.label} field={field} cardId={card.id} />
        ))}
      </div>
      <div className="flex items-center justify-end gap-2 border-t border-zinc-800 px-4 py-2.5">
        <Button variant="outline" size="sm" className="h-7 border-zinc-700 bg-zinc-900 text-[11px] text-zinc-400 hover:text-zinc-200">
          Reset
        </Button>
        <Button
          size="sm"
          className="h-7 bg-cyan-500 text-[11px] text-zinc-950 hover:bg-cyan-400"
          onClick={() =>
            toast({
              title: `${card.title} saved`,
              description: `${liveCount} hot-reloaded · ${restartCount} require restart`,
            })
          }
        >
          Save
        </Button>
      </div>
    </Card>
  );
}

export function SettingsSection() {
  const [search, setSearch] = useState("");
  const filtered = cards.filter((c) => c.title.toLowerCase().includes(search.toLowerCase()));
  const totalLive = cards.reduce((acc, c) => acc + c.fields.filter((f) => f.live).length, 0);
  const totalRestart = cards.reduce((acc, c) => acc + c.fields.filter((f) => f.restart).length, 0);

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
            <RotateCcw className="h-3 w-3" /> {totalRestart} restart-required
          </Badge>
        </div>
      </div>

      {/* Cards grid */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {filtered.map((card) => (
          <SettingsCard key={card.id} card={card} />
        ))}
      </div>
    </div>
  );
}
