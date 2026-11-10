import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  api,
  type FixerLLMSettings,
  type InstalledComponents,
  type SettingsSpec,
} from "./api";
import { useSession } from "./session";

type Draft = SettingsSpec & { _installed?: InstalledComponents; _message?: string };

function Toggle({
  label,
  help,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  help: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label className={`setting-row ${disabled ? "disabled" : ""}`} title={disabled ? "Install this part of Sentinel first (Helm)" : help}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        <span className="setting-label">{label}</span>
        <span className="setting-help">{help}</span>
      </span>
    </label>
  );
}

function Field({
  label,
  help,
  children,
}: {
  label: string;
  help: string;
  children: ReactNode;
}) {
  return (
    <label className="setting-field">
      <span className="setting-label">{label}</span>
      <span className="setting-help">{help}</span>
      {children}
    </label>
  );
}

function Section({
  title,
  lead,
  badge,
  children,
}: {
  title: string;
  lead: string;
  badge?: string;
  children: ReactNode;
}) {
  return (
    <section className="card settings-section">
      <div className="settings-section-head">
        <h3>{title}</h3>
        {badge && <span className={`pill ${badge === "Installed" ? "green" : "amber"}`}>{badge}</span>}
      </div>
      <p className="muted settings-lead">{lead}</p>
      <div className="settings-body">{children}</div>
    </section>
  );
}

