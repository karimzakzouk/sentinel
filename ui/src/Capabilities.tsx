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
  act: "Acting (later phases)",
  lab: "Lab sandbox",
  "lab.user-namespaces": "User namespaces in the Lab",
  "lab.sandbox-runtime": "Sandboxed runtime",
};

const statusPill: Record<string, string> = { available: "green", unavailable: "red", unknown: "amber" };

export function Capabilities() {
  const report = usePoll(api.capabilities, [], 30_000);
  if (report.error && !report.data) return <div className="error-banner">{report.error}</div>;
  if (!report.data) return <p className="empty">Loading…</p>;
  const r = report.data;
  return (
    <article>
      <div className="title-row">
        <h1>What Sentinel can do here</h1>
      </div>
      <p className="subtitle">
        Checked {ago(r.generatedAt)} as <span className="mono">{r.identity}</span> on {r.cluster.platform}{" "}
        {r.cluster.serverVersion}, {r.cluster.nodes} node(s)
        {r.cluster.networkPlugin ? `, ${r.cluster.networkPlugin}` : ""}.
      </p>
      {r.warnings && r.warnings.length > 0 && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--amber)" }}>
          <h2>Warnings</h2>
          <ul>
            {r.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </div>
      )}
      <section className="card" style={{ marginBottom: 16 }}>
        <h2>Features</h2>
        <div className="features">
          {r.features.map((f) => (
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
      <section className="card">
        <h2>Checks</h2>
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
                <td className="mono">{c.id}</td>
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
