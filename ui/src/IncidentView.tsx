import type { ReactNode } from "react";
import {
  api,
  type Action,
  type Evidence,
  type Hypothesis,
  type Incident,
  type LedgerEvent,
  type Signal,
} from "./api";
import { actionLabel, ago, classLabel, clock, duration, isOpen, pct, reading, workload } from "./format";
import { usePoll } from "./usePoll";

const signalLabels: Record<string, string> = {
  "pod.image-pull-error": "Image pull errors",
  "pod.crash-loop": "Crash loop",
  "pod.oom-killed": "OOM kills",
  "pod.restarted": "Restarts",
  "pod.not-ready": "Pods not ready",
  "pod.probe-failed": "Probe failures",
  "pod.config-error": "Container configuration errors",
  "pod.unschedulable": "Unschedulable pods",
  "pod.memory-near-limit": "Memory near its limit",
  "service.no-endpoints": "Service without ready endpoints",
  "log.new-error": "New errors in the logs",
};

function Bar({ label, p, dim, title }: { label: ReactNode; p: number; dim?: boolean; title?: string }) {
  return (
    <div className="bar-row" title={title}>
      <span className="label">{label}</span>
      <span className={`bar${dim ? " dim" : ""}`}>
        <span style={{ width: `${Math.max(2, Math.round(p * 100))}%` }} />
      </span>
      <span className="value">{pct(p)}</span>
    </div>
  );
}

function State({ incident }: { incident: Incident }) {
  if (incident.mergedInto) return <span className="pill">Merged</span>;
  return isOpen(incident) ? (
    <span className="pill red">
      <span className="dot" /> Open
    </span>
  ) : (
    <span className="pill green">
      <span className="dot" /> Resolved
    </span>
  );
}

// The latest assessment's hypotheses: the ones recorded after the last
// incident record that carried an assessment.
function latestHypotheses(events: LedgerEvent[]): Hypothesis[] {
  const out: Hypothesis[] = [];
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (!e) continue;
    if (e.type === "sentinel.hypothesis.proposed") out.unshift(e.data as Hypothesis);
    else if (out.length > 0) break;
  }
  return out;
}

function lastOf<T>(events: LedgerEvent[], type: string): T | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e?.type === type) return e.data as T;
  }
  return undefined;
}

function TimelineEvent({ event }: { event: LedgerEvent }) {
  const when = <div className="when">{clock(event.time)}</div>;
  switch (event.type) {
    case "sentinel.incident.opened":
      return (
        <li className="event">
          {when}
          <div className="what">Incident opened</div>
        </li>
      );
    case "sentinel.incident.merged":
      return (
        <li className="event">
          {when}
          <div className="what">Folded into the incident of its cause</div>
        </li>
      );
    case "sentinel.incident.resolved":
      return (
        <li className="event resolved">
          {when}
          <div className="what">Resolved: quiet for the whole incident gap</div>
        </li>
      );
    case "sentinel.signal.observed": {
      const s = event.data as Signal;
      return (
        <li className="event signal">
          {when}
          <div className="what">
            {signalLabels[s.type] ?? s.type} on {workload(s.object)}
          </div>
          <div className="detail">
            {s.attributes?.count ?? "?"} in the window, a {s.tailProbability.toExponential(1)} chance under its usual behavior
          </div>
        </li>
      );
    }
    case "sentinel.evidence.attached": {
      const ev = event.data as Evidence;
      const sum = ev.data?.summary;
      const redacted = Object.entries(sum?.redactions ?? {});
      return (
        <li className="event evidence">
          {when}
          <div className="what">Log evidence from {ev.object?.name}</div>
          <div className="detail">{ev.summary}</div>
          {sum && (
            <details>
              <summary>
                {sum.lines} lines, {sum.templates?.length ?? 0} templates, {sum.exceptions?.length ?? 0} exceptions
              </summary>
              <ul className="templates">
                {sum.exceptions?.map((x) => (
                  <li key={x.fingerprint} className="error">
                    <span className="count">×{x.count}</span>
                    <code>
                      {x.types.join(" → ")}
                      {x.frames?.some((f) => f.inApp) &&
                        "\n  at " +
                          x.frames
                            .filter((f) => f.inApp)
                            .slice(0, 3)
                            .map((f) => f.function)
                            .join("\n  at ")}
                    </code>
                  </li>
                ))}
                {sum.templates?.map((t) => (
                  <li key={t.template} className={t.error ? "error" : ""}>
                    <span className="count">×{t.count}</span>
                    <code>{t.template}</code>
                  </li>
                ))}
              </ul>
              {redacted.length > 0 && (
                <div className="redactions">
                  Removed before storing:{" "}
                  {redacted.map(([rule, n]) => `${rule} ×${n}`).join(", ")}
                </div>
              )}
            </details>
          )}
        </li>
      );
    }
    case "sentinel.action.proposed": {
      const a = event.data as Action;
      return (
        <li className="event action">
          {when}
          <div className="what">
            Would {actionLabel(a.type).toLowerCase()}: {workload(a.target)}{" "}
            {a.shadow && <span className="pill amber">recorded, not done</span>}
          </div>
          {a.params?.reason && <div className="detail">{a.params.reason}</div>}
        </li>
      );
    }
    default:
      return null;
  }
}

