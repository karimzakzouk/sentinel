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
  type: string;
  target: ObjectRef;
  params?: Record<string, string>;
  shadow?: boolean;
  reversible: boolean;
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

export const api = {
  summary: () => get<Summary>("/api/summary"),
  incidents: () => get<{ incidents: Incident[] | null }>("/api/incidents").then((r) => r.incidents ?? []),
  incident: (id: string) => get<IncidentDetail>(`/api/incidents/${encodeURIComponent(id)}`),
  capabilities: () => get<CapabilityReport>("/api/capabilities"),
};
