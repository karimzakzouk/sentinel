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
  "node.isolation": { label: "Node cannot run pods", meaning: "the pods are on a node that is not ready or is under memory, disk or process pressure" },
  "dns.failure": { label: "Names do not resolve", meaning: "the workload cannot resolve the names of what it calls" },
  "cert.expiry": { label: "Certificate expired", meaning: "a certificate the workload serves or checks has expired" },
  "dependency.failure": { label: "Connections refused", meaning: "something the workload calls refuses its connections, and no change of its own explains it" },
};

const actions: Record<string, { label: string; verb: string }> = {
  "rollout.undo": { label: "Roll back", verb: "roll back the rollout of" },
  "scale.restore": { label: "Restore replicas", verb: "restore the replica count of" },
  scale: { label: "Restore replicas", verb: "restore the replica count of" },
  restart: { label: "Restart pods", verb: "restart the pods of" },
  "argocd.rollback": { label: "Pause Argo CD, then roll back", verb: "pause Argo CD and roll back" },
  "flux.rollback": { label: "Suspend Flux, then roll back", verb: "suspend Flux and roll back" },
  "helm.rollback": { label: "Roll back a Helm release", verb: "roll back the Helm release of" },
  "rollouts.abort": { label: "Abort an Argo Rollout", verb: "abort the Argo Rollout of" },
  "flagger.abort": { label: "Abort a Flagger canary", verb: "abort the Flagger canary of" },
  "fix.merge": { label: "Merge its own fix in Git", verb: "merge the fix for" },
  escalate: { label: "Ask a person", verb: "ask a person to look at" },
  none: { label: "Do nothing", verb: "leave" },
};

export const classLabel = (c?: string) => (c && classes[c]?.label) || c || "Unclassified";
export const classMeaning = (c?: string) => (c && classes[c]?.meaning) || "";
export const actionLabel = (a?: string) => (a && actions[a]?.label) || a || "";
export const actionVerb = (a?: string) => (a && actions[a]?.verb) || a || "";

// What became of an action Sentinel asked the actor for, by Ledger event.
// What the Lab said about a proposed rollback, by Ledger event.
export const labStages: Record<string, { title: string; pill: string; tone: string }> = {
  "sentinel.experiment.requested": { title: "Asked the Lab to verify rolling back", pill: "in the Lab", tone: "accent" },
  "sentinel.experiment.passed": { title: "The Lab verified the rollback", pill: "fixes it", tone: "green" },
  "sentinel.experiment.failed": { title: "The Lab found the rollback does not help", pill: "does not fix it", tone: "red" },
  "sentinel.experiment.notsure": { title: "The Lab could not tell", pill: "not sure", tone: "amber" },
  "sentinel.experiment.aborted": { title: "The Lab stopped before a verdict", pill: "stopped", tone: "amber" },
};

// How the fix at the source went, by Ledger event.
export const fixStages: Record<string, { title: string; pill: string; tone: string }> = {
  "sentinel.fix.requested": { title: "Asked the Fixer to make the rollback hold in Git", pill: "in the Fixer", tone: "accent" },
  "sentinel.fix.proposed": { title: "The Fixer opened a pull request", pill: "fix proposed", tone: "accent" },
  "sentinel.fix.merged": { title: "Someone merged the fix", pill: "merged", tone: "accent" },
  "sentinel.fix.ready": { title: "The fix is in the owner's source", pill: "resume the owner", tone: "amber" },
  "sentinel.fix.applied": { title: "The owner is resumed and has applied the fix", pill: "applied, checking", tone: "accent" },
  "sentinel.fix.held": { title: "Fixed at the source", pill: "fixed", tone: "green" },
  "sentinel.fix.notheld": { title: "The fix did not hold", pill: "did not hold", tone: "red" },
  "sentinel.fix.abstained": { title: "The Fixer has no fix to propose", pill: "fix it by hand", tone: "amber" },
  "sentinel.fix.failed": { title: "The Fixer could not propose a fix", pill: "fix it by hand", tone: "red" },
  "sentinel.fix.declined": { title: "The fix was closed without merging", pill: "declined", tone: "amber" },
  "sentinel.fix.code": { title: "The fix to the image's code moved on", pill: "code", tone: "accent" },
};

// webLink is a URL safe to link to: http or https only.
export const webLink = (u?: string) => (u && /^https?:\/\//.test(u) ? u : undefined);

export const actionStages: Record<string, { title: string; pill: string; tone: string }> = {
  "sentinel.action.requested": { title: "Asked the actor to", pill: "requested", tone: "accent" },
  "sentinel.action.awaiting": { title: "Waiting for a person", pill: "needs approval", tone: "amber" },
  "sentinel.action.executed": { title: "The actor did it", pill: "done, verifying", tone: "accent" },
  "sentinel.action.failed": { title: "The actor did not act", pill: "not done", tone: "red" },
  "sentinel.action.verified": { title: "Verified", pill: "done and verified", tone: "green" },
  "sentinel.action.unverified": { title: "Not verified", pill: "left to people", tone: "red" },
  "sentinel.action.reverted": { title: "A person reverted it", pill: "reverted", tone: "red" },
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

const done: Record<string, string> = {
  "rollout.undo": "rolled back",
  "scale.restore": "restored the replica count of",
};

// What Sentinel would say about an incident, in a sentence or two; did names
// the event of the last step of an action Sentinel carried out, if any.
export function reading(i: Incident, did?: { type: string; event: string }): string {
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
  const target = workload(a.culprit?.object ?? i.objects?.[0]);
  const act = actions[a.decision];
  if (did && did.event !== "sentinel.action.requested" && did.event !== "sentinel.action.failed") {
    const outcome =
      did.event === "sentinel.action.verified"
        ? ", and the incident's signals cleared"
        : did.event === "sentinel.action.unverified"
          ? ", but the incident's signals did not clear; it is left to people"
          : "; it is checking that the incident clears";
    s += ` Sentinel ${done[did.type] ?? did.type} ${target}${outcome}.`;
  } else if (did?.event === "sentinel.action.failed") {
    s += ` Sentinel asked to ${act?.verb ?? did.type} ${target}, but the actor did not; it is left to people.`;
  } else if (did) {
    s += ` Sentinel has asked to ${act?.verb ?? did.type} ${target}.`;
  } else if (act && a.decision !== "none") {
    s += ` Sentinel would ${act.verb} ${a.decision === "escalate" ? "it" : target}.`;
  }
  return s;
}
