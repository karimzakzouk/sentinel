import type { Assessment, Incident, ObjectRef } from "./api";

// Plain-language names for the Brain's fault classes and actions.
const classes: Record<string, { label: string; meaning: string }> = {
  "deploy.bad-image": { label: "Bad image", meaning: "a deploy points at an image that cannot be pulled" },
  "deploy.crash-on-start": { label: "Crash on start", meaning: "a deploy makes the container exit as it starts" },
  "deploy.new-errors": { label: "New errors after a deploy", meaning: "a deploy's pods stay up but log errors the old ones did not" },
  "resource.memory-limit": { label: "Memory limit too low", meaning: "the container needs more memory than its limit allows" },
  "config.probe": { label: "Broken health check", meaning: "a probe marks healthy pods as failing" },
  "config.error": { label: "Configuration error", meaning: "the container cannot be created from its configuration" },
  "capacity.unschedulable": { label: "Cannot schedule", meaning: "no node can take the pods" },
  "runtime.crash": { label: "Crashing", meaning: "the containers keep crashing without a change that explains it" },
  "dependency.unavailable": { label: "Dependency unavailable", meaning: "a workload others depend on has no ready endpoints" },
  "runtime.errors": { label: "Errors without a change", meaning: "the pods stay up but log errors at an unusual rate, and no change explains it" },
};

const actions: Record<string, { label: string; verb: string }> = {
  "rollout.undo": { label: "Roll back", verb: "roll back the rollout of" },
  "scale.restore": { label: "Restore replicas", verb: "restore the replica count of" },
  escalate: { label: "Ask a person", verb: "ask a person to look at" },
  none: { label: "Do nothing", verb: "leave" },
};

export const classLabel = (c?: string) => (c && classes[c]?.label) || c || "Unclassified";
export const classMeaning = (c?: string) => (c && classes[c]?.meaning) || "";
export const actionLabel = (a?: string) => (a && actions[a]?.label) || a || "";
export const actionVerb = (a?: string) => (a && actions[a]?.verb) || a || "";

// What became of an action Sentinel asked the actor for, by Ledger event.
export const actionStages: Record<string, { title: string; pill: string; tone: string }> = {
  "sentinel.action.requested": { title: "Asked the actor to", pill: "requested", tone: "accent" },
  "sentinel.action.executed": { title: "The actor did it", pill: "done, verifying", tone: "accent" },
  "sentinel.action.failed": { title: "The actor did not act", pill: "not done", tone: "red" },
  "sentinel.action.verified": { title: "Verified", pill: "done and verified", tone: "green" },
  "sentinel.action.unverified": { title: "Not verified", pill: "left to people", tone: "red" },
};

export const workload = (o?: ObjectRef) => (o ? (o.namespace ? `${o.namespace}/${o.name}` : o.name) : "");

// pct never rounds a probability to certainty in either direction.
export function pct(p: number): string {
  if (p >= 0.995) return ">99%";
  if (p > 0 && p < 0.005) return "<1%";
  return `${Math.round(p * 100)}%`;
}

export function isOpen(i: Incident): boolean {
  return i.state !== "resolved" && i.state !== "closed";
}

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}

export function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function duration(from: string, to: string): string {
  const s = Math.max(0, Math.round((Date.parse(to) - Date.parse(from)) / 1000));
  if (s < 90) return `${s}s`;
  const m = Math.round(s / 60);
  return m < 90 ? `${m} min` : `${Math.round(m / 60)} h`;
}

// What Sentinel would say about an incident, in a sentence or two.
export function reading(i: Incident): string {
  const a: Assessment | undefined = i.assessment;
  const where = workload(i.objects?.[0]);
  if (!a) return `Sentinel saw trouble on ${where} and is still assessing it.`;
  let s = `${classLabel(a.class)} on ${where}: ${classMeaning(a.class)} (${pct(a.classP)} likely).`.replace(">99% likely", "almost certainly");
  if (a.culprit) {
    const fields = a.culprit.fields?.length ? ` (${a.culprit.fields.join(", ")})` : "";
    s += ` The likely cause is the ${a.culprit.kind} change to ${workload(a.culprit.object)}${fields}, ${pct(a.culprit.p)} likely.`;
  }
  const top = a.rootCauses?.[0];
  if (top && workload(top.object) !== where) {
    s += ` The root cause looks upstream, in ${workload(top.object)} (${pct(top.p)}).`;
  }
  const act = actions[a.decision];
  if (act && a.decision !== "none") {
    s += ` Sentinel would ${act.verb} ${a.decision === "escalate" ? "it" : workload(a.culprit?.object ?? i.objects?.[0])}.`;
  }
  return s;
}
