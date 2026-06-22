"use client";

import { useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import useSWR from "swr";
import { ArrowRight, ArrowLeft, Check, Eye, EyeOff, Loader2 } from "lucide-react";

const fetcher = (url: string) => fetch(url).then((r) => r.json());

const PROVIDERS = [
  { value: "openai", label: "OpenAI (GPT-4o, GPT-4o-mini)", fast: "gpt-4o-mini", frontier: "gpt-4o" },
  { value: "gemini", label: "Google Gemini (2.0 Flash, 2.5 Pro)", fast: "gemini-2.0-flash", frontier: "gemini-2.5-pro" },
  { value: "zai", label: "z.ai GLM (4-flash, 4.5)", fast: "glm-4-flash", frontier: "glm-4.5" },
  { value: "litellm", label: "Anthropic Claude (via LiteLLM proxy)", fast: "anthropic/claude-3-5-haiku-20241022", frontier: "anthropic/claude-3-5-sonnet-20241022" },
  { value: "ollama", label: "Ollama (local models)", fast: "qwen2.5:7b", frontier: "qwen2.5:32b" },
  { value: "vllm", label: "vLLM (self-hosted)", fast: "Qwen/Qwen2.5-7B-Instruct", frontier: "Qwen/Qwen2.5-72B-Instruct" },
  { value: "azure", label: "Azure OpenAI", fast: "gpt-4o-mini", frontier: "gpt-4o" },
];

export default function SetupPage() {
  const router = useRouter();
  const { data: existing } = useSWR("/api/proxy/settings", fetcher);
  const [step, setStep] = useState(1);
  const [githubToken, setGithubToken] = useState("");
  const [llmProvider, setLlmProvider] = useState("gemini");
  const [llmApiKey, setLlmApiKey] = useState("");
  const [llmFastModel, setLlmFastModel] = useState("gemini-2.0-flash");
  const [llmFrontierModel, setLlmFrontierModel] = useState("gemini-2.5-pro");
  const [showGithub, setShowGithub] = useState(false);
  const [showLlm, setShowLlm] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (existing && existing.github_token_set && existing.llm_api_key_set) {
      router.push("/");
    }
  }, [existing, router]);

  const pickProvider = (value: string) => {
    setLlmProvider(value);
    const p = PROVIDERS.find((x) => x.value === value);
    if (p) {
      setLlmFastModel(p.fast);
      setLlmFrontierModel(p.frontier);
    }
  };

  const finish = async () => {
    setSaving(true);
    setError("");
    try {
      const body: Record<string, string> = {
        llm_provider: llmProvider,
        llm_fast_model: llmFastModel,
        llm_frontier_model: llmFrontierModel,
      };
      if (githubToken) body.github_token = githubToken;
      if (llmApiKey) body.llm_api_key = llmApiKey;
      const r = await fetch("/api/proxy/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      router.push("/");
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div style={{ minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center", background: "var(--color-bg)", padding: "1rem" }}>
      <div className="panel" style={{ width: "100%", maxWidth: 560, padding: 0 }}>
        <div className="panel-header">
          <h2 style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ color: "var(--color-accent)", fontWeight: 700 }}>S</span>
            Sentinel Setup — Step {step} of 3
          </h2>
        </div>
        <div className="panel-body" style={{ padding: "2rem" }}>
          <div style={{ display: "flex", gap: 8, marginBottom: "2rem" }}>
            {[1, 2, 3].map((s) => (
              <div key={s} style={{ height: 4, flex: 1, borderRadius: 2, background: s <= step ? "var(--color-accent)" : "var(--color-border)" }} />
            ))}
          </div>
          {error && (
            <div style={{ color: "var(--color-danger)", fontSize: 13, marginBottom: 16, padding: "8px 12px", background: "rgba(255,93,93,0.1)", borderRadius: 3 }}>{error}</div>
          )}
          {step === 1 && (
            <div>
              <h3 style={{ fontSize: 16, marginBottom: 8 }}>GitHub Token</h3>
              <p style={{ fontSize: 13, color: "var(--color-text-dim)", marginBottom: 16 }}>
                Sentinel needs a GitHub Personal Access Token to clone repos, parse stack traces, and open Pull Requests with fixes. Create one at{" "}
                <a href="https://github.com/settings/tokens" target="_blank" rel="noopener noreferrer" style={{ color: "var(--color-accent)" }}>github.com/settings/tokens</a>{" "}
                — it needs the <code style={{ color: "var(--color-accent)" }}>repo</code> scope.
              </p>
              <div style={{ display: "flex", gap: 8, marginBottom: 16 }}>
                <input type={showGithub ? "text" : "password"} value={githubToken} onChange={(e) => setGithubToken(e.target.value)} placeholder="ghp_..." autoFocus
                  style={{ flex: 1, background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "10px 12px", color: "var(--color-text)", fontSize: 14, fontFamily: "var(--font-mono)" }} />
                <button onClick={() => setShowGithub(!showGithub)} style={{ padding: "0 12px", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, cursor: "pointer" }}>
                  {showGithub ? <EyeOff size={14} /> : <Eye size={14} />}
                </button>
              </div>
              <p style={{ fontSize: 11, color: "var(--color-text-dim)" }}>You can skip this and set it later in Settings, but Sentinel won&apos;t be able to open PRs without it.</p>
              <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 24 }}>
                <button onClick={() => setStep(2)} style={{ background: "var(--color-accent)", color: "var(--color-bg)", border: "none", borderRadius: 3, padding: "10px 20px", fontWeight: 600, cursor: "pointer", display: "flex", alignItems: "center", gap: 6 }}>
                  Next <ArrowRight size={14} />
                </button>
              </div>
            </div>
          )}
          {step === 2 && (
            <div>
              <h3 style={{ fontSize: 16, marginBottom: 8 }}>LLM Provider</h3>
              <p style={{ fontSize: 13, color: "var(--color-text-dim)", marginBottom: 16 }}>Pick your LLM provider. Sentinel uses the OpenAI-compatible API, so any of these work.</p>
              <select value={llmProvider} onChange={(e) => pickProvider(e.target.value)}
                style={{ width: "100%", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "10px 12px", color: "var(--color-text)", fontSize: 14, marginBottom: 16 }}>
                {PROVIDERS.map((p) => (<option key={p.value} value={p.value}>{p.label}</option>))}
              </select>
              <label style={{ fontSize: 12, color: "var(--color-text-dim)", textTransform: "uppercase", letterSpacing: "0.05em" }}>API Key</label>
              <div style={{ display: "flex", gap: 8, marginBottom: 16, marginTop: 4 }}>
                <input type={showLlm ? "text" : "password"} value={llmApiKey} onChange={(e) => setLlmApiKey(e.target.value)} placeholder={llmProvider === "ollama" || llmProvider === "vllm" ? "dummy (no auth needed)" : "sk-... or AIza..."}
                  style={{ flex: 1, background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "10px 12px", color: "var(--color-text)", fontSize: 14, fontFamily: "var(--font-mono)" }} />
                <button onClick={() => setShowLlm(!showLlm)} style={{ padding: "0 12px", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, cursor: "pointer" }}>
                  {showLlm ? <EyeOff size={14} /> : <Eye size={14} />}
                </button>
              </div>
              <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, marginBottom: 16 }}>
                <div>
                  <label style={{ fontSize: 12, color: "var(--color-text-dim)", textTransform: "uppercase", letterSpacing: "0.05em" }}>Fast model</label>
                  <input type="text" value={llmFastModel} onChange={(e) => setLlmFastModel(e.target.value)} style={{ width: "100%", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "8px 12px", color: "var(--color-text)", fontSize: 13, fontFamily: "var(--font-mono)", marginTop: 4 }} />
                </div>
                <div>
                  <label style={{ fontSize: 12, color: "var(--color-text-dim)", textTransform: "uppercase", letterSpacing: "0.05em" }}>Frontier model</label>
                  <input type="text" value={llmFrontierModel} onChange={(e) => setLlmFrontierModel(e.target.value)} style={{ width: "100%", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "8px 12px", color: "var(--color-text)", fontSize: 13, fontFamily: "var(--font-mono)", marginTop: 4 }} />
                </div>
              </div>
              <div style={{ display: "flex", justifyContent: "space-between", gap: 8, marginTop: 24 }}>
                <button onClick={() => setStep(1)} style={{ background: "transparent", color: "var(--color-text-dim)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "10px 20px", cursor: "pointer", display: "flex", alignItems: "center", gap: 6 }}>
                  <ArrowLeft size={14} /> Back
                </button>
                <button onClick={() => setStep(3)} style={{ background: "var(--color-accent)", color: "var(--color-bg)", border: "none", borderRadius: 3, padding: "10px 20px", fontWeight: 600, cursor: "pointer", display: "flex", alignItems: "center", gap: 6 }}>
                  Next <ArrowRight size={14} />
                </button>
              </div>
            </div>
          )}
          {step === 3 && (
            <div style={{ textAlign: "center" }}>
              <div style={{ width: 48, height: 48, borderRadius: "50%", background: "rgba(0,217,163,0.15)", display: "flex", alignItems: "center", justifyContent: "center", margin: "0 auto 16px" }}>
                <Check size={24} style={{ color: "var(--color-accent)" }} />
              </div>
              <h3 style={{ fontSize: 16, marginBottom: 8 }}>Ready to go</h3>
              <p style={{ fontSize: 13, color: "var(--color-text-dim)", marginBottom: 24 }}>
                Sentinel is configured. You can change any of these settings later from the <strong style={{ color: "var(--color-text)" }}>Settings</strong> page.
              </p>
              <div style={{ textAlign: "left", background: "var(--color-bg)", border: "1px solid var(--color-border)", borderRadius: 3, padding: 16, marginBottom: 24, fontSize: 13, fontFamily: "var(--font-mono)" }}>
                <div>GitHub: {githubToken ? "✓ configured" : "— skipped (set later)"}</div>
                <div>LLM: {llmProvider} ({llmFrontierModel})</div>
                <div>API key: {llmApiKey ? "✓ configured" : "— skipped"}</div>
              </div>
              <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
                <button onClick={() => setStep(2)} style={{ background: "transparent", color: "var(--color-text-dim)", border: "1px solid var(--color-border)", borderRadius: 3, padding: "10px 20px", cursor: "pointer", display: "flex", alignItems: "center", gap: 6 }}>
                  <ArrowLeft size={14} /> Back
                </button>
                <button onClick={finish} disabled={saving} style={{ background: "var(--color-accent)", color: "var(--color-bg)", border: "none", borderRadius: 3, padding: "10px 24px", fontWeight: 600, cursor: saving ? "not-allowed" : "pointer", display: "flex", alignItems: "center", gap: 6 }}>
                  {saving ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                  {saving ? "Saving..." : "Finish"}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
