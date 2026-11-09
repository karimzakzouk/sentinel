import { api, type ActionSummary } from "./api";
import { actionLabel, actionStages, ago, clock, workload } from "./format";
import { usePoll } from "./usePoll";

function Row({ s, onOpen }: { s: ActionSummary; onOpen: (id: string) => void }) {
  const a = s.action;
  const stage = actionStages[`sentinel.action.${s.stage}`];
  const detail = a.params?.verification ?? a.params?.result ?? a.params?.breaker ?? a.params?.reason;
  return (
    <tr>
      <td title={`first recorded ${clock(s.since)}`}>{ago(s.at)}</td>
      <td className="mono">{workload(a.target)}</td>
      <td>{actionLabel(a.type)}</td>
      <td>
        {stage ? <span className={`pill ${stage.tone}`}>{stage.pill}</span> : <span className="pill">would have</span>}
        {a.params?.approvedBy && <div className="note">approved by {a.params.approvedBy}</div>}
      </td>
      <td className="detail">{detail}</td>
      <td>
        <a href={`#/incidents/${a.incidentId}`} onClick={() => onOpen(a.incidentId)}>
          incident
        </a>
      </td>
    </tr>
  );
}

function Table({ rows, onOpen }: { rows: ActionSummary[]; onOpen: (id: string) => void }) {
  return (
    <table className="table actions">
      <thead>
        <tr>
          <th>When</th>
          <th>Workload</th>
          <th>Action</th>
          <th>State</th>
          <th>What happened</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {rows.map((s) => (
          <Row key={s.action.id} s={s} onOpen={onOpen} />
        ))}
      </tbody>
    </table>
  );
}

// Actions is the audit view: what Sentinel did where a policy let it, and
// what it would have done elsewhere.
export function Actions({ onOpen }: { onOpen: (id: string) => void }) {
  const actions = usePoll(api.actions, []);
  if (actions.error && !actions.data) return <div className="error-banner">{actions.error}</div>;
  if (!actions.data) return <p className="empty">Loading…</p>;
  const taken = actions.data.filter((s) => !s.action.shadow);
  const shadow = actions.data.filter((s) => s.action.shadow);
  return (
    <article>
      <div className="title-row">
        <h1>What Sentinel did</h1>
      </div>
      <p className="subtitle">
        Actions Sentinel took where a SentinelPolicy let it, and what it would have done everywhere else. Every step is in the
        Ledger.
      </p>
      <section className="card" style={{ marginBottom: 16 }}>
        <h2>Taken</h2>
        {taken.length > 0 ? (
          <Table rows={taken} onOpen={onOpen} />
        ) : (
          <p className="note">None yet: no SentinelPolicy has let Sentinel act, or nothing called for it.</p>
        )}
      </section>
      <section className="card">
        <h2>Would have taken</h2>
        {shadow.length > 0 ? <Table rows={shadow} onOpen={onOpen} /> : <p className="note">None.</p>}
      </section>
    </article>
  );
}
