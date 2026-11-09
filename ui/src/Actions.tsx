import { api, type ActionSummary, type TrackRecord } from "./api";
import { actionLabel, actionStages, ago, classLabel, clock, pct, workload } from "./format";
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

// Records shows each cell's track record: how often an action cleared a kind
// of incident on a workload, and the autonomy that record supports.
function Records({ records }: { records: TrackRecord[] }) {
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Action</th>
          <th>Fault class</th>
          <th>Workload</th>
          <th title="Verified: the incident's signals cleared">Cleared</th>
          <th title="The action ran but the incident did not clear">Did not</th>
          <th title="5th percentile of the success rate, from a uniform prior">At least</th>
          <th>Supports</th>
        </tr>
      </thead>
      <tbody>
        {records.map((r) => (
          <tr key={`${r.key.action}|${r.key.faultClass}|${r.key.service}`}>
            <td>{actionLabel(r.key.action)}</td>
            <td>{classLabel(r.key.faultClass)}</td>
            <td className="mono">{r.key.service.replace(/^[^/]*\//, "")}</td>
            <td>{r.successes}</td>
            <td>{r.failures}</td>
            <td>{pct(r.lower)}</td>
            <td>
              <span className={`pill ${r.level === "autonomous" ? "green" : ""}`}>{r.level}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Actions is the audit view: what Sentinel did where a policy let it, and
// what it would have done elsewhere.
export function Actions({ onOpen }: { onOpen: (id: string) => void }) {
  const actions = usePoll(api.actions, []);
  const records = usePoll(api.trackRecords, []);
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
      <section className="card" style={{ marginBottom: 16 }}>
        <h2>Track records</h2>
        {records.data && records.data.length > 0 ? (
          <>
            <p className="note">
              Each verified action counts for its cell. A cell supports acting on its own once the success rate is at least
              90% with 95% confidence: 28 straight successes from a uniform prior. For now this is shown, not enforced.
            </p>
            <Records records={records.data} />
          </>
        ) : (
          <p className="note">No verified actions yet.</p>
        )}
      </section>
      <section className="card">
        <h2>Would have taken</h2>
        {shadow.length > 0 ? <Table rows={shadow} onOpen={onOpen} /> : <p className="note">None.</p>}
      </section>
    </article>
  );
}
