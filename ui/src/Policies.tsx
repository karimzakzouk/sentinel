import { useCallback, useEffect, useState } from "react";
import { api, type AutonomyRule, type NamespacePolicies, type Policy, type PolicySpec } from "./api";
import { useSession } from "./session";

// The actions a policy can let Sentinel take, and what each level means.
// Keep in sync with ActionRequest's action enum plus fix.merge (policy-only).
const actions: { id: string; label: string }[] = [
  { id: "rollout.undo", label: "Roll back a rollout" },
  { id: "scale", label: "Restore replicas" },
  { id: "restart", label: "Restart pods" },
  { id: "argocd.rollback", label: "Pause Argo CD, then roll back" },
  { id: "flux.rollback", label: "Suspend Flux, then roll back" },
  { id: "helm.rollback", label: "Roll back a Helm release" },
  { id: "rollouts.abort", label: "Abort an Argo Rollout" },
  { id: "fix.merge", label: "Merge its own fix in Git" },
];

const levels: { id: string; label: string; meaning: string }[] = [
  { id: "observe", label: "Observe", meaning: "records what it would do, does nothing" },
  { id: "recommend", label: "Recommend", meaning: "tells people what to do" },
  { id: "approve", label: "Approve", meaning: "acts once a person approves" },
  { id: "lab-verified", label: "Lab verified", meaning: "acts once the Lab verifies the fix; else waits for a person" },
  { id: "act-unless-vetoed", label: "Act unless vetoed", meaning: "acts after a while unless a person vetoes" },
  { id: "autonomous", label: "Autonomous", meaning: "acts on its own" },
];

function Rule({ rule, onChange, onRemove }: { rule: AutonomyRule; onChange: (r: AutonomyRule) => void; onRemove: () => void }) {
  const level = levels.find((l) => l.id === rule.level);
  return (
    <div className="rule">
      <select className="field" value={rule.action} aria-label="Action" onChange={(e) => onChange({ ...rule, action: e.target.value })}>
        {actions.map((a) => (
          <option key={a.id} value={a.id}>
            {a.label}
          </option>
        ))}
        {!actions.some((a) => a.id === rule.action) && <option value={rule.action}>{rule.action}</option>}
      </select>
      <select className="field" value={rule.level} aria-label="Level" title={level?.meaning}
        onChange={(e) => onChange({ ...rule, level: e.target.value })}>
        {levels.map((l) => (
          <option key={l.id} value={l.id} title={l.meaning}>
            {l.label}
          </option>
        ))}
      </select>
      <label className="check" title="Held at approve until its track record on this workload and fault earns the level">
        <input type="checkbox" checked={!!rule.earned} onChange={(e) => onChange({ ...rule, earned: e.target.checked || undefined })} />
        earned by its track record
      </label>
      <button className="btn" aria-label="Remove" onClick={onRemove}>
        Remove
      </button>
    </div>
  );
}

function captureOf(requests: boolean, bodies: boolean): PolicySpec["capture"] {
  if (!requests && !bodies) return undefined;
  // Bodies only applies with requests; turning bodies on alone also turns requests on.
  if (bodies && !requests) requests = true;
  return { ...(requests ? { requests: true } : {}), ...(bodies ? { bodies: true } : {}) };
}

