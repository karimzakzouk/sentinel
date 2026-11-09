// Types mirror the JSON the controller's API serves (internal/api, internal/model).

export interface ObjectRef {
  group?: string;
  kind: string;
  namespace?: string;
  name: string;
}

export interface RankedObject {
  object: ObjectRef;
  p: number;
  inSet: boolean;
}

export interface Culprit {
  changeId: string;
  kind: string;
  object: ObjectRef;
  fields?: string[];
  p: number;
}

export interface Assessment {
  class: string;
  classP: number;
  culprit?: Culprit;
  rootCauses?: RankedObject[];
  decision: string;
}

export interface Incident {
  id: string;
  fingerprint: string;
  state: string;
  title: string;
  openedAt: string;
  updatedAt: string;
  objects?: ObjectRef[];
  impacted?: ObjectRef[];
  signalIds?: string[];
  evidenceIds?: string[];
  mergedInto?: string;
  assessment?: Assessment;
}

export interface Feature {
  name: string;
  enabled: boolean;
  reason: string;
}

export interface Check {
  id: string;
  status: string;
  detail: string;
}

export interface Cluster {
  platform: string;
  serverVersion?: string;
  nodes?: number;
  networkPlugin?: string;
}

export interface Summary {
  version: string;
  mode: string;
  ledger: { headSeq: number; headHash: string; verified: { events: number; at: string } };
  incidents: { open: number; total: number };
  cluster?: Cluster;
  features?: Feature[];
}

export interface CapabilityReport {
  generatedAt: string;
  identity: string;
  cluster: Cluster;
  features: Feature[];
  checks: Check[];
  warnings?: string[];
}

export interface LedgerEvent {
  seq: number;
  time: string;
  type: string;
  data: unknown;
}

export interface IncidentDetail {
  incident: Incident;
  events: LedgerEvent[];
}

// Records found in an incident's events.

export interface LikelihoodRatio {
  factor: string;
  ratio: number;
}

export interface Signal {
  id: string;
  type: string;
  object: ObjectRef;
  observedAt: string;
  onset: string;
  tailProbability: number;
  attributes?: Record<string, string>;
}

export interface Hypothesis {
  faultClass: string;
  posterior: number;
  ratios?: LikelihoodRatio[];
}

export interface Action {
  id: string;
  incidentId: string;
  type: string;
  target: ObjectRef;
  params?: Record<string, string>;
  state: string;
  shadow?: boolean;
  reversible: boolean;
}

export interface Experiment {
  id: string;
  incidentId: string;
  purpose: string;
  target: ObjectRef;
  revision?: string;
  lab?: string;
  phase?: string;
  message?: string;
  runs?: number;
  fidelity?: string[];
}

export interface Fix {
  id: string;
  incidentId: string;
  target: ObjectRef;
  fromRevision?: string;
  phase: string;
  message?: string;
  owner?: string;
  resume?: string;
  repository?: string;
  change?: string[];
  pullRequest?: string;
  mergeCommit?: string;
  code?: {
    pullRequest?: string;
    function?: string;
    change?: string[];
    image?: string;
    verdict?: string;
    message?: string;
  };
}

export interface TrackRecord {
  key: { action: string; faultClass: string; service: string };
  successes: number;
  failures: number;
  lower: number;
  level: string;
}

export interface ActionSummary {
  action: Action;
  stage: string;
  since: string;
  at: string;
}

export interface TemplateCount {
  template: string;
  count: number;
  error?: boolean;
  sample: string;
}

export interface ExceptionCount {
  language: string;
  types: string[];
  frames?: { function: string; inApp?: boolean }[];
  fingerprint: string;
  count: number;
  sample: string;
}

export interface LogSummary {
  lines: number;
  records: number;
  templates?: TemplateCount[];
  exceptions?: ExceptionCount[];
  redactions?: Record<string, number>;
}

export interface Evidence {
  source: string;
  object?: ObjectRef;
  summary: string;
  sha256: string;
  collectedAt: string;
  data?: { reason?: string; container?: string; summary?: LogSummary };
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // The body was not JSON; the status says enough.
    }
    throw new Error(message);
  }
  return (await res.json()) as T;
}

// send makes a change through the console, as the signed-in person. The
// server takes changes only as JSON from this page.
async function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const text = (await res.text()).trim();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
}

export interface Me {
  user: string;
  groups?: string[];
}

export interface AutonomyRule {
  action: string;
  level: string;
  earned?: boolean;
}

export interface PolicySpec {
  autonomy?: AutonomyRule[];
  budgets?: { falseAlarmsPerWeek?: number; alphaPercent?: number; maxCapacityRemovalPercent?: number };
  quietPeriodAfterHumanEdit?: string;
}

export interface Policy {
  metadata: { name: string; namespace: string };
  spec: PolicySpec;
}

export interface NamespacePolicies {
  namespace: string;
  policies: Policy[];
  error?: string;
}

// whoami is who signed in to the console, null when nobody has, and
// undefined when this Sentinel serves no console.
async function whoami(): Promise<Me | null | undefined> {
  const res = await fetch("/api/session", { headers: { Accept: "application/json" }, credentials: "same-origin" });
  if (res.status === 401) return null;
  if (!res.ok) return undefined;
  return (await res.json()) as Me;
}

export const api = {
  whoami,
  signIn: (token: string) => send<Me>("POST", "/api/session", { token }),
  signOut: () => send<void>("DELETE", "/api/session"),
  decide: (request: string, decision: "approve" | "veto") => {
    const [ns = "", name = ""] = request.split("/");
    return send<{ done: string }>("POST", `/api/requests/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${decision}`, {});
  },
  resume: (kind: string, namespace: string, name: string) => send<{ done: string }>("POST", "/api/resume", { kind, namespace, name }),
  policies: () => send<NamespacePolicies[]>("GET", "/api/policies"),
  putPolicy: (namespace: string, name: string, spec: PolicySpec) =>
    send<{ done: string }>("PUT", `/api/policies/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`, spec),
  deletePolicy: (namespace: string, name: string) =>
    send<{ done: string }>("DELETE", `/api/policies/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`),
  summary: () => get<Summary>("/api/summary"),
  incidents: () => get<{ incidents: Incident[] | null }>("/api/incidents").then((r) => r.incidents ?? []),
  incident: (id: string) => get<IncidentDetail>(`/api/incidents/${encodeURIComponent(id)}`),
  capabilities: () => get<CapabilityReport>("/api/capabilities"),
  actions: () => get<{ actions: ActionSummary[] | null }>("/api/actions").then((r) => r.actions ?? []),
  trackRecords: () => get<{ trackRecords: TrackRecord[] | null }>("/api/trackrecords").then((r) => r.trackRecords ?? []),
  experiments: () => get<{ experiments: Experiment[] | null }>("/api/experiments").then((r) => r.experiments ?? []),
};
