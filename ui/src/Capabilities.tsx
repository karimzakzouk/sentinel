import { api } from "./api";
import { ago } from "./format";
import { usePoll } from "./usePoll";

// Plain names for the features the capability report derives.
const featureNames: Record<string, string> = {
  "detect.api": "Kubernetes signals",
  "detect.logs": "Log evidence",
  "detect.metrics": "Resource metrics",
  "detect.node-agent": "Node agent",
  "detect.ebpf": "eBPF",
  act: "Acting on workloads",
  lab: "Lab sandbox",
  "capture.traffic": "Traffic capture",
  "fixer.code": "Code fixes",
  "lab.user-namespaces": "User namespaces in the Lab",
  "lab.sandbox-runtime": "Sandboxed runtime",
};

// Tools that may own workloads; Sentinel's changes must go through them.
const ownerNames: Record<string, string> = {
  "owner.argocd": "Argo CD",
  "owner.argo-rollouts": "Argo Rollouts",
  "owner.flux": "Flux",
  "owner.flux-helm": "Flux Helm releases",
  "owner.flagger": "Flagger",
  "owner.helm": "Helm",
};

const statusPill: Record<string, string> = { available: "green", unavailable: "red", unknown: "amber" };

// The report states each permission Sentinel must not hold as its own
// warning; a cluster admin's identity holds dozens.
const excessPermission = /^(\S+) can (.+?) (?:cluster-wide|in (\S+)); Sentinel must not hold this permission, remove it$/;

function Warnings({ warnings }: { warnings: string[] }) {
  const byScope = new Map<string, string[]>();
  const other: string[] = [];
  let identity = "";
  for (const w of warnings) {
    const m = excessPermission.exec(w);
    if (!m) {
      other.push(w);
      continue;
    }
    const [, who = "", permission = "", namespace] = m;
    identity = who;
    const scope = namespace ? `in ${namespace}` : "cluster-wide";
    byScope.set(scope, [...(byScope.get(scope) ?? []), permission]);
  }
  const excess = [...byScope.values()].reduce((n, perms) => n + perms.length, 0);
  return (
    <div className="card warnings">
      <h2>Warnings</h2>
      {excess > 0 && (
        <>
          <p>
            <span className="mono">{identity}</span> holds {excess} permission{excess === 1 ? "" : "s"} Sentinel must
            not have. Run Sentinel as its own service account, or remove them.
          </p>
          <details>
            <summary>Show the permissions</summary>
            <table className="table">
              <tbody>
                {[...byScope].map(([scope, perms]) => (
                  <tr key={scope}>
                    <td className="scope">{scope}</td>
                    <td>
                      {perms.map((p) => (
                        <span key={p} className="chip mono">
                          {p}
                        </span>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        </>
      )}
      {other.length > 0 && (
        <ul>
          {other.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function Capabilities() {
  const report = usePoll(api.capabilities, [], 30_000);
  if (report.error && !report.data) return <div className="error-banner">{report.error}</div>;
  if (!report.data) return <p className="empty">Loading…</p>;
  const r = report.data;
  const owners = r.features.filter((f) => f.name.startsWith("owner."));
  const features = r.features.filter((f) => !f.name.startsWith("owner."));
  return (
    <article>
      <div className="title-row">
        <h1>This cluster</h1>
      </div>
      <p className="subtitle">
        What Sentinel is allowed to see and do here. Checked {ago(r.generatedAt)} as{" "}
        <span className="mono">{r.identity}</span> on {r.cluster.platform} {r.cluster.serverVersion}, {r.cluster.nodes}{" "}
        node(s)
        {r.cluster.networkPlugin ? `, network: ${r.cluster.networkPlugin}` : ""}. Turn behaviour on under{" "}
        <a href="#/settings">Settings</a>; set per-namespace rules under <a href="#/policies">Policies</a>.
      </p>
      {r.warnings && r.warnings.length > 0 && <Warnings warnings={r.warnings} />}
      <section className="card" style={{ marginBottom: 16 }}>
        <h2>What works on this cluster</h2>
        <div className="features">
          {features.map((f) => (
            <div key={f.name} className="feature">
              <div className="name">
                <span className="dot" style={{ color: f.enabled ? "var(--green)" : "var(--faint)" }} />
                {featureNames[f.name] ?? f.name}
              </div>
              <div className="why">{f.reason}</div>
            </div>
          ))}
        </div>
      </section>
      {owners.length > 0 && (
        <section className="card" style={{ marginBottom: 16 }}>
          <h2>Who else manages the workloads</h2>
          <ul className="owners">
            {owners.map((f) => (
              <li key={f.name}>
                <span className="dot" style={{ color: f.enabled ? "var(--amber)" : "var(--faint)" }} />
                <span className="name">{ownerNames[f.name] ?? f.name}</span>
                <span className="why">{f.reason}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      <section className="card">
        <h2>Technical checks</h2>
        <p className="muted">For operators who want the raw probe results.</p>
        <table className="table">
          <thead>
            <tr>
              <th>Check</th>
              <th>Status</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {r.checks.map((c) => (
              <tr key={c.id}>
                <td>{featureNames[c.id] ?? c.id}</td>
                <td>
                  <span className={`pill ${statusPill[c.status] ?? ""}`}>{c.status}</span>
                </td>
                <td>{c.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </article>
  );
}
