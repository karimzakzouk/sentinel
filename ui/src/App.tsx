import { useEffect, useState } from "react";
import { api, type Incident, type Summary } from "./api";
import { Actions } from "./Actions";
import { Capabilities } from "./Capabilities";
import { ago, classLabel, isOpen, workload } from "./format";
import { IncidentView } from "./IncidentView";
import { Policies } from "./Policies";
import { SignIn, useSession } from "./session";
import { usePoll } from "./usePoll";

function useRoute(): [string, (route: string) => void] {
  const [route, setRoute] = useState(() => location.hash.slice(1) || "/");
  useEffect(() => {
    const onChange = () => setRoute(location.hash.slice(1) || "/");
    addEventListener("hashchange", onChange);
    return () => removeEventListener("hashchange", onChange);
  }, []);
  return [route, (r) => (location.hash = r)];
}

export function Shield({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <path fill="var(--accent)" d="M16 2 4 6.5v8.6C4 22.6 9.1 28.6 16 30c6.9-1.4 12-7.4 12-14.9V6.5Z" />
      <path fill="none" stroke="#fff" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.6" d="m10.5 16.2 3.8 3.8 7.4-7.6" />
    </svg>
  );
}

function Header({ summary }: { summary?: Summary }) {
  const c = summary?.cluster;
  const open = summary?.incidents.open ?? 0;
  return (
    <header className="header">
      <div className="brand">
        <Shield />
        Sentinel <small>{summary?.version}</small>
      </div>
      {c && (
        <span className="pill" title="The cluster Sentinel watches">
          {c.platform} · {c.serverVersion} · {c.nodes} node{c.nodes === 1 ? "" : "s"}
        </span>
      )}
      <div className="spacer" />
      {summary?.mode === "assist" ? (
        <span className="pill amber" title="A SentinelPolicy lets Sentinel run reversible actions on its own; elsewhere it only records them">
          <span className="dot" /> Acting where a policy allows
        </span>
      ) : (
        <span className="pill accent" title="Sentinel records what it would do and changes nothing">
          <span className="dot" /> Observe mode
        </span>
      )}
      {summary && (
        <span
          className="pill green"
          title={`Every event's hash checked at start; head ${summary.ledger.headSeq} ${summary.ledger.headHash.slice(0, 16)}…`}
        >
          Ledger verified · {summary.ledger.headSeq.toLocaleString()} events
        </span>
      )}
      {summary && (
        <span className={`pill ${open > 0 ? "red" : "green"}`}>
          <span className="dot" />
          {open > 0 ? `${open} open` : "All clear"}
        </span>
      )}
      <Account />
    </header>
  );
}

// Account signs a person in to the console, or says who is.
function Account() {
  const { me, available, signOut } = useSession();
  const [asking, setAsking] = useState(false);
  if (!available) return null;
  return (
    <>
      {me ? (
        <span className="account">
          <span className="pill" title={me.groups?.join(", ")}>
            {me.user}
          </span>
          <button className="btn" onClick={() => void signOut()}>
            Sign out
          </button>
        </span>
      ) : (
        <button className="btn primary" onClick={() => setAsking(true)}>
          Sign in
        </button>
      )}
      {asking && <SignIn onClose={() => setAsking(false)} />}
    </>
  );
}

function IncidentItem({ incident, current, onSelect }: { incident: Incident; current: boolean; onSelect: () => void }) {
  const open = isOpen(incident);
  return (
    <button className="item" aria-current={current} onClick={onSelect}>
      <div className="title">
        <span className="dot" style={{ color: open ? "var(--red)" : "var(--green)" }} />
        {classLabel(incident.assessment?.class)}
      </div>
      <div className="meta">
        <span>{workload(incident.objects?.[0])}</span>
        <span>·</span>
        <span>{ago(incident.openedAt)}</span>
        {incident.objects && incident.objects.length > 1 && (
          <span>
            · {incident.objects.length - 1} {incident.objects.length === 2 ? "symptom" : "symptoms"} grouped
          </span>
        )}
      </div>
    </button>
  );
}

export function App() {
  const [route, go] = useRoute();
  const summary = usePoll(api.summary, []);
  const incidents = usePoll(api.incidents, []);
  const view = route.startsWith("/capabilities")
    ? "capabilities"
    : route.startsWith("/actions")
      ? "actions"
      : route.startsWith("/policies")
        ? "policies"
        : "incidents";
  const all = (incidents.data ?? []).filter((i) => !i.mergedInto);
  const open = all.filter(isOpen);
  const done = all.filter((i) => !isOpen(i));
  const selected = route.startsWith("/incidents/") ? route.slice("/incidents/".length) : (open[0] ?? done[0])?.id;

  return (
    <div className="app">
      <Header summary={summary.data} />
      <div className="body">
        <aside className="sidebar">
          <div className="tabs" role="tablist">
            <button className="tab" role="tab" aria-selected={view === "incidents"} onClick={() => go("/")}>
              Incidents
            </button>
            <button className="tab" role="tab" aria-selected={view === "actions"} onClick={() => go("/actions")}>
              Actions
            </button>
            <button className="tab" role="tab" aria-selected={view === "policies"} onClick={() => go("/policies")}>
              Policies
            </button>
            <button className="tab" role="tab" aria-selected={view === "capabilities"} onClick={() => go("/capabilities")}>
              Capabilities
            </button>
          </div>
          {(summary.error || incidents.error) && (
            <div className="error-banner">Cannot reach Sentinel: {summary.error ?? incidents.error}</div>
          )}
          {view === "incidents" && (
            <nav className="list" aria-label="Incidents">
              {open.length > 0 && <h3>Open</h3>}
              {open.map((i) => (
                <IncidentItem key={i.id} incident={i} current={i.id === selected} onSelect={() => go(`/incidents/${i.id}`)} />
              ))}
              {done.length > 0 && <h3>Resolved</h3>}
              {done.map((i) => (
                <IncidentItem key={i.id} incident={i} current={i.id === selected} onSelect={() => go(`/incidents/${i.id}`)} />
              ))}
              {incidents.data && all.length === 0 && <p className="empty">No incidents yet.</p>}
            </nav>
          )}
        </aside>
        <main className="main">
          {view === "capabilities" ? (
            <Capabilities />
          ) : view === "policies" ? (
            <Policies />
          ) : view === "actions" ? (
            <Actions onOpen={(id) => go(`/incidents/${id}`)} />
          ) : selected ? (
            <IncidentView id={selected} onOpen={(id) => go(`/incidents/${id}`)} />
          ) : !incidents.data ? (
            <p className="empty">{incidents.error ? "" : "Loading…"}</p>
          ) : (
            <div className="empty">
              <h2>Nothing to explain</h2>
              <p>
                Sentinel is watching. When something goes wrong it opens an incident here, explains what it sees, and records
                what it would do.
              </p>
            </div>
          )}
        </main>
      </div>
    </div>
  );
}