function PolicyCard({ policy, onSaved }: { policy: Policy; onSaved: () => void }) {
  const [spec, setSpec] = useState<PolicySpec>(policy.spec ?? {});
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const { namespace, name } = policy.metadata;
  const rules = spec.autonomy ?? [];
  const setRules = (autonomy: AutonomyRule[]) => setSpec({ ...spec, autonomy });
  const run = async (what: () => Promise<{ done: string }>) => {
    setError("");
    setMessage("");
    try {
      setMessage((await what()).done);
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <div className="card policy">
      <h3>
        {name} <span className="muted">in {namespace}</span>
      </h3>
      {rules.length === 0 && <p className="muted">No actions: Sentinel only observes here.</p>}
      {rules.map((r, i) => (
        <Rule
          key={i}
          rule={r}
          onChange={(next) => setRules(rules.map((x, j) => (j === i ? next : x)))}
          onRemove={() => setRules(rules.filter((_, j) => j !== i))}
        />
      ))}
      <label className="check capture" title="What traffic capture's agent sees of this namespace's requests, kept so the Lab can replay reads">
        <input
          type="checkbox"
          checked={!!spec.capture?.requests}
          onChange={(e) => setSpec({ ...spec, capture: captureOf(e.target.checked, e.target.checked ? !!spec.capture?.bodies : false) })}
        />
        Record request details: method, route, status and timing, never bodies unless opted in below. Needs traffic capture installed.
      </label>
      <label
        className="check capture"
        title="Also keep redacted request and response bodies, capped ~4KiB, for Lab write replay. Only with request details on."
      >
        <input
          type="checkbox"
          checked={!!spec.capture?.bodies}
          onChange={(e) => setSpec({ ...spec, capture: captureOf(!!spec.capture?.requests || e.target.checked, e.target.checked) })}
        />
        Record request and response bodies (needs chart capture.exportBodies and plain HTTP JSON from OBI). Off by default.
      </label>
      <p className="muted policy-note">
        Cluster-wide Fixer knobs stay in the chart&apos;s values, not in a policy:{" "}
        <code>fixer.code.withoutRollback</code>, <code>fixer.code.fixers</code>, and <code>fixer.code.llm</code>. See{" "}
        <code>charts/sentinel/values.yaml</code>.
      </p>
      <div className="policy-actions">
        <button
          className="btn"
          onClick={() => setRules([...rules, { action: actions.find((a) => !rules.some((r) => r.action === a.id))?.id ?? "rollout.undo", level: "approve" }])}
        >
          Add an action
        </button>
        <div className="spacer" />
        <button className="btn danger" onClick={() => confirm(`Delete the policy ${namespace}/${name}?`) && run(() => api.deletePolicy(namespace, name))}>
          Delete
        </button>
        <button className="btn primary" onClick={() => run(() => api.putPolicy(namespace, name, spec))}>
          Save
        </button>
      </div>
      {message && <div className="ok-banner">{message}</div>}
      {error && <div className="error-banner">{error}</div>}
    </div>
  );
}

// Policies shows, per namespace Sentinel watches, what its policies let it
// do, and lets a signed-in person change them as themselves. The same
// policies stay YAML a person can keep in Git; the console sets only the
// fields it shows.
export function Policies() {
  const { me } = useSession();
  const [all, setAll] = useState<NamespacePolicies[]>();
  const [error, setError] = useState("");
  const [newName, setNewName] = useState<Record<string, string>>({});
  const load = useCallback(() => {
    api.policies().then(setAll, (e: Error) => setError(e.message));
  }, []);
  useEffect(() => {
    if (me) load();
  }, [me, load]);
  if (!me) {
    return (
      <div className="empty">
        <h2>Policies</h2>
        <p>Sign in to see what Sentinel may do in each namespace, and to change it with your own permissions.</p>
      </div>
    );
  }
  return (
    <div className="policies">
      <h2>Policies</h2>
      <p className="muted">
        What Sentinel may do in each namespace it watches. Unlisted actions stay at observe. Changes are made as {me.user}; the
        cluster refuses what your permissions do not allow.
      </p>
      {error && <div className="error-banner">{error}</div>}
      {all?.map((ns) => (
        <section key={ns.namespace} className="namespace">
          <h3>{ns.namespace}</h3>
          {ns.error && <div className="error-banner">{ns.error}</div>}
          {ns.policies.map((p) => (
            <PolicyCard key={p.metadata.name} policy={p} onSaved={load} />
          ))}
          {!ns.error && (
            <div className="new-policy">
              <input
                className="field"
                placeholder="new policy's name"
                aria-label={`New policy in ${ns.namespace}`}
                value={newName[ns.namespace] ?? ""}
                onChange={(e) => setNewName({ ...newName, [ns.namespace]: e.target.value })}
              />
              <button
                className="btn"
                disabled={!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(newName[ns.namespace] ?? "")}
                onClick={() => {
                  const name = newName[ns.namespace] ?? "";
                  const added: Policy = { metadata: { name, namespace: ns.namespace }, spec: { autonomy: [] } };
                  setAll(all.map((x) => (x.namespace === ns.namespace ? { ...x, policies: [...x.policies, added] } : x)));
                  setNewName({ ...newName, [ns.namespace]: "" });
                }}
              >
                New policy
              </button>
            </div>
          )}
        </section>
      ))}
    </div>
  );
}