export function IncidentView({ id, onOpen }: { id: string; onOpen: (id: string) => void }) {
  const detail = usePoll(() => api.incident(id), [id]);
  if (detail.error && !detail.data) return <div className="error-banner">{detail.error}</div>;
  if (!detail.data) return <p className="empty">Loading…</p>;
  const { incident, events } = detail.data;
  const a = incident.assessment;
  const hypotheses = latestHypotheses(events);
  const action = lastOf<Action>(events, "sentinel.action.proposed");
  const factors = hypotheses[0]?.ratios ?? [];
  const symptoms = incident.objects?.slice(1) ?? [];

  return (
    <article>
      <div className="title-row">
        <h1>{classLabel(a?.class)}</h1>
        <State incident={incident} />
        <span className="chip mono">{workload(incident.objects?.[0])}</span>
      </div>
      <p className="subtitle">
        Opened {clock(incident.openedAt)} ({ago(incident.openedAt)}) · {isOpen(incident) ? "ongoing for" : "lasted"}{" "}
        {duration(incident.openedAt, isOpen(incident) ? new Date().toISOString() : incident.updatedAt)}
        {incident.mergedInto && (
          <>
            {" "}
            · merged into{" "}
            <a href={`#/incidents/${incident.mergedInto}`} onClick={() => onOpen(incident.mergedInto ?? "")}>
              its cause's incident
            </a>
          </>
        )}
      </p>

      <div className="grid">
        <section className="card">
          <h2>Sentinel's reading</h2>
          <p className="reading">{reading(incident)}</p>
        </section>
        <section className="card">
          <h2>What it would do</h2>
          {a ? (
            <>
              <div className="decision">
                {actionLabel(a.decision)}
                {a.decision !== "none" && <span className="pill amber">Observe mode: recorded, not done</span>}
              </div>
              {action?.params?.reason && <p className="note">{action.params.reason}</p>}
            </>
          ) : (
            <p className="note">Not assessed yet.</p>
          )}
        </section>
      </div>

      <div className="grid">
        <section className="card">
          <h2>Fault class</h2>
          {hypotheses.length > 0
            ? hypotheses.map((h) => <Bar key={h.faultClass} label={classLabel(h.faultClass)} p={h.posterior} />)
            : a && <Bar label={classLabel(a.class)} p={a.classP} />}
        </section>
        <section className="card">
          <h2>Root cause</h2>
          {a?.rootCauses?.length ? (
            a.rootCauses.map((r) => (
              <Bar
                key={workload(r.object)}
                label={<span className="mono">{workload(r.object)}</span>}
                p={r.p}
                dim={!r.inSet}
                title={r.inSet ? "In the 90% confidence set" : "Outside the confidence set"}
              />
            ))
          ) : (
            <p className="note">Only {workload(incident.objects?.[0])} was considered.</p>
          )}
        </section>
      </div>

      {(a?.culprit || factors.length > 0) && (
        <section className="card" style={{ marginBottom: 16 }}>
          <h2>Why this change</h2>
          {a?.culprit && (
            <p style={{ marginTop: 0 }}>
              The {a.culprit.kind} change to <span className="mono">{workload(a.culprit.object)}</span>
              {a.culprit.fields?.length ? (
                <>
                  {" "}
                  (<span className="mono">{a.culprit.fields.join(", ")}</span>)
                </>
              ) : null}{" "}
              is {pct(a.culprit.p)} likely to have caused this. Each factor multiplies its odds:
            </p>
          )}
          <ul className="factors">
            {factors.map((f) => (
              <li key={f.factor}>
                <span>{f.factor}</span>
                <span className={`times ${f.ratio >= 1 ? "up" : "down"}`}>
                  ×{f.ratio >= 10 ? f.ratio.toFixed(0) : f.ratio.toFixed(2)}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {(incident.impacted?.length || symptoms.length > 0) && (
        <section className="card" style={{ marginBottom: 16 }}>
          <h2>Impact</h2>
          {symptoms.length > 0 && (
            <p style={{ marginTop: 0 }}>
              Symptoms grouped here: {symptoms.map((o) => workload(o)).join(", ")}.
            </p>
          )}
          {incident.impacted?.length ? (
            <>
              <p className="note">Workloads that depend on it, directly or through others:</p>
              <div className="chips">
                {incident.impacted.map((o) => (
                  <span key={workload(o)} className="chip mono">
                    {workload(o)}
                  </span>
                ))}
              </div>
            </>
          ) : null}
        </section>
      )}

      <section className="card">
        <h2>Timeline</h2>
        <ol className="timeline">
          {events.map((e) => (
            <TimelineEvent key={e.seq} event={e} />
          ))}
        </ol>
      </section>
    </article>
  );
}
