"use client";

import { useState, useEffect } from "react";
import useSWR from "swr";
import { Save, RefreshCw, Check, AlertCircle, Eye, EyeOff } from "lucide-react";

interface Settings {
  github_token: string;
  github_token_set: boolean;
  llm_provider: string;
  llm_api_key: string;
  llm_api_key_set: boolean;
  llm_fast_model: string;
  llm_frontier_model: string;
  prometheus_url: string;
  loki_url: string;
  jaeger_url: string;
  production_namespaces: string;
  write_namespaces: string;
  confidence_threshold_autonomous: number;
  confidence_threshold_human_review: number;
  panic_cpu_threshold_pct: number;
  panic_connections_threshold: number;
}

const fetcher = (url: string) => fetch(url).then((r) => r.json());

const PROVIDERS = [
  { value: "openai", label: "OpenAI (GPT-4o)" },
  { value: "gemini", label: "Google Gemini" },
  { value: "zai", label: "z.ai GLM" },
  { value: "litellm", label: "Anthropic via LiteLLM" },
  { value: "ollama", label: "Ollama (local)" },
  { value: "vllm", label: "vLLM (self-hosted)" },
  { value: "azure", label: "Azure OpenAI" },
  { value: "custom", label: "Custom" },
];

export default function SettingsPage() {
  const { data, mutate } = useSWR<Settings>("/api/proxy/settings", fetcher);
  const [form, setForm] = useState<Partial<Settings>>({});
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState("");
  const [showGithub, setShowGithub] = useState(false);
  const [showLlm, setShowLlm] = useState(false);

  useEffect(() => {
    if (data) setForm(data);
  }, [data]);

  const update = (field: keyof Settings, value: string | number) => {
    setForm((f) => ({ ...f, [field]: value }));
    setSaved(false);
  };

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      const r = await fetch("/api/proxy/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(form),
      });
      if (!r.ok) throw new Error(await r.text());
      setSaved(true);
      mutate();
      setTimeout(() => setSaved(false), 3000);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  if (!data) {
    return <div className="text-center text-[var(--color-text-dim)] p-8">Loading...</div>;
  }

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Settings</h1>
        <div className="flex items-center gap-3">
          {saved && (
            <span className="text-xs text-[var(--color-accent)] flex items-center gap-1">
              <Check size={12} /> Saved
            </span>
          )}
          {error && (
            <span className="text-xs text-[var(--color-danger)] flex items-center gap-1">
              <AlertCircle size={12} /> {error}
            </span>
          )}
          <button
            onClick={save}
            disabled={saving}
            className="flex items-center gap-2 bg-[var(--color-accent)] text-[var(--color-bg)] px-3 py-1.5 rounded text-sm font-semibold hover:opacity-90 disabled:opacity-50"
          >
            {saving ? <RefreshCw size={14} className="animate-spin" /> : <Save size={14} />}
            Save
          </button>
        </div>
      </div>

      {/* SECRETS */}
      <div className="panel">
        <div className="panel-header"><h2>Secrets</h2></div>
        <div className="panel-body space-y-4">
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">
              GitHub Token {data.github_token_set && <span className="text-[var(--color-accent)]">(set)</span>}
            </label>
            <div className="flex gap-2 mt-1">
              <input
                type={showGithub ? "text" : "password"}
                value={form.github_token || ""}
                onChange={(e) => update("github_token", e.target.value)}
                placeholder={data.github_token_set ? "•••• (saved)" : "ghp_..."}
                className="flex-1 bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)]"
              />
              <button
                onClick={() => setShowGithub(!showGithub)}
                className="p-2 hover:bg-[var(--color-panel-2)] rounded"
              >
                {showGithub ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
            <p className="text-[11px] text-[var(--color-text-dim)] mt-1">
              Required for code-fix Pull Requests. Create at https://github.com/settings/tokens (needs `repo` scope).
            </p>
          </div>
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">
              LLM API Key {data.llm_api_key_set && <span className="text-[var(--color-accent)]">(set)</span>}
            </label>
            <div className="flex gap-2 mt-1">
              <input
                type={showLlm ? "text" : "password"}
                value={form.llm_api_key || ""}
                onChange={(e) => update("llm_api_key", e.target.value)}
                placeholder={data.llm_api_key_set ? "•••• (saved)" : "sk-... or AIza..."}
                className="flex-1 bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)]"
              />
              <button
                onClick={() => setShowLlm(!showLlm)}
                className="p-2 hover:bg-[var(--color-panel-2)] rounded"
              >
                {showLlm ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* LLM */}
      <div className="panel">
        <div className="panel-header"><h2>LLM Provider</h2></div>
        <div className="panel-body space-y-4">
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Provider</label>
            <select
              value={form.llm_provider || "gemini"}
              onChange={(e) => update("llm_provider", e.target.value)}
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1"
            >
              {PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>{p.label}</option>
              ))}
            </select>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Fast model</label>
              <input
                type="text"
                value={form.llm_fast_model || ""}
                onChange={(e) => update("llm_fast_model", e.target.value)}
                placeholder="gemini-2.0-flash"
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
              <p className="text-[11px] text-[var(--color-text-dim)] mt-1">Used for log parsing, evidence gathering</p>
            </div>
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Frontier model</label>
              <input
                type="text"
                value={form.llm_frontier_model || ""}
                onChange={(e) => update("llm_frontier_model", e.target.value)}
                placeholder="gemini-2.5-pro"
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
              <p className="text-[11px] text-[var(--color-text-dim)] mt-1">Used for root-cause synthesis, code patches</p>
            </div>
          </div>
        </div>
      </div>

      {/* OBSERVABILITY */}
      <div className="panel">
        <div className="panel-header"><h2>Observability Sources</h2></div>
        <div className="panel-body space-y-4">
          <p className="text-xs text-[var(--color-text-dim)]">
            Leave empty to disable. In core mode, Sentinel still watches Kubernetes events even if all are empty.
          </p>
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Prometheus URL</label>
            <input
              type="text"
              value={form.prometheus_url || ""}
              onChange={(e) => update("prometheus_url", e.target.value)}
              placeholder="http://prometheus-server.monitoring.svc"
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Loki URL</label>
            <input
              type="text"
              value={form.loki_url || ""}
              onChange={(e) => update("loki_url", e.target.value)}
              placeholder="http://loki.monitoring.svc"
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Jaeger URL</label>
            <input
              type="text"
              value={form.jaeger_url || ""}
              onChange={(e) => update("jaeger_url", e.target.value)}
              placeholder="http://jaeger-query.observability.svc"
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
            />
          </div>
        </div>
      </div>

      {/* SAFETY */}
      <div className="panel">
        <div className="panel-header"><h2>Safety & Governance</h2></div>
        <div className="panel-body space-y-4">
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Production namespaces</label>
            <input
              type="text"
              value={form.production_namespaces || ""}
              onChange={(e) => update("production_namespaces", e.target.value)}
              placeholder="prod,production,payments"
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
            />
            <p className="text-[11px] text-[var(--color-text-dim)] mt-1">Comma-separated. OPA enforces stricter rules here.</p>
          </div>
          <div>
            <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Write namespaces (RBAC)</label>
            <input
              type="text"
              value={form.write_namespaces || ""}
              onChange={(e) => update("write_namespaces", e.target.value)}
              placeholder="default,prod"
              className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
            />
            <p className="text-[11px] text-[var(--color-text-dim)] mt-1">
              Comma-separated. Empty = read-only mode (Sentinel detects but never fixes).
            </p>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Autonomous confidence</label>
              <input
                type="number"
                step="0.01"
                min="0"
                max="1"
                value={form.confidence_threshold_autonomous ?? 0.92}
                onChange={(e) => update("confidence_threshold_autonomous", parseFloat(e.target.value))}
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
            </div>
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Human review threshold</label>
              <input
                type="number"
                step="0.01"
                min="0"
                max="1"
                value={form.confidence_threshold_human_review ?? 0.70}
                onChange={(e) => update("confidence_threshold_human_review", parseFloat(e.target.value))}
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
            </div>
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Panic CPU %</label>
              <input
                type="number"
                step="0.1"
                value={form.panic_cpu_threshold_pct ?? 95.0}
                onChange={(e) => update("panic_cpu_threshold_pct", parseFloat(e.target.value))}
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
            </div>
            <div>
              <label className="text-xs text-[var(--color-text-dim)] uppercase tracking-wider">Panic connections</label>
              <input
                type="number"
                value={form.panic_connections_threshold ?? 9000}
                onChange={(e) => update("panic_connections_threshold", parseInt(e.target.value))}
                className="w-full bg-[var(--color-bg)] border border-[var(--color-border)] rounded px-3 py-1.5 text-sm focus:outline-none focus:border-[var(--color-accent)] mt-1 font-mono"
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
