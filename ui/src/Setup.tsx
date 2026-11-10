import { useEffect, useState } from "react";
import { api, type SettingsSpec } from "./api";

/** First-run: pick which namespaces Sentinel may watch. */
export function Setup({ onDone }: { onDone: () => void }) {
  const [rows, setRows] = useState<{ name: string; blocked: boolean; reason?: string }[]>([]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [base, setBase] = useState<SettingsSpec>({});
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    Promise.all([api.namespaces(), api.settings()])
      .then(([ns, settings]) => {
        setRows(ns.namespaces.filter((n) => !n.blocked).map((n) => ({ name: n.name, blocked: n.blocked, reason: n.reason })));
        const blocked = new Set(ns.namespaces.filter((n) => n.blocked).map((n) => n.name));
        setPicked(new Set((settings.spec?.watch?.namespaces ?? []).filter((n) => !blocked.has(n))));
        setBase(settings.spec ?? {});
      })
      .catch((e: Error) => setError(e.message));
  }, []);

  const toggle = (name: string) => {
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  const save = async () => {
    if (picked.size === 0) {
      setError("Pick at least one application namespace.");
      return;
    }
    setSaving(true);
    setError("");
    try {
      const namespaces = [...picked].sort();
      await api.putSettings({ ...base, watch: { namespaces } });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="empty" style={{ maxWidth: 520, margin: "2rem auto", textAlign: "left" }}>
      <h2>What should Sentinel watch?</h2>
      <p className="muted">
        Pick the application namespaces. Sentinel will not see the rest of the cluster. You can change this later under
        Settings.
      </p>
      {error && <div className="error-banner">{error}</div>}
      {!rows.length && !error ? (
        <p className="muted">Loading namespaces…</p>
      ) : (
        <ul className="installed-map" style={{ margin: "1rem 0" }}>
          {rows.map((n) => (
            <li key={n.name}>
              <label className="check" style={{ display: "flex", gap: 8, alignItems: "center" }}>
                <input type="checkbox" checked={picked.has(n.name)} onChange={() => toggle(n.name)} />
                <span className="mono">{n.name}</span>
              </label>
            </li>
          ))}
        </ul>
      )}
      <button className="btn primary" type="button" disabled={saving || picked.size === 0} onClick={() => void save()}>
        {saving ? "Saving…" : "Start watching"}
      </button>
    </div>
  );
}