function InstalledMap({ installed }: { installed?: InstalledComponents }) {
  const rows: { key: keyof InstalledComponents; name: string; means: string }[] = [
    { key: "actor", name: "Actor", means: "Can roll back and scale apps when a policy allows it" },
    { key: "lab", name: "Lab", means: "Tests fixes in a sealed copy before trusting them" },
    { key: "fixer", name: "Fixer", means: "Opens pull requests so rollbacks hold in Git" },
    { key: "capture", name: "Traffic capture", means: "Sees requests apps serve, for the Lab to replay" },
    { key: "captureMitm", name: "HTTPS body MITM", means: "Decrypts HTTPS for opted-in apps (HTTPS_PROXY + trust CA)" },
    { key: "nodeAgent", name: "Node agent", means: "Reads redacted logs from each machine" },
  ];
  return (
    <ul className="installed-map">
      {rows.map((r) => {
        const on = !!installed?.[r.key];
        return (
          <li key={r.key}>
            <span className="dot" style={{ color: on ? "var(--green)" : "var(--faint)" }} />
            <span>
              <strong>{r.name}</strong>
              <span className="muted"> — {on ? r.means : "Not installed yet. Turn it on in the Helm chart, then it appears here."}</span>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function durationValue(d?: string, fallback = ""): string {
  return d && d.length > 0 ? d : fallback;
}

export function Settings() {
  const { me } = useSession();
  const [draft, setDraft] = useState<Draft>();
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(() => {
    api
      .settings()
      .then((s) => {
        setDraft({ ...(s.spec ?? {}), _installed: s.status?.installed, _message: s.status?.message });
        setError("");
      })
      .catch((e: Error) => setError(e.message));
  }, []);

  useEffect(() => {
    if (me) load();
  }, [me, load]);

  if (!me) {
    return (
      <div className="empty">
        <h2>Settings</h2>
        <p>Sign in to see what is installed and to turn features on or off without editing Helm YAML.</p>
      </div>
    );
  }
  if (!draft && !error) return <p className="empty">Loading settings…</p>;

  const installed = draft?._installed;
  const set = (patch: Partial<SettingsSpec>) => setDraft({ ...draft!, ...patch });
  const setDetection = (patch: NonNullable<SettingsSpec["detection"]>) =>
    set({ detection: { ...draft!.detection, ...patch } });
  const setLogs = (patch: NonNullable<SettingsSpec["logs"]>) => set({ logs: { ...draft!.logs, ...patch } });
  const setLab = (patch: NonNullable<SettingsSpec["lab"]>) => set({ lab: { ...draft!.lab, ...patch } });
  const setActor = (patch: NonNullable<SettingsSpec["actor"]>) => set({ actor: { ...draft!.actor, ...patch } });
  const setCapture = (patch: NonNullable<SettingsSpec["capture"]>) =>
    set({ capture: { ...draft!.capture, ...patch } });
  const setFixerCode = (patch: NonNullable<SettingsSpec["fixer"]>["code"]) =>
    set({ fixer: { code: { ...draft!.fixer?.code, ...patch } } });
  const setLLM = (patch: FixerLLMSettings) =>
    setFixerCode({ llm: { ...draft!.fixer?.code?.llm, ...patch } });

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    setError("");
    setMessage("");
    try {
      const { _installed: _, _message: __, ...spec } = draft;
      setMessage((await api.putSettings(spec)).done);
      load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const fixers = draft?.fixer?.code?.fixers ?? [];
  const fixerOptions = [
    { id: "restore", label: "Restore the crashing function" },
    { id: "template", label: "Apply a known fix template" },
    { id: "mine", label: "Learn from this repo’s past fixes" },
    { id: "revert", label: "Revert the whole bad commit" },
    { id: "llm", label: "Ask a language model (last resort)" },
  ];

  return (
    <div className="settings">
      <h2>Settings</h2>
      <p className="muted">
        Everything you can change while Sentinel is running. Per-app rules (who may act in which namespace) live under{" "}
        <a href="#/policies">Policies</a>. Installing new components (Lab pool, capture agent, Fixer forge token) still
        needs the chart once — then you control them here.
      </p>
      {error && <div className="error-banner">{error}</div>}
      {message && <div className="ok-banner">{message}</div>}
      {draft?._message && <p className="muted">Last apply: {draft._message}</p>}

      <Section title="What’s installed" lead="A map of the pieces this cluster has. Grey means install with Helm first; green means you can configure it below.">
        <InstalledMap installed={installed} />
      </Section>

      <Section
        title="Detection"
        lead="How sensitive Sentinel is when something looks wrong. Lower false alarms = quieter; shorter windows = faster but noisier."
      >
        <Field label="False alarms per app per week" help="Budget for mistaken alerts. 1 is the usual starting point.">
          <input
            className="field"
            type="number"
            min={0}
            max={100}
            value={draft?.detection?.falseAlarmsPerWeek ?? 1}
            onChange={(e) => setDetection({ falseAlarmsPerWeek: Number(e.target.value) })}
          />
        </Field>
        <div className="settings-grid">
          <Field label="Look-back window" help="How far back each check looks (e.g. 5m).">
            <input
              className="field"
              value={durationValue(draft?.detection?.window, "5m")}
              onChange={(e) => setDetection({ window: e.target.value })}
            />
          </Field>
          <Field label="Check every" help="How often detectors run (e.g. 15s).">
            <input
              className="field"
              value={durationValue(draft?.detection?.tick, "15s")}
              onChange={(e) => setDetection({ tick: e.target.value })}
            />
          </Field>
          <Field label="Quiet before closing" help="How long trouble must stay gone before an incident closes.">
            <input
              className="field"
              value={durationValue(draft?.detection?.incidentGap, "5m")}
              onChange={(e) => setDetection({ incidentGap: e.target.value })}
            />
          </Field>
          <Field label="Warm-up" help="How long a new app is watched before alerts are trusted.">
            <input
              className="field"
              value={durationValue(draft?.detection?.warmUp, "30m")}
              onChange={(e) => setDetection({ warmUp: e.target.value })}
            />
          </Field>
        </div>
        <Toggle
          label="Collect container logs"
          help="Pulls redacted crash and error logs into incidents."
          checked={draft?.logs?.enabled !== false}
          onChange={(v) => setLogs({ enabled: v })}
        />
      </Section>

      <Section
        title="Traffic capture"
        lead="Cluster-wide: whether the capture agent may export request bodies. Each namespace still opts in under Policies."
        badge={installed?.capture ? "Installed" : "Not installed"}
      >
        <Toggle
          label="Export HTTP bodies from the agent"
          help="Turns on body attributes in the capture agent. Without this, Policies’ “record bodies” has nothing to keep. Does not decrypt HTTPS by itself."
          checked={!!draft?.capture?.exportBodies}
          disabled={!installed?.capture}
          onChange={(v) => setCapture({ exportBodies: v })}
        />
        <Toggle
          label="Run HTTPS MITM for opted-in apps"
          help="Starts the CONNECT proxy that terminates TLS for apps that set HTTPS_PROXY and trust the capture CA, then posts bodies to Sentinel. Off keeps ciphertext invisible to capture."
          checked={!!draft?.capture?.mitmEnabled}
          disabled={!installed?.captureMitm}
          onChange={(v) => setCapture({ mitmEnabled: v })}
        />
        <Field label="Body buffer (bytes)" help="How much of each request/response the agent may hold before redaction.">
          <input
            className="field"
            type="number"
            min={1024}
            max={65536}
            disabled={!installed?.capture}
            value={draft?.capture?.bodyBufferBytes ?? 8192}
            onChange={(e) => setCapture({ bodyBufferBytes: Number(e.target.value) })}
          />
        </Field>
      </Section>

      <Section
        title="Acting"
        lead="The kill switch and how often Sentinel may act in a namespace. What it may do still comes from Policies."
        badge={installed?.actor ? "Installed" : "Not installed"}
      >
        <Toggle
          label="Allow actions"
          help="Off rejects every action request immediately. Use this as an emergency stop."
          checked={draft?.actor?.actionsEnabled !== false}
          disabled={!installed?.actor}
          onChange={(v) => setActor({ actionsEnabled: v })}
        />
        <Field label="Actions per namespace per hour" help="Hard cap so a noisy namespace cannot thrash.">
          <input
            className="field"
            type="number"
            min={0}
            max={1000}
            disabled={!installed?.actor}
            value={draft?.actor?.perHour ?? 6}
            onChange={(e) => setActor({ perHour: Number(e.target.value) })}
          />
        </Field>
      </Section>

      <Section
        title="Lab"
        lead="Whether Sentinel asks the Lab to prove a rollback before trusting it."
        badge={installed?.lab ? "Installed" : "Not installed"}
      >
        <Toggle
          label="Verify rollbacks in the Lab"
          help="When on, proposed rollbacks are raced in a sealed copy before autonomy treats them as proven."
          checked={draft?.lab?.verifyRollbacks !== false}
          disabled={!installed?.lab}
          onChange={(v) => setLab({ verifyRollbacks: v })}
        />
      </Section>

      <Section
        title="Fixer"
        lead="How permanent fixes are proposed in Git. Forge login and tokens stay in the chart (secrets); behaviour is here."
        badge={installed?.fixer ? "Installed" : "Not installed"}
      >
        <Toggle
          label="Also fix crashes with nothing to roll back"
          help="Older bugs that survived several deploys can still get a code fix."
          checked={draft?.fixer?.code?.withoutRollback !== false}
          disabled={!installed?.fixer}
          onChange={(v) => setFixerCode({ withoutRollback: v })}
        />
        <p className="setting-label" style={{ marginTop: 12 }}>
          Fix strategies (in order)
        </p>
        <p className="setting-help">Uncheck to skip a strategy. The language model only runs if listed and configured below.</p>
        <div className="fixer-list">
          {fixerOptions.map((f) => (
            <label key={f.id} className={`check ${!installed?.fixer ? "disabled" : ""}`}>
              <input
                type="checkbox"
                disabled={!installed?.fixer}
                checked={fixers.includes(f.id)}
                onChange={(e) => {
                  const next = e.target.checked ? [...fixers, f.id] : fixers.filter((x) => x !== f.id);
                  setFixerCode({ fixers: next });
                }}
              />
              {f.label}
            </label>
          ))}
        </div>
        <Field label="LLM API URL" help="Empty turns the language-model fixer off. Key stays in the chart Secret.">
          <input
            className="field"
            disabled={!installed?.fixer}
            placeholder="https://…"
            value={draft?.fixer?.code?.llm?.url ?? ""}
            onChange={(e) => setLLM({ url: e.target.value })}
          />
        </Field>
        <div className="settings-grid">
          <Field label="Model" help="Model name the API expects.">
            <input
              className="field"
              disabled={!installed?.fixer}
              value={draft?.fixer?.code?.llm?.model ?? ""}
              onChange={(e) => setLLM({ model: e.target.value })}
            />
          </Field>
          <Field label="Format" help="openai or anthropic.">
            <select
              className="field"
              disabled={!installed?.fixer}
              value={draft?.fixer?.code?.llm?.format || "openai"}
              onChange={(e) => setLLM({ format: e.target.value })}
            >
              <option value="openai">openai</option>
              <option value="anthropic">anthropic</option>
            </select>
          </Field>
          <Field label="Max attempts" help="How many model edits per fix.">
            <input
              className="field"
              type="number"
              min={1}
              max={10}
              disabled={!installed?.fixer}
              value={draft?.fixer?.code?.llm?.maxAttempts ?? 2}
              onChange={(e) => setLLM({ maxAttempts: Number(e.target.value) })}
            />
          </Field>
        </div>
      </Section>

      <Section title="Diagnostics" lead="How chatty Sentinel’s own logs are, and how often it re-checks what the cluster allows.">
        <Field label="Log level" help="debug is noisy; info is normal; warn/error are quieter.">
          <select className="field" value={draft?.logLevel || "info"} onChange={(e) => set({ logLevel: e.target.value })}>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </Field>
        <Field label="Capability check interval" help="How often “What Sentinel can do here” is refreshed.">
          <input
            className="field"
            value={durationValue(draft?.capabilities?.interval, "10m")}
            onChange={(e) => set({ capabilities: { interval: e.target.value } })}
          />
        </Field>
      </Section>

      <div className="policy-actions sticky-save">
        <button className="btn" onClick={load} disabled={saving}>
          Reload
        </button>
        <div className="spacer" />
        <button className="btn primary" onClick={() => void save()} disabled={saving || !draft}>
          {saving ? "Saving…" : "Save settings"}
        </button>
      </div>
    </div>
  );
}
