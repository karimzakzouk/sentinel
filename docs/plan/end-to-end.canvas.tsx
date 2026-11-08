import {
  BarChart,
  Callout,
  Card,
  CardBody,
  CardHeader,
  CollapsibleSection,
  Divider,
  Grid,
  H1,
  H2,
  H3,
  Link,
  Pill,
  Row,
  Stack,
  Swatch,
  Table,
  Text,
  computeDAGLayout,
  useCanvasState,
  useHostTheme,
  useMemo,
} from "cursor/canvas";
import type { TableRowTone } from "cursor/canvas";

/* ------------------------------ helpers ------------------------------ */

const c = (s: string) => <Text size="small">{s}</Text>;
const cs = (s: string) => (
  <Text size="small" tone="secondary">
    {s}
  </Text>
);
const cb = (s: string) => (
  <Text size="small" weight="semibold">
    {s}
  </Text>
);

function Bullets({ items }: { items: string[] }) {
  const theme = useHostTheme();
  return (
    <Stack gap={6}>
      {items.map((s, i) => (
        <Row key={i} gap={8} align="start">
          <span style={{ color: theme.text.tertiary, minWidth: 10, lineHeight: "20px" }}>•</span>
          <Text style={{ flex: 1, minWidth: 0 }}>{s}</Text>
        </Row>
      ))}
    </Stack>
  );
}

function Numbered({ items }: { items: string[] }) {
  const theme = useHostTheme();
  return (
    <Stack gap={6}>
      {items.map((s, i) => (
        <Row key={i} gap={8} align="start">
          <span style={{ color: theme.text.tertiary, minWidth: 18, lineHeight: "20px" }}>{i + 1}.</span>
          <Text style={{ flex: 1, minWidth: 0 }}>{s}</Text>
        </Row>
      ))}
    </Stack>
  );
}

function Pre({ text }: { text: string }) {
  const theme = useHostTheme();
  return (
    <pre
      style={{
        margin: 0,
        padding: 12,
        borderRadius: 6,
        background: theme.fill.quaternary,
        border: `1px solid ${theme.stroke.tertiary}`,
        color: theme.text.secondary,
        fontSize: 12,
        lineHeight: "18px",
        overflowX: "auto",
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      }}
    >
      {text}
    </pre>
  );
}

type Row4 = [string, string, string, string];
type Row3 = [string, string, string];

function T3({ headers, rows, tones }: { headers: string[]; rows: Row3[]; tones?: (TableRowTone | undefined)[] }) {
  return <Table headers={headers} rowTone={tones} rows={rows.map(([a, b, d]) => [cb(a), c(b), cs(d)])} />;
}
function T4({ headers, rows, tones }: { headers: string[]; rows: Row4[]; tones?: (TableRowTone | undefined)[] }) {
  return <Table headers={headers} rowTone={tones} rows={rows.map(([a, b, d, e]) => [cb(a), c(b), c(d), cs(e)])} />;
}

const TABS = [
  ["overview", "Overview"],
  ["lifecycle", "Lifecycle"],
  ["incidents", "Incidents"],
  ["watcher", "Watcher"],
  ["brain", "Brain"],
  ["lab", "Lab"],
  ["fixer", "Fixer"],
  ["platform", "Platform"],
  ["eval", "Evaluation"],
  ["plan", "Plan & team"],
  ["market", "Market"],
  ["decisions", "Decisions"],
  ["sources", "Sources"],
] as const;

export default function SentinelComplete() {
  const [tab, setTab] = useCanvasState<string>("tab", "overview");
  return (
    <Stack gap={18} style={{ padding: 4, maxWidth: 1120 }}>
      <Stack gap={6}>
        <H1>Sentinel: the complete project, end to end</H1>
        <Text tone="secondary">
          Researched from scratch on November 8, 2026: incident studies and postmortems, the state of the art for each module, Sentinel's own security, evaluation methods, the market, and your supervisor's ideation guide. Every choice below lists its alternatives and what would make us change it. Nothing here is treated as gospel, including this document.
        </Text>
      </Stack>
      <Row gap={6} wrap>
        {TABS.map(([id, label]) => (
          <Pill key={id} active={tab === id} onClick={() => setTab(id)}>
            {label}
          </Pill>
        ))}
      </Row>
      <Divider />
      {tab === "overview" && <Overview />}
      {tab === "lifecycle" && <Lifecycle />}
      {tab === "incidents" && <Incidents />}
      {tab === "watcher" && <Watcher />}
      {tab === "brain" && <Brain />}
      {tab === "lab" && <Lab />}
      {tab === "fixer" && <Fixer />}
      {tab === "platform" && <Platform />}
      {tab === "eval" && <Evaluation />}
      {tab === "plan" && <Plan />}
      {tab === "market" && <Market />}
      {tab === "decisions" && <Decisions />}
      {tab === "sources" && <Sources />}
    </Stack>
  );
}

/* ------------------------------ Overview ------------------------------ */

function ArchitectureDiagram() {
  const theme = useHostTheme();
  const W = 178;
  const H = 54;
  type Effort = "green" | "yellow" | "red";
  const nodes: Record<string, [string, string, Effort]> = {
    cluster: ["Your cluster", "workloads, Argo / Flux / Helm", "red"],
    obi: ["eBPF capture", "OBI + our exporter", "yellow"],
    watcher: ["Watcher", "collect, detect, group", "green"],
    brain: ["Brain", "localize, decide, verify", "green"],
    faker: ["Record & replay", "fakes, seeded databases", "green"],
    llm: ["LLM (optional)", "candidate patches only", "red"],
    actions: ["Owner adapters", "action contracts + fence", "green"],
    fixer: ["Fixer", "localize, repair, build", "green"],
    ledger: ["Ledger & UI", "audit log, timeline", "green"],
    lab: ["Lab", "seal, reproduce, verdict", "green"],
    git: ["Git host & CI", "PRs, checks, merges", "red"],
  };
  const layout = useMemo(
    () =>
      computeDAGLayout({
        nodes: Object.keys(nodes).map((id) => ({ id })),
        edges: [
          { from: "cluster", to: "watcher" },
          { from: "obi", to: "watcher" },
          { from: "watcher", to: "brain" },
          { from: "watcher", to: "faker" },
          { from: "brain", to: "actions" },
          { from: "brain", to: "fixer" },
          { from: "brain", to: "ledger" },
          { from: "llm", to: "fixer" },
          { from: "faker", to: "lab" },
          { from: "fixer", to: "lab" },
          { from: "lab", to: "git" },
        ],
        direction: "horizontal",
        nodeWidth: W,
        nodeHeight: H,
        rankGap: 36,
        nodeGap: 14,
        padding: 8,
      }),
    [],
  );
  return (
    <svg width="100%" viewBox={`0 0 ${layout.width} ${layout.height}`} style={{ display: "block", maxWidth: layout.width }}>
      <defs>
        <marker id="sc-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
          <path d="M0,0 L8,4 L0,8 z" fill={theme.text.tertiary} />
        </marker>
      </defs>
      {layout.edges.map((e, i) => {
        const mx = (e.sourceX + e.targetX) / 2;
        return (
          <path
            key={i}
            d={`M${e.sourceX},${e.sourceY} C${mx},${e.sourceY} ${mx},${e.targetY} ${e.targetX - 2},${e.targetY}`}
            stroke={theme.stroke.primary}
            strokeWidth={1.2}
            fill="none"
            markerEnd="url(#sc-arrow)"
          />
        );
      })}
      {layout.nodes.map((n) => {
        const [title, sub, effort] = nodes[n.id];
        return (
          <g key={n.id}>
            <rect x={n.x} y={n.y} width={W} height={H} rx={6} fill={theme.fill.quaternary} stroke={theme.category[effort]} strokeWidth={2} />
            <text x={n.x + W / 2} y={n.y + 22} textAnchor="middle" fontSize={12} fontWeight={600} fill={theme.text.primary}>
              {title}
            </text>
            <text x={n.x + W / 2} y={n.y + 39} textAnchor="middle" fontSize={10.5} fill={theme.text.secondary}>
              {sub}
            </text>
          </g>
        );
      })}
    </svg>
  );
}

function Overview() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>What Sentinel is</H2>
        <Text>
          Sentinel is an open-source (Apache-2.0), self-hosted autonomous SRE for Kubernetes. One Helm install on any cluster; it detects what the cluster allows and reports what it can't do. It watches workloads, notices real incidents, and stops the bleeding with reversible actions executed through whatever owns the workload (Argo CD, Flux, Helm or plain Kubernetes), verifying each action against user-facing signals and undoing it if it doesn't help. It then localizes the cause and produces config or code fixes, each reproduced and judged in a sealed Lab with stated error bounds, delivered as pull requests or auto-merged for fix classes that have earned it. Every incident leaves a regression test and updates track records, so Sentinel earns autonomy gradually. Everything is explained, audited and safe by construction. Humans are involved only when something truly needs a human.
        </Text>
      </Stack>

      <Callout tone="info" title="Five guarantees: the thesis you defend">
        <Stack gap={4}>
          <Text>1. Every automatic action is reversible (or made reversible first), checked against its contract and an API-server policy fence, verified on user-facing signals, and undone if it doesn't help.</Text>
          <Text>2. No LLM output can trigger a cluster write. The LLM only drafts candidate patches, and the Lab judges them.</Text>
          <Text>3. Every proposed fix was reproduced and judged in a sealed Lab with stated error bounds. "Not sure" never becomes a PR.</Text>
          <Text>4. Sentinel fails closed. A missing prerequisite disables a feature with a stated reason; it never weakens safety.</Text>
          <Text>5. Every decision is explained with its evidence and recorded in a tamper-evident log.</Text>
        </Stack>
      </Callout>

      <Stack gap={8}>
        <H2>Architecture and effort colors</H2>
        <ArchitectureDiagram />
        <Row gap={16} wrap>
          <Row gap={6} align="center"><Swatch color="green" style={{ width: 14, height: 14 }} /><Text size="small">Built by the team</Text></Row>
          <Row gap={6} align="center"><Swatch color="yellow" style={{ width: 14, height: 14 }} /><Text size="small">Partly built (we extend an existing tool)</Text></Row>
          <Row gap={6} align="center"><Swatch color="red" style={{ width: 14, height: 14 }} /><Text size="small">Already made (commodity, behind an interface)</Text></Row>
        </Row>
        <Text size="small" tone="tertiary">
          Color coding follows Dr. Youssef's guide. Every red or yellow box sits behind an interface we own, so it can be swapped and measured. The Ledger records every step from every module; only the main edges are drawn.
        </Text>
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Design principles</H3>
          <Bullets
            items={[
              "Adaptive, not hard-coded. Thresholds are learned from each service's history and its sibling replicas, calibrated by A/A runs, or expressed as risk budgets you choose (false alarms per service per week, error level α, capacity Sentinel may remove). No magic numbers like '20 runs' or '3 services in 60 seconds'.",
              "Evidence, not assertion. Every hypothesis carries readable likelihood ratios; every verdict carries an interval.",
              "Reversible by default, fail closed, least privilege.",
              "The core is ours; commodity edges are swappable and measured against our own versions.",
              "Grow by data. A new incident pattern, fix template or policy is data plus a test scenario, not new code.",
              "Nothing is gospel. Each major decision lists what evidence would make us change it (Decisions tab).",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Measurable claims, pre-registered with the supervisor</H3>
          <Bullets
            items={[
              "Zero isolation breaches across every Lab run, including a red-team exfiltration attempt.",
              "The Lab's observed false-PASS rate on labeled good, bad and deletion-style patches stays at or below the configured α.",
              "On a blind pool of 80–100 injected incidents plus 30+ no-fault controls, a statistically significant paired improvement over no-op, scripted and Robusta-style baselines, with zero wrong actions in 60+ opportunities (which bounds the rate below 5%).",
              "LLM-off results reported first; the LLM is measured as an increment.",
              "A correct capability report on at least four cluster types.",
              "No API-server SLO violation attributable to Sentinel up to the tested scale, with published scaling curves.",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="neutral" title="What it is not">
        Not a monitoring platform (it stores incidents and evidence, not all telemetry), not a chatbot, not an LLM agent with cluster credentials, and never an operator of irreversible changes without a human approval.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Lifecycle ------------------------------ */

function Lifecycle() {
  return (
    <Stack gap={22}>
      <H2>The full lifecycle</H2>
      <Stack gap={10}>
        <CollapsibleSection title="1. Install (day 0)" defaultOpen>
          <Bullets
            items={[
              "Three Helm charts: sentinel-crds (CRDs are upgraded separately because Helm never upgrades crds/), sentinel (controller, optional node agent), and sentinel-fence (admin-owned API-server policies that constrain Sentinel itself).",
              "Capability detection runs at install and whenever nodes change: API discovery (metrics.k8s.io, OpenShift, Cilium, Prometheus Operator), one SelfSubjectAccessReview per needed verb, node facts (Fargate label, kernel, runtime), dry-run Pod probes for each tier, NetworkPolicy enforcement, user namespaces, sandbox RuntimeClasses, owners present (Argo CD, Flux, Helm, Argo Rollouts, Flagger), and a registry the nodes can pull from.",
              "Output: a capability report and a per-node coverage map. A dry-run blocked by a webhook is reported as 'unknown', not 'denied'.",
              "Sentinel starts in observe (shadow) mode: read-only, Lab, Fixer and LLM off. It records what it would have done, so the team can see its precision before granting any write.",
            ]}
          />
        </CollapsibleSection>
        <CollapsibleSection title="2. Configure" defaultOpen>
          <Bullets
            items={[
              "Namespace owners opt in with a SentinelPolicy resource: autonomy level per action class (observe, recommend, approve, act unless vetoed, autonomous), risk budgets, tier-0 marks, and quiet periods after human edits.",
              "Connect a GitHub App (tokens expire hourly and are scoped per repo), notifications (Slack, GitHub), and optionally an LLM endpoint (local or API, OpenAI-compatible).",
              "Enable the Lab only where the NetworkPolicy probe passes; otherwise it stays off with the reason shown.",
            ]}
          />
        </CollapsibleSection>
        <CollapsibleSection title="3. Learn (first hours and days)">
          <Bullets
            items={[
              "Baselines start immediately by comparing each pod with its sibling replicas. Detection waits until the model's uncertainty is small enough, not for a fixed warm-up period.",
              "The dependency map fills in from config, logs, and eBPF or traces where available. A change log begins: pod templates, images, ConfigMaps (Sentinel keeps redacted snapshots, because Kubernetes keeps no ConfigMap history), Secrets (hash only), HPAs, and Argo/Flux revisions.",
              "The shadow report shows would-have-done actions, so precision can be measured before anything is allowed to act.",
            ]}
          />
        </CollapsibleSection>
        <CollapsibleSection title="4. The incident loop, step by step" defaultOpen>
          <Numbered
            items={[
              "Detect. Per-entity detectors flag anomalies as calibrated tail probabilities. False discoveries are controlled across series, persistence is checked, and a 'went away' check filters transients. Kubernetes facts (OOMKilled, CrashLoopBackOff, FailedScheduling) are scored against each workload's own count baseline.",
              "Group. An onset-time posterior for each signal; signals join one incident when their onsets fit a learned lag pattern and their services are close in the dependency map.",
              "Localize. Robust deviation scores retrieve the top suspects; reranking on the dependency map picks the deepest anomalous node and reports a confidence set, not one guess.",
              "Ask 'what changed?'. Candidate changes within two hops are scored with likelihood ratios: type prior, onset gap, share of failing pods on the new revision, and a new-vs-old difference-in-differences.",
              "Decide. Minimize expected loss over allowed actions, plus 'do nothing' and 'escalate'. When more evidence is worth the wait, Sentinel waits or asks the Lab first.",
              "Check. CEL policies at planning and again before execution; the action contract's preconditions; the API-server fence; a server-side dry run.",
              "Act through the owner. Argo CD: pause auto-sync, roll back by history, open a revert PR, re-enable after merge. Flux: suspend, roll back, revert, resume. Helm: SDK rollback. Plain: rollout undo or snapshot restore. Rollouts: abort.",
              "Verify. User-facing signals of the target and its callers, compared against untouched pods where possible, with anytime-valid tests. Commit only if nothing regressed and the main signal improved; otherwise undo and escalate.",
              "Communicate. One incident, one thread. Page only on fast error-budget burn when no safe action exists, verification failed, or approval is needed.",
              "Fix. Mitigated is not fixed. The Fixer resolves the source, localizes, generates candidates; the Lab reproduces and judges; a PR opens, or auto-merges for a graduated class, followed by a canary rollout (5%, 25%, 100%).",
              "Close and learn. Track records update, the regression test lands with the fix, the incident is sealed in the tamper-evident log, and human edits to Sentinel's PRs feed pattern mining.",
            ]}
          />
        </CollapsibleSection>
        <CollapsibleSection title="5. Autonomy over weeks">
          <Bullets
            items={[
              "Each cell (action × fault class × service) keeps a Beta success record, pooled across similar services and decayed over time.",
              "A cell moves up one level when the 5th percentile of its success rate clears the target and its harm-free runs satisfy the rule of three; any harm demotes it and opens its circuit breaker.",
              "Under a uniform prior that takes about 28 straight successes for 90% and 58 for 95%. Many cells will stay at 'approve' for a long time. That's intended.",
              "Game days keep humans practiced, because automation makes manual skills fade (Bainbridge's 'ironies of automation').",
            ]}
          />
        </CollapsibleSection>
        <CollapsibleSection title="6. Upgrade, uninstall, and life after graduation">
          <Bullets
            items={[
              "Upgrades use CRD conversion and expand/contract migrations with a backup taken before the upgrade, so a Helm rollback works.",
              "Uninstall runs a pre-delete hook that stops actions. Lab namespaces are owned by Experiment objects and garbage-collected. The Ledger volume is kept.",
              "After graduation: Apache-2.0, DCO sign-offs, governance files, an external maintainer, named adopters, then the CNCF Sandbox application.",
            ]}
          />
        </CollapsibleSection>
      </Stack>

      <Stack gap={10}>
        <H2>Six incidents, start to finish</H2>
        <T3
          headers={["Incident", "What Sentinel does", "Humans see"]}
          rows={[
            ["Bad deploy through Argo CD", "A new exception fingerprint appears only on the new ReplicaSet (rate-ratio test). Attribution strongly favors that deploy. Sentinel pauses auto-sync, rolls back by history ID, verifies on callers' error rate, and opens a revert PR. The Fixer maps the stack trace onto the deploy diff, tries a 'handle timeout with fallback' template, passes the build-parity check, and the Lab's paired test passes. A fix PR opens; auto-sync resumes after the merge.", "One thread, a revert PR and a fix PR with evidence."],
            ["Bad ConfigMap value (no new revision)", "Pods started after the change fail and older pods don't; that gap is the evidence. Sentinel restores its own snapshot (a compensable action, so only with a snapshot) and restarts the affected pods. The Fixer finds the source file (Helm values layer or Kustomize overlay) and opens a PR restoring the last known-good field, checked by re-rendering every overlay.", "A restored service and a config PR."],
            ["Memory leak", "OOM kills are anomalous against the workload's own baseline, and memory is climbing rather than flat at the limit, so it's a leak, not a too-small limit. A rolling restart goes through the Eviction API so disruption budgets apply. The Lab reproduces the growth under replayed load (memory slope); a template or LLM candidate must flatten the slope.", "Restart notice, later a PR (human review)."],
            ["DNS or dependency cascade", "Dozens of services error at once. Grouping makes it one incident, and reranking points at the shared cause (a CoreDNS pseudo-node). Sentinel scales CoreDNS only if policy explicitly allows touching kube-system; otherwise it escalates. It never restarts the symptom services.", "One incident, not fifty alerts."],
            ["Bad migration or data corruption", "Detected and diagnosed, never acted on automatically. Sentinel prepares a plan: stop writers, take a backup, rehearse the repair on a database clone in the Lab, then present it for one-click approval.", "An approval request with a tested plan."],
            ["Noise", "A cron spike, a rollout blip or a single restart falls within its learned baselines or self-recovers, so it's suppressed and listed in the digest.", "Nothing, except a line in the daily digest."],
          ]}
        />
      </Stack>
    </Stack>
  );
}

/* ------------------------------ Incidents ------------------------------ */

type Inc = [string, string, string, string];
const INCIDENTS: [string, Inc[]][] = [
  ["Change-induced", [
    ["Bad app rollout", "Roll back through the owner when attribution is strong, then a fix PR", "Yes", "Yes, unless a migration ran"],
    ["Bad config or flag push", "Revert through the owner or restore Sentinel's snapshot; PR to the config source", "Yes if Git-owned; partly for runtime flags", "Yes"],
    ["Upgrade or version skew", "Pause the rollout, cordon new nodes, escalate", "Partly", "Partly"],
    ["Destructive human action", "Detect mass deletions, prepare a restore plan from Git or backup", "No", "Partly"],
  ]],
  ["Resource and capacity", [
    ["Memory limit too low or leak", "Raise within headroom if memory is flat at the limit; rolling restart if it's climbing; right-sizing or fix PR", "Yes", "Yes"],
    ["CPU throttling", "Raise or remove the limit, or scale out; measure throttling's effect on latency per workload", "Yes", "Yes"],
    ["Node pressure or NotReady", "Cordon; rate-limited drain; escalate node replacement", "Partly", "Partly"],
    ["Load spike", "Scale out within budget", "Yes", "Yes"],
    ["Autoscaler misbehavior", "Pin minimum replicas through the owner; never fight the HPA", "Partly", "Yes"],
    ["Quotas and provider limits", "Diagnose and escalate (billing and policy)", "No", "n/a"],
  ]],
  ["Dependency and overload", [
    ["Downstream dependency failure", "Act on the dependency if it's in the cluster; only pre-declared fallbacks", "Partly", "Yes"],
    ["Retry storm or metastable failure", "Detect (load persists after the trigger is gone); shed only via pre-declared switches; escalate", "Partly", "Yes"],
    ["Probe-induced restart cascade", "Propose relaxed probes as a PR; mitigate only by policy", "Partly", "Yes"],
    ["Gray failure or noisy neighbor", "Peer comparison; cordon one node at a time", "Partly", "Yes"],
  ]],
  ["Network and DNS", [
    ["Cluster DNS failure", "Scale CoreDNS if policy allows touching kube-system, else escalate", "Partly", "Yes"],
    ["CNI, conntrack, SNAT, MTU", "Cordon affected nodes; escalate", "Partly", "Partly"],
    ["Service or Ingress routing", "Revert the manifest through the owner", "Yes", "Yes"],
    ["Access or policy change", "Revert only if it doesn't widen access; otherwise ask for approval", "Partly", "Yes"],
  ]],
  ["Control plane and platform", [
    ["Admission webhook outage", "Escalate with a prepared plan (changing failurePolicy bypasses a control)", "Partly (approval)", "Yes"],
    ["API server or etcd overload", "Identify the abusive client, propose flow-control (APF) config, escalate", "Partly", "Partly"],
    ["Certificate or credential expiry", "Forecast days to expiry; trigger cert-manager renewal if present", "Partly", "Partly"],
    ["Operator bug or field fight", "Detect managedFields churn; suspending reconciliation needs approval", "Partly", "Yes"],
    ["Scheduling constraints", "Diagnose; relax through the owner as a PR", "Partly", "Yes"],
    ["Image or registry pull failure", "Roll back to the last pullable digest", "Yes", "Yes"],
    ["Storage or volume", "Diagnose; expand only with approval (volumes can't shrink)", "Partly", "Partly"],
  ]],
  ["Code and data", [
    ["Latent code bug, error handling", "Mitigate (restart, or roll back if recent); fix as a Lab-verified PR", "Partly; fix via PR", "Yes (mitigation)"],
    ["Data corruption or bad migration", "Never automatic: plan, backup, clone rehearsal, approval", "No", "No"],
    ["Job or queue backlog", "Suspend a CronJob or scale consumers only if pre-declared", "Partly", "Yes"],
  ]],
  ["External and security", [
    ["Zone or provider failure", "Shift capacity in-cluster within policy; escalate", "Partly", "Partly"],
    ["Abusive or heavy client", "Detect; escalate (a business decision)", "No", "Yes"],
    ["Security compromise", "Quarantine plan, preserve evidence, human decides", "No", "No"],
  ]],
  ["Noise and observability", [
    ["False alarm or self-resolving blip", "Suppress and list in the digest", "Yes", "Yes"],
    ["Observability failure", "Treat missing data as unknown, never as healthy; escalate", "Partly", "Yes"],
  ]],
];

function Incidents() {
  const rows: [string, string, string, string, string][] = [];
  INCIDENTS.forEach(([fam, list]) => list.forEach(([cls, resp, auto, rev]) => rows.push([fam, cls, resp, auto, rev])));
  const tones: (TableRowTone | undefined)[] = rows.map((r) => (r[3].startsWith("Yes") ? "success" : r[3].startsWith("No") ? "danger" : "warning"));
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>What actually breaks: 33 classes in 8 families</H2>
        <Text tone="secondary">
          Built from incident studies (Microsoft Teams SoCC'22, Azure HotOS'19, Microsoft GenAI 2025, Google SRE, Mutiny 2024, Kubernetes configuration defects 2025, operator-bug studies), public postmortems (k8s.af, danluu) and benchmark fault catalogs. It does not come from any team list.
        </Text>
        <Table
          headers={["Family", "Class", "Sentinel's response", "Automatic?", "Reversible?"]}
          rowTone={tones}
          rows={rows.map(([f, a, b, d, e]) => [cs(f), cb(a), c(b), c(d), cs(e)])}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Starting set: the most coverage per unit of work</H3>
          <Numbered
            items={[
              "Change-attributed revert through the owner: bad rollouts, config pushes, routing manifests, image pulls. Binary and config pushes trigger 68% of Google outages, and rollback is Teams'22's top mitigation.",
              "Right-sizing and scaling through the owner: OOM, throttling, load spikes, autoscaler pinning. Cheap to reproduce in the Lab, and the permanent fix is a config PR.",
              "Node isolation (cordon, drain) by comparing a node with its peers, not with fixed thresholds.",
              "Event reason classifiers (OOMKilled, CrashLoopBackOff, ImagePullBackOff, FailedScheduling, FailedMount, webhook and x509 errors) plus certificate-expiry forecasting.",
              "Noise suppression from day one: in one Microsoft study, 11% of detected incidents were false alarms and about 20% self-recovered.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Never automatic: detect, diagnose, prepare, escalate</H3>
          <Bullets
            items={[
              "Data and state: restores, database failover, rollback after a schema or data migration, deleting or expanding volumes.",
              "Platform: control-plane or node-image downgrades, etcd operations, CNI changes.",
              "Anything that widens access or disables a control.",
              "Security incidents (evidence must be preserved; attackers can shape the signals).",
              "Business decisions: blocking tenants, user-visible load shedding, suspending business jobs, unless pre-declared.",
              "Anything outside the cluster: cloud quotas, cross-region failover, third parties.",
              "Weak attribution. At ING, the change closest in time was the culprit only 9.8% of the time, so reverts need statistical attribution.",
              "Fleet-wide actions without speed limits (the AWS us-east-1 2025 outage).",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="warning" title="Where this evidence is weak">
        The studies count different things (triggers vs root causes, high severity only), so their percentages can't be compared or added. k8s.af stories are mostly from 2017–2019 and self-managed clusters. Config-vs-code shares swing between studies because tooling shapes them, so Sentinel should learn each cluster's own mix. Vendor surveys are opaque. Benchmarks contain almost no change-induced faults. Published data on how well auto-remediation works in Kubernetes is nearly nonexistent, while VOID's 2024 data shows humans had to intervene in 75% of automation-involved incidents.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Watcher ------------------------------ */

function Watcher() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Watcher: see everything, store little, assume nothing</H2>
        <Text>
          One Go controller that works from the Kubernetes API alone, with two optional tiers enabled only after a probe passes. Its only settings are policies: a false-alarm budget per service per week, a false-discovery level, and a minimum effect size relative to noise.
        </Text>
        <T3
          headers={["Tier", "Collects", "Where it works"]}
          rows={[
            ["0 · API", "Informers (Pods, workloads, Services, EndpointSlices, Nodes, events.k8s.io/v1), persisted Events, logs API, metrics.k8s.io or kubelet stats, Prometheus if found", "Everywhere"],
            ["1 · Node agent", "/var/log/pods tailing; cgroup v2 memory.peak, memory.events, pressure-stall (PSI) data; no capabilities, read-only hostPath", "Not Fargate or AKS Automatic; OpenShift needs an SCC grant; Autopilot logs only unless allowlisted"],
            ["2 · eBPF", "OBI or Hubble behind an interface: per-version request rate, errors, latency, service graph, opt-in payloads", "BPF capabilities, kernel 5.8+ with BTF"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Collection</H3>
          <Bullets
            items={[
              "Strip managedFields and unused spec before caching; metadata-only informers for ConfigMaps; never watch Secrets (list reveals values), so infer secret changes from rollouts.",
              "Deduplicate container terminations by (pod UID, container, restartCount) instead of a cooldown. Events are already thinned by Kubernetes, so their counts are lower bounds.",
              "Log stream budget: always fetch the previous and current logs when a container terminates, restarts or flips readiness. Give the remaining follow slots to workloads ranked by recent rollout, anomaly score and error rate.",
              "Never request nodes/proxy: a plain GET on it can reach exec in any pod.",
              "Tier 1 tails CRI log files like the OTel filelog receiver: file fingerprint plus inode survives rotation, partial lines are reassembled, per-language state machines group stack traces. It ships templates, counts and redacted samples, not raw logs.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Time, windows, backpressure</H3>
          <Bullets
            items={[
              "Event time comes from CRI and object timestamps. Each source's watermark uses a learned lateness (the online p99 of arrival delay), and an idleness timeout stops quiet pods from holding things back.",
              "An incident is a session window over topology-connected entities, with a learned gap. It emits early and updates as late evidence arrives, replacing v1's fixed 500 ms window.",
              "Bounded queues. When saturated, shed payloads of already-known templates first, then healthy workloads' log streams; never drop Kubernetes state facts. Drop counters make detectors treat gaps as missing data, not zeros.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Adaptive detection</H3>
        <T3
          headers={["Signal", "Method", "Why"]}
          rows={[
            ["Kubernetes fact counts (OOM, crash loops, scheduling, probes)", "Gamma–Poisson posterior predictive with a prior pooled over similar workloads (empirical Bayes)", "One OOM in a service that never restarts is anomalous; in a routinely restarting job it isn't"],
            ["Rates and metrics (latency quantiles, error ratio, throttling, PSI, template rates)", "MSTL seasonal decomposition, then median/MAD residuals turned into tail probabilities by SPOT/DSPOT; Benjamini–Hochberg across series; CUSUM for persistence; Mann–Kendall 'went away' check", "The only knob is the false-alarm budget; at Meta, raw change-point detection gave a 99.7% false-positive rate"],
            ["Rollouts and config changes", "Compare new-ReplicaSet pods with old ones at the same time, plus before vs after (Gandalf-style)", "The concurrent control cancels daily cycles"],
            ["Error budgets", "Multiwindow multi-burn-rate when a target exists; a derived target from the 28-day attained ratio's lower bound otherwise (our heuristic, not from a source); Beta–Binomial posterior for low traffic", "Low-traffic services still get meaningful alerts"],
            ["Cold start", "Compare each pod with its sibling replicas from the first minute; gate on posterior width, not a fixed warm-up", "No day-long blind spot"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Logs and fingerprints</H3>
          <Bullets
            items={[
              "Pipeline: parse CRI, reassemble partial and multiline pieces (adaptive flush timeout), redact, mask variables, parse JSON structurally, mine templates.",
              "Templates: our own Go prefix tree in the style of Drain, merged hierarchically (ByteBrain-style) so granularity is chosen at query time instead of a fixed similarity threshold. LILAC and LibreLog lead accuracy benchmarks but need an LLM, so they're used offline only, to label evaluation data.",
              "Exception fingerprints, Sentry-style: exception type plus normalized in-app frames (module and function, no line numbers), with a versioned algorithm.",
              "'New error after deploy' requires a significantly higher rate on new pods than on old ones (Poisson rate-ratio test), not just 'first seen'.",
              "Interpretable count and novelty detectors over template streams. Deep log-anomaly models disappoint under realistic evaluation.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Topology and redaction</H3>
          <Bullets
            items={[
              "Dependency edges come from observed traffic (eBPF), traces, logs (hosts in connection errors) and config (user-set env and ConfigMap values; ignore auto-injected *_SERVICE_HOST variables). They're combined with noisy-OR, using per-source reliabilities calibrated against eBPF ground truth, decayed with a learned half-life, and kept with provenance.",
              "Redaction minimizes first: persist templates plus HMAC-hashed parameters; raw lines live only in short-retention evidence.",
              "Layered detectors: key-name rules with fail-closed allowlists, provider regexes from gitleaks (MIT), recalibrated entropy checks, Luhn/JWT/PEM structure checks, optional Presidio.",
              "Expect misses: the best tool on SecretBench reached 88% recall at 46% precision. So redact twice (before mining and before anything leaves the cluster) and test with canary secrets.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Algorithms to document for this module</H3>
        <T3
          headers={["Algorithm", "Purpose", "Cost"]}
          rows={[
            ["Median/MAD + MSTL", "Robust seasonal baseline; replica comparison at cold start", "O(1) per point; daily refit"],
            ["SPOT/DSPOT", "Calibrated tail probabilities from an alert budget", "O(1) amortized"],
            ["Conformal p-values + Benjamini–Hochberg", "False-discovery control across series", "O(m log m) per tick"],
            ["CUSUM, Mann–Kendall", "Persistent shifts; transient filter", "O(1); O(w²) small w"],
            ["Gamma–Poisson, Beta–Binomial", "Count baselines; low traffic", "O(1)"],
            ["Spatial-temporal rollout test, Poisson rate ratio", "Deploy regressions; new errors", "O(pods)"],
            ["Drain-style tree + hierarchical merge", "Log templates", "O(depth + tokens) per line"],
            ["Noisy-OR edge fusion", "Dependency map", "O(edges)"],
          ]}
        />
      </Stack>

      <Callout tone="warning" title="Risks and what to measure">
        Nobody has measured how many log streams the API server can carry. Autocorrelation breaks the exchangeability that SPOT and conformal methods assume, so prewhiten and verify. eBPF can be expensive on very fast services (one OBI test showed 35–63% added latency). Template churn after deploys fragments fingerprints. Measure agent overhead A/B (target at most 1% of node CPU at p95), detection latency split into collection, detection and assembly lag, per-event precision and recall without point-adjust, false alarms per service per week during quiet weeks, template grouping accuracy, topology edge precision, and how many canary secrets are caught.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Brain ------------------------------ */

const CONTRACT = `kind: ActionContract
spec:
  action: rollback
  owner: argocd
  reversibility: reversible          # reversible | compensable | irreversible-harmless | irreversible
  preconditions:                     # CEL, all must hold
  - "!target.operationInProgress && target.autoSyncDisableable"
  - "target.humanEditAge > policy.humanQuietPeriod"
  - "change.top.kind in ['image','template'] && !change.top.schemaMigration"
  blastRadius: {maxCapacityRemovedPct: policy.capacityBudget}
  execute: {op: rollbackToHistory, arg: change.top.previousHistoryID}
  verify: {primary: error_ratio, guards: [p99, caller_error_ratio], control: concurrent-preferred}
  undo: {op: rollbackToHistory, arg: preAction.historyID}
  followUp: gitRevertPR`;

function Brain() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Brain: decide and stop the bleeding, with no AI</H2>
        <Text>
          Replace v1's Granger-based analyzer. On independent benchmarks, simple robust deviation scoring matched or beat causal discovery, and Granger-style ranking scored near random. Choose actions by expected cost among those policy allows, execute through the owner, verify with anytime-valid tests, and graduate autonomy from pooled track records.
        </Text>
        <T4
          headers={["Stage", "Primary method", "Fallback or alternative", "Evidence"]}
          rows={[
            ["Onset and grouping", "BOCPD onset posterior per signal; join signals by learned lag pattern and dependency hops", "Fixed windows (rejected)", "Replaces v1's '3+ services in 60 s'"],
            ["Retrieve suspects", "BARO RobustScorer: max deviation from pre-onset median over IQR; keep the top 15 services", "N-sigma (fragile to onset error)", "RCAEval Train Ticket Avg@5: BARO 0.80 vs CIRCA 0.46, RCD 0.13"],
            ["Rerank", "Smooth Traversal on the dependency map (node score minus its highest-scoring parent), with a confidence set", "PetShop correlation ranking when the graph is untrusted", "Ties broken by earlier onset, then change evidence"],
            ["What changed", "Naive-Bayes log-likelihood ratios: type prior, onset gap, new-revision failing share, difference-in-differences", "Closest change in time (rejected: 9.8% correct at ING)", "Gandalf 94.9% precision; ChangeRCA 85% top-1 (only 30 incidents)"],
            ["Decide", "Expected loss: impact × time-to-mitigate + disruption + harm risk; value of information decides waiting or a Lab check", "Guarded Thompson sampling only to split near-ties", "Safe RL rejected: no faithful simulator, no guarantees"],
            ["Escalate", "Page on fast burn when no allowed action, failed verification, open breaker or approval needed; else notify", "Fixed thresholds", "Google: at most 2 incidents per 12-hour shift"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Action contracts and policy (data, not code)</H3>
          <Pre text={CONTRACT} />
          <Text size="small" tone="secondary">
            SentinelPolicy resources pair CEL match conditions with an effect (deny beats require-approval beats allow). They run at planning and again before execution, and ship with test fixtures. The executor enforces blast radius independently. CEL wins over Rego and custom YAML because it's Go-native, bounded in cost, and the same language as the API-server fence.
          </Text>
        </Stack>
        <Stack gap={8}>
          <H3>Reversibility classes</H3>
          <T3
            headers={["Class", "Examples", "Rule"]}
            tones={["success", "warning", "info", "danger"]}
            rows={[
              ["Reversible", "Restart, scale, owner rollback, abort, suspend", "Eligible for automation"],
              ["Compensable", "Volumes, ConfigMaps or Secrets, database-touching actions", "Automatic only after a verified snapshot (VolumeSnapshot ReadyToUse, Velero with freeze hooks, healthy WAL archive); undo is itself a plan"],
              ["Irreversible but harmless", "Deleting evicted pods or finished Jobs", "Explicit allow-list"],
              ["Irreversible", "Deleting volumes or namespaces, schema rollback, emails, payments, consumed messages", "Approval with a prepared, Lab-tested plan only"],
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Owner adapters</H3>
        <T3
          headers={["Owner", "Procedure", "Caveats found in research"]}
          rows={[
            ["Resolution order", "Argo CD tracking-id annotation or instance label, then Flux labels, then Helm release annotations plus the release Secret, then plain Kubernetes", "managed-by: Helm alone is a false signal on Argo-rendered charts"],
            ["Pre-flight", "Hold a per-workload Lease; defer on recent human managedFields edits or in-flight Argo/Flux/Helm operations; server-side dry-run; bounded target set; resourceVersion preconditions, never force", "Stops conflicts with humans and other controllers"],
            ["Argo CD", "Project-role token (get, update, sync or rollback). Record syncPolicy, disable auto-sync and read it back, roll back to the last healthy history ID, verify, revert PR, re-enable after merge", "Argo CD 3.1.0 ignores enabled:false, so delete the automated block there; ApplicationSets need ignoreApplicationDifferences on syncPolicy or it's Git-revert only"],
            ["Flux", "Suspend with Sentinel's field manager, helm rollback or rollout undo, verify, revert PR, resume after merge", "If a parent Kustomization owns spec.suspend, escalate; check HelmRelease remediation to avoid double rollbacks"],
            ["Helm", "SDK rollback with watcher-based waits", "Helm 4's server-side rollback only resets Helm-owned fields; use client-side apply when drift exists"],
            ["Plain Kubernetes", "Rollout undo for template changes; Eviction API for partial restarts (respects disruption budgets); ConfigMap reverts from Sentinel's own versioned copies", "If an HPA owns replicas, adjust its bounds or escalate"],
            ["Argo Rollouts, Flagger", "Rollouts: abort, then revert the template in Git. Flagger: Sentinel serves its rollback webhook", "Defer to their analysis when present"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Verification</H3>
          <Bullets
            items={[
              "Judge on user-facing signals of the target and its callers, never readiness or alert silence. In STRATUS, restarts cleared alerts in 8 of 18 problems, and healthy-looking pods hid 115 HTTP errors.",
              "Prefer a concurrent control (evict one of N pods, or roll back through a canary) and compare per request. Otherwise compare with a counterfactual: seasonal baseline plus the self-recovery rate of past untreated incidents, which corrects for regression to the mean.",
              "Anytime-valid count tests or confidence sequences, so 30-second polling doesn't inflate false alarms. Verdicts: success, harm or inconclusive (inconclusive never counts as success).",
              "STRATUS's rule only blocks regressions, so Sentinel also requires the primary signal to improve. A restart that hides a leak is labeled 'mitigated, not fixed' and handed to the Fixer with a 24-hour recurrence watch.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Guards, learning, autonomy</H3>
          <Bullets
            items={[
              "Token buckets per scope, a cap on capacity removed per window (AWS added exactly this after S3 2017 and us-east-1 2025), a breaker on failed verifications or abnormal action volume, and no restarts under overload.",
              "Beta(α, β) per action × fault class × service, with priors pooled across services by empirical Bayes; exponential decay with a half-life chosen by held-out accuracy; chaos and staging trials weighted below 1.",
              "Labels: verification verdict, 24-hour recurrence, approval or rejection with reason, a human reverting Sentinel's action (counted as harm), and a one-click 'was the root cause right?'.",
              "Ladder (after Parasuraman et al.): shadow, propose, act with a veto window, autonomous with a digest. Promote on a lower-bound rule; demote on any harm.",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="warning" title="Risks and what to measure">
        Public benchmarks inject resource and network faults and rarely bad deploys, which are Sentinel's main cases. BARO, RCAEval and two related evaluations share an author, so independent replication matters. Smooth Traversal assumes a single root cause; difference-in-differences needs parallel trends and fails on single replicas. Automation caused or worsened the S3 2017, us-east-1 2025 and Meta 2021 outages, and Sentinel could too. Measure AC@1/3 and Avg@5, lift over random, Brier score, change-attribution hit rate and innocent-rollback rate, escalation precision and recall, A/A false alarms of verification, undo and inconclusive rates, capacity removed, breaker trips, human reverts, time to mitigate and pages per shift. (v1's SLO module also mislabels a 3× three-day ticket rule as '10% in 3 days'; 3× for 3 days burns 30%.)
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Lab ------------------------------ */

function Lab() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Lab: the core contribution</H2>
        <Text>
          A fail-closed pipeline whose verdict comes from anytime-valid sequential tests, not fixed run counts. First, fix v1: its sandbox NetworkPolicy has one empty egress rule, which Kubernetes defines as allow-all egress, while it also copies real production Secrets into the sandbox.
        </Text>
        <T3
          headers={["Stage", "What happens", "Fails closed when"]}
          rows={[
            ["1. Synthesize", "Freeze the failing pod's template by digest; derive its dependency closure; record every fidelity difference from production", "A required dependency can't be classified"],
            ["2. Seal", "Namespace from a pre-created pool, quotas, default-deny network, per-Lab DNS, Pod Security 'restricted' with user namespaces, sandbox runtime if present", "The policy can't be applied"],
            ["3. Prove", "Probe matrix with positive controls before any workload starts, and again at teardown", "Any forbidden destination is reachable"],
            ["4. Instantiate", "Real copies of stateless services, throwaway databases seeded from recordings, fakes for external APIs", "Seeding fails"],
            ["5. Calibrate", "Run the baseline twice (B and B′) to learn noise masks and empirical nulls", "Noise too high to decide"],
            ["6. Reproduce", "Confidence sequence on the baseline's failure rate; fidelity versus production", "Not reproduced or fidelity gap: NOT SURE"],
            ["7. Race", "Randomized blocks of baseline and candidates; e-processes; elimination", "Budget exhausted: NOT SURE"],
            ["8. Judge", "Behavior diffs vs A/A noise, deletion controls, latency, memory", "Oracle too weak: NOT SURE"],
            ["9. Report", "Verdict with bounds and evidence; Experiment-owned namespace garbage-collected; TTL reaper as backstop", "n/a"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Environment synthesis</H3>
          <Numbered
            items={[
              "Freeze the template: pinned digests, same CPU architecture, resources, probes, env, volumes. Replace the ServiceAccount (no token, no RBAC); drop mesh sidecars, HPA, disruption budgets and cloud-identity annotations.",
              "Static edges: parse env, ConfigMaps, mounted configs and the structure of Secrets (host, port, scheme; never values). Resolve names through Services, EndpointSlices, pods and owners.",
              "Dynamic edges: Hubble or mesh telemetry if present; otherwise socket snapshots (catching pool connections opened long ago) plus TCP tracing. Edges seen only statically are created lazily when the Lab DNS log shows a lookup.",
              "Classify: stateless (copy), stateful or datastore (throwaway + seed), external (fake), API server and cloud metadata (deny).",
              "Frontier: recurse into copies; stop and fake once recordings cover the interactions or the budget runs out.",
              "Names: short names resolve in the Lab namespace; names embedding the production namespace go through a per-Lab CoreDNS rewrite with no upstream forwarding.",
              "TLS: a Lab certificate authority mints certificates for faked hosts, with trust injected per runtime (Go/OpenSSL cert files, Python requests bundle, Node extra CA certs, a JVM truststore built by an init container). Pinned or mutual TLS means NOT SURE on those paths.",
              "Secrets: shape-preserving fakes (same keys and formats, fresh keypairs) carrying honeytoken markers.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Isolation and its proof</H3>
          <Bullets
            items={[
              "Never allow kube-dns from the Lab: its default config forwards upstream, which is a DNS exfiltration channel. Use the per-Lab CoreDNS without forwarding.",
              "User namespaces (stable in Kubernetes 1.36, hostUsers: false) let root images run under 'restricted'. Needs Linux 6.3+ and containerd 2.0+.",
              "Positive controls must succeed (Lab echo service, Lab DNS name); otherwise 'blocked' is indistinguishable from 'probe broken'.",
              "Negative matrix must fail: API server, kube-dns and external resolution, the production twin's Service and pod IPs, node IPs (10250, a NodePort), cloud metadata (169.254.169.254, fd00:ec2::254, 169.254.169.252, 168.63.129.16), internet by IP, IPv6 when dual-stack.",
              "Continuous: a tripwire logs any Lab-sourced connection, the Lab DNS logs every query, and honeytoken markers are searched in both.",
              "Gaps the NetworkPolicy spec admits: pods may start before the policy is programmed, ICMP is undefined, node and loopback traffic can't be blocked. That's why the probe runs after policies exist and before workloads start.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Faking dependencies: build vs reuse</H3>
        <T4
          headers={["Dependency", "Strategy", "Reused", "Team builds"]}
          rows={[
            ["Stateless in-cluster service", "Real copy, same digest", "Kubernetes", "Fork and frontier logic"],
            ["HTTP/1.1 and HTTP/2 frontier", "Replay recorded calls", "MockServer engine as a temporary piece", "Signature matcher (method, path template, body minus learned noise); same-signature responses cycled in order; unmatched-call rate as a fidelity signal"],
            ["gRPC", "Replay without .proto files", "grpc-go's unknown-service handler", "Descriptor-aware matching when available"],
            ["Postgres or MySQL", "Real throwaway database, same major version", "Official images; DBLab or Neon branches if a masked source is allowed", "Schema from migrations or a schema-only dump; rows from recorded results mapped to tables via the column table IDs; loaded with replication role 'replica' to skip triggers"],
            ["Redis", "Real throwaway", "Official image", "Key seeder from recorded reads"],
            ["Kafka", "Single-node KRaft broker", "apache/kafka-native", "Topic mirror, trigger injection, pre-committed consumer offsets (otherwise 'latest' skips the trigger)"],
            ["External HTTPS", "Fake plus Lab CA", "Optional MockServer", "Per-host certificates"],
            ["Time and randomness", "Treated as noise by default", "libfaketime where it works (not Go static binaries)", "A/A noise masks"],
          ]}
        />
      </Stack>

      <Stack gap={8}>
        <H3>Reproduction by trigger class</H3>
        <T3
          headers={["Class", "How", "Pitfalls"]}
          rows={[
            ["Startup", "Fresh pod starts; amplify with a dependency that's down or slow at boot, or CPU throttling", "Init races need repeated starts"],
            ["Any request", "Sampled recorded requests", "Requests within one pod correlate, so the unit is the pod"],
            ["Specific request", "Exact capture with recorded downstream responses; else rebuilt from call sequences (BugRedux 16/17); else generated workload (ReproCopilot 38/50, not for concurrency)", "Needs opt-in payload capture"],
            ["Load or time", "Open-loop replay at production rate, extended adaptively; CPU limits, GOMAXPROCS sweeps, latency and reset faults in fakes; PCT or Fray for the JVM; rr chaos mode as an opt-in privileged tier", "For leaks, estimate the slope rather than waiting for OOM"],
            ["Data dependent", "Seed the rows the failing request read; shrink them with delta debugging", "Personal data means masking"],
            ["Unknown trigger", "Rank dependency fault sites by how closely the Lab's logs and call sequences match production; rerank after each run (a black-box take on Anduril)", "An amplified failure must match the production failure signature"],
          ]}
        />
      </Stack>

      <Stack gap={8}>
        <H2>The verdict procedure (the algorithm to point at)</H2>
        <Numbered
          items={[
            "Failure rates: a beta-mixture confidence sequence on each arm's failure rate. Ville's inequality bounds the chance of ever excluding the true rate by α, so you may look after every run.",
            "Superiority without knowing the baseline rate: pair one baseline and one candidate run per block and keep only discordant blocks. Testing 'the candidate fails at least as often' becomes a test on θ ≥ ½, where the baseline rate drops out (θ/(1−θ) is the odds ratio). The e-process is a supermartingale for every θ ≥ ½.",
            "Decision per candidate at level α/K: PASS when the superiority e-value reaches K/α and the candidate's upper bound is below τ; FAIL when its lower bound exceeds τ; NOT SURE when the budget runs out. τ is policy (the residual rate the error budget tolerates) and must sit below the reproduced baseline's lower bound.",
            "Racing: candidates share blocks; Bonferroni over e-processes stays valid under dependence and optional stopping; eliminate on FAIL; stop at the first PASS (good-arm identification, cheaper than finding the best arm).",
            "No regressions on normal traffic: every metric must be non-inferior (an intersection–union test, so no correction is needed). Responses: candidate-vs-baseline differences must stay within baseline-vs-baseline noise. Latency: Brunner–Munzel plus a Hodges–Lehmann shift interval and quantile confidence sequences, with the margin taken from A/A data rather than Kayenta's fixed ±25%. Memory: per-run Theil–Sen slope plus Mann–Kendall.",
            "Overfitting controls: automatically generated deletion 'fixes' (early return, catch-all) run as negative controls; if one passes, the oracle is too weak and the verdict is NOT SURE. Call sequences on normal traffic must match the baseline within noise (PATCH-SIM filtered 56.3% of incorrect patches and no correct ones).",
            "A/A calibration: run the whole procedure on two identical baselines. If the empirical false-PASS or false-FAIL rate sits above α, coarsen the unit or return NOT SURE.",
          ]}
        />
        <Grid columns={2} gap={20}>
          <Stack gap={6}>
            <Text weight="semibold">Runs to accept a perfect fix with the paired test</Text>
            <BarChart
              categories={["90%", "50%", "20%", "10%", "5%"]}
              series={[{ name: "Total runs (baseline + candidate)", data: [16, 28, 70, 140, 280] }]}
              showValues
              height={210}
            />
            <Text size="small" tone="tertiary">
              Horizontal axis: how often the bug fires on the baseline. Vertical axis: expected total Lab runs (count) to reach PASS for a fix that never fails, using 7 discordant blocks (uniform prior, α = 0.05), about 14 ÷ baseline rate. Computed, not measured. Rare bugs get expensive; amplification or NOT SURE is the honest answer.
            </Text>
          </Stack>
          <Stack gap={8}>
            <Card>
              <CardHeader>Rollout checks (for the Brain)</CardHeader>
              <CardBody>
                <Text size="small">The same procedure on live telemetry: old- vs new-ReplicaSet pods in the same window, counts per request, warm-up excluded, and two halves of the old pods as the A/A pair. Argo Rollouts or Flagger verdicts are used when present.</Text>
              </CardBody>
            </Card>
            <Card>
              <CardHeader>Cost and scheduling</CardHeader>
              <CardBody>
                <Text size="small">Each Lab is a Kueue workload (quota, priority, preemption). One baseline arm serves all candidates. Digests are pulled through a peer cache (Spegel). A rare bug at a 1% rate needs about 1,400 paired runs, which is why amplification and the NOT SURE outcome exist.</Text>
              </CardBody>
            </Card>
          </Stack>
        </Grid>
      </Stack>

      <Callout tone="warning" title="Risks and what to measure">
        Production-only conditions (scale, data volume, kernel, neighbors) show up only as a fidelity gap. Request bodies need opt-in capture. Time and randomness control isn't portable. Isolation depends on the CNI. Several reproduction results are self-reported (Ant Group, Anduril, ReproCopilot). Measure closure precision and recall on apps with known topology, the probe matrix across kind, EKS strict, GKE Dataplane V2 and AKS, red-team exfiltration attempts, reproduction rate per trigger class, empirical false-PASS and false-FAIL versus α, confidence-sequence coverage, runs to decision versus fixed 20, the NOT SURE rate, CPU-seconds per verdict, and leaked namespaces.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Fixer ------------------------------ */

const ADAPTER = `type Adapter interface {
  ID() string
  Detect(repo FS) float64
  ParseTrace(raw string) ([]Frame, error)          // plus signature
  MapFrame(Frame, Revision) (FileSpan, error)      // source maps, R8, path prefixes
  RevisionFromArtifact(Image) (*Revision, error)
  Grammar() *sitter.Language
  Templates() []Template                           // query + rewrite + class
  Check(ws Workspace, files []string) []Diagnostic
  BuildStrategy(ws Workspace) Strategy             // Rebuild | Overlay | Ko | Jib
  DevTests(ws Workspace, scope []string) Result    // never the oracle
}`;

function Fixer() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Fixer: a cascade ordered by how often each method is right</H2>
        <Text>
          Config restoration, then hunk-level partial revert, fixed templates, patterns mined from the repo's own history, and finally an optional LLM. Every candidate passes the same non-LLM gates. Two v1 conflicts must go: gates that use LLM-generated tests and reproducers, and an LLM 'reviewer' that audits fixes.
        </Text>
        <T3
          headers={["Step", "What happens", "Notes"]}
          rows={[
            ["1. Intake", "Signature, mitigation applied, live-object diff, image digest from the Brain and Ledger", "Mitigated is not fixed"],
            ["2. Source resolution", "Signed provenance, then build info embedded in the artifact (Go vcs.revision, Java git.properties, .NET SourceRevisionId), then OCI labels, then SHA-like tags, then a one-time annotation", "Require one signed source or two that agree; abstain if Go's vcs.modified is true"],
            ["3. Localization", "A learned ranker over culprit-change features (ChangeLocator, Orca's differential), crash-stack expansion (CrashLocator), span spectra (Ochiai on failing vs normal traces) and the config last-known-good diff", "Only about 60% of fixes touch a stack frame; combining families beat the best single one by 200% at top-1"],
            ["4. Generation", "Config restore, partial revert, templates, mined patterns, LLM; keep at most 3 Lab-passing alternatives", "Revert is a labeled fallback: developers declined all 18 SapFix reverts"],
            ["5. Pre-filters", "Parse and typecheck, no new analyzer warnings, scope limited to localized files (deny CI, CODEOWNERS, migrations, Dockerfiles, lockfiles), anti-deletion, minimal edits, dedupe", "Kali showed deletions pass weak tests"],
            ["6. Build", "Rootless BuildKit with the repo's Dockerfile and frozen lockfiles, or overlay changed files onto the production digest for interpreted code; sign with cosign and attach provenance", "Build-parity check first: rebuild the unpatched commit and require the Lab to reproduce the incident on it"],
            ["7. Lab", "The baseline must fail and the candidate pass, plus differential replay against an A/A noise floor and sequential tests", "The Lab owns the oracle"],
            ["8. Delivery", "PR with Lab evidence and localization rationale; auto-merge only for graduated classes, with a canary and automatic Git revert", "Outcomes retrain the ranker, pattern priors and trust scores"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Repair without an LLM</H3>
          <Bullets
            items={[
              "Config restoration: restore only the fields the symptom implicates to the last known-good revision. The symptom-to-field map starts as a seed table and learns from outcomes. Timeouts, limits and probe thresholds come from quantiles of normal runs, never constants.",
              "Templates (tree-sitter query plus rewrite): Yuan's trivial error-handling patterns (35% of catastrophic failures); nil and missing-key guards that must log and emit a metric so nothing is silently masked; retry caps with backoff and idempotency; resource leaks gated by escape analysis.",
              "Mined patterns: GumTree-style AST diffs on tree-sitter, anti-unification and clustering (Getafix, Bloomberg's Fixie). Training pairs come from incident-linked fix commits, human commits that closed Sentinel incidents, human edits to Sentinel's PRs, and rejected fixes.",
              "Expected coverage is a hypothesis to test: high for config incidents with a known-good revision, roughly 10–25% of code incidents. No primary source has measured this for Kubernetes services.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Optional LLM, fenced in</H3>
          <Bullets
            items={[
              "Generator only. LLM judges accept 17–22% of overfitting patches and favor their own outputs; models exploit tests they can see.",
              "Split oracles: the LLM may iterate against a dev set (compiler, repo unit tests, parsed crash); the Lab's reproduction and traffic replay stay hidden. Test feedback raised Meta's solve rate from 28.5% to 43.9%.",
              "Abstain first, then sample about 20 candidates; choose by Lab evidence plus minimality, never by vote.",
              "Prompt injection is enforced architecturally: no tools, network or credentials; output is a diff that must pass the scope filter; only the Lab executes code.",
              "Public coding benchmarks mislead: OpenAI retired SWE-bench Verified in February 2026, and SWE-Bench Pro shows large public-vs-private gaps. Choose models on your own bench.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Config and manifest repair: finding the right file</H3>
        <Numbered
          items={[
            "Find the writer: the managedFields manager; then Argo tracking-id to the Application's repo and path, Flux labels to the source, the Helm release secret to chart and values, or a repo annotation for plain kubectl.",
            "Find the file and line: Kustomize build metadata (origin and transformer annotations) names the file and patch that set the field. For Helm, perturb candidate values and re-render to see which keys move the field, then edit the highest-precedence layer.",
            "Edit with a comment-preserving YAML syntax-tree splice.",
            "Render-diff check: re-render every overlay. Only the intended fields may change; report the blast radius on other environments.",
            "Converge: PR to the config repo, verify the live object after sync, then release the Brain's mitigation (for example, re-enable auto-sync).",
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Language adapter (everything else is language-agnostic)</H3>
          <Pre text={ADAPTER} />
          <Text size="small" tone="secondary">Order: Go, Python, Node, then Java. SCIP indexers (Go, TS/JS, Java/Kotlin, Python, Ruby, C#, Rust, C++) can add precise cross-references.</Text>
        </Stack>
        <Stack gap={8}>
          <H3>Auto-merge safety</H3>
          <T3
            headers={["Tier", "Fix kind", "Policy"]}
            tones={["success", "success", "info", "warning", "danger"]}
            rows={[
              ["T0", "Restore an exact last-known-good value", "Graduates first"],
              ["T1", "Correct with a learned value", "Graduates per repo and namespace"],
              ["T2", "Catalog template", "Graduates slowly"],
              ["T3", "Mined pattern", "Mostly human review"],
              ["T4", "LLM patch", "Always human review"],
            ]}
          />
          <Bullets
            items={[
              "Graduation: 'held' (no revert or recurrence within 7 days) modeled as Beta-Binomial; allow auto-merge once the 95% lower bound clears the operator's threshold. By the rule of three, claiming under 5% failure takes about 60 clean human-approved fixes.",
              "Never auto-merge migrations, CI, CODEOWNERS, Dockerfiles, major dependency bumps or security code. SLSA Source L4 branches can't be auto-merged.",
              "Mechanics: a separate GitHub App, the Lab verdict as a required check, re-verify the base before merging, one auto-merge in flight per service, and a kill switch.",
              "Canary at 5%, 25% and 100%, judged with mSPRT; rollback is a Git revert of the culprit commit plus a rollout abort, escalating if later commits touched the same hunks.",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="warning" title="Risks and what to measure">
        LLM-free coverage for Kubernetes incidents has never been measured. Pattern mining starts cold. Verified patches still overfit (29.6% of test-passing SWE-bench patches behave differently from the human fix). Build parity breaks with unpinned dependencies and private base images. Guard templates can mask bugs. Measure source-resolution accuracy, localization top-1/top-5 and MAP, per-generator plausible, correct and overfitting rates, coverage by incident class, the Lab's false-accept rate, PR acceptance, time to first response, human edits before merge, recurrence at 7 and 30 days, cost per landed fix, and time to first candidate (developers tolerate up to an hour and review at most 5 patches). Run ablations with the LLM off, mining off, and each localization signal removed.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Platform ------------------------------ */

function Platform() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Security of Sentinel itself</H2>
        <Text>A read-mostly controller whose writes are limited three ways: RBAC scoped to namespaces, an admission fence the API server enforces, and typed action contracts. A rule-based Brain makes every decision; the LLM never holds write credentials.</Text>
        <T3
          headers={["Identity", "Can", "Never"]}
          rows={[
            ["watcher", "get/list/watch pods, pods/log, events and workloads in allowlisted namespaces", "list Secrets, pods/exec, nodes/proxy, escalate, bind, unconstrained impersonate, token requests, admission objects"],
            ["actor", "patch existing Deployments/StatefulSets and */scale per namespace; on 1.36+ constrained impersonation, and approved actions run as the approver", "create workloads (that implicitly grants every Secret and ServiceAccount in the namespace)"],
            ["lab", "a pool of namespaces and RoleBindings created at install, so Sentinel never writes namespaces or RBAC at runtime", "touch production namespaces"],
            ["builder", "no API token; build in the repo's CI with SLSA provenance by default; in-cluster rootless builds opt-in and isolated", "run with production credentials"],
            ["llm pod", "only the provider key, egress only to the provider, token budgets", "hold a Kubernetes token or reach the cluster API"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Fence, fail-closed, audit, supply chain</H3>
          <Bullets
            items={[
              "Fail closed: an empty token, secret or allowlist means refuse. Google's Diskerase wiped machines because an empty set meant 'all'. Helm generates missing secrets; API calls use TokenReview and SubjectAccessReview; webhooks require HMAC, a timestamp window and constant-time comparison; repo reads use os.OpenRoot; clones skip git hooks.",
              "Policy fence: admin-owned ValidatingAdmissionPolicies (CEL, failurePolicy Fail) matching Sentinel's identities. Allowlisted namespaces only; patches may change only replicas within bounds, the restart annotation, or the image to a previously deployed or signed digest; never service accounts, volumes, secret refs, RBAC, admission objects or CRDs. Every action first runs as a server-side dry run through the same admission path.",
              "Audit: one append-only, event-sourced log is both the Ledger and the audit trail, organized as a Merkle tree (Tessera tiles, as in Rekor v2) with a persisted signing key and checkpoints published where Sentinel can't rewrite them. v1's in-memory hash chain broke on restart.",
              "Supply chain: releases ship SLSA L3 provenance and cosign signatures; admission admits only digests from expected signers; Lab-built images carry an identity production rejects.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Threat model</H3>
          <T3
            headers={["Threat", "Impact", "Mitigation"]}
            rows={[
              ["Telemetry injection", "Wrong action", "Deterministic Brain, typed allowlist, masking, fence"],
              ["Controller compromise", "Cluster takeover", "Patch-only namespaced RBAC, fence, signed images"],
              ["Unauthenticated API or webhook (v1)", "Anyone acts", "Fail-closed startup, HMAC plus replay window"],
              ["Spoofed chat approval", "Unauthorized change", "Signed requests, identity mapping, impersonate the approver"],
              ["Lab escape", "Data leak", "Probed egress deny, restricted pods, synthetic credentials"],
              ["Malicious repository", "Host read, code execution", "os.OpenRoot, no hooks, tokenless builder"],
              ["Ledger tampering", "Hidden actions", "Merkle log, external checkpoints"],
              ["Wrong revert (v1)", "Regression", "Revert only the culprit commit; abort if later commits overlap"],
              ["Runaway automation", "Amplified outage", "Adaptive concurrency, error-budget action budgets, kill switch"],
            ]}
          />
        </Stack>
      </Grid>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H2>Scale and reliability</H2>
          <Bullets
            items={[
              "Informers with bookmarks and WatchList instead of v1's raw watch channels (which never restarted); export watch restarts and last-event age; fail readiness when caches go stale.",
              "Memory: untrimmed large pods measured about 241 KB each in the informer cache (about 23 KB trimmed), so trim fields, use selectors and metadata-only informers.",
              "Log streams bypass API Priority and Fairness, so fetch bounded tails after a trigger. Ship a FlowSchema, honor 429 Retry-After, size executor concurrency adaptively (AIMD or Gradient2).",
              "High availability: two replicas with Lease leader election. client-go provides no fencing, so writes use resourceVersion preconditions and disruptive actions re-read live state.",
              "Stress test: KWOK at about 1k nodes and 100k pods for object scale, real nodes running log generators for log storms, kube-burner for churn, fault storms of 1–10% of pods, leader kills and forced watch expiries. Accept when no API-server SLO violation is attributable to Sentinel (a statistical A/B), and publish memory growth per 1k pods.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H2>Extensibility and data model</H2>
          <T3
            headers={["Extension point", "First two implementations", "Hosting"]}
            rows={[
              ["Signal source", "Kubernetes watch, Alertmanager", "Compiled in"],
              ["Detector", "CEL rule, change-point detector", "Compiled in or WASM (sandboxed)"],
              ["Action", "Rollout undo, scale", "Compiled in, with a contract"],
              ["Owner adapter", "Argo CD, Flux", "Compiled in"],
              ["Lab experiment", "Replay canary, config bisection", "Compiled in"],
              ["Fake", "HTTP replay, ephemeral Postgres", "Compiled in"],
              ["Fix strategy", "Config rollback, partial revert", "Compiled in"],
              ["Language adapter", "Go, Python", "Compiled in or gRPC plugin"],
              ["Notifier", "Slack, GitHub", "gRPC plugin"],
              ["LLM provider", "OpenAI-compatible (incl. local), Anthropic", "gRPC plugin"],
            ]}
          />
          <Text size="small" tone="secondary">
            Data model: Signal → Incident (fingerprint, state machine) ← Evidence (source, trust label, hash) → Hypothesis (prior, posterior) → Action → Verdict → Fix → TrackRecord. Stored as an event-sourced log with CloudEvents envelopes, projected into SQLite or Postgres. CRDs hold only intent and small status (SentinelPolicy, ActionRequest, Experiment), because etcd caps objects at 1.5 MiB. Rules ship as OCI artifacts declaring the engine version they need, like Falco.
          </Text>
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H2>What people see</H2>
        <Grid columns={2} gap={16}>
          <Bullets
            items={[
              "Overview: capability report, coverage map, open incidents, error-budget status.",
              "Incident timeline: signals with trust labels, hypotheses with their likelihood ratios, actions with contract states, verification charts, Lab runs with confidence sequences, PRs, and 'cannot determine' as a valid outcome.",
              "Policies: the autonomy ladder per namespace and action, with track records and their intervals.",
              "Lab runs: probe matrix results, fidelity gaps, verdicts with bounds.",
              "Daily digest and the shadow report (what it would have done).",
            ]}
          />
          <Bullets
            items={[
              "Calibrate trust (Lee and See): show track records with intervals, which rules fired, which hypotheses were rejected.",
              "Announce intent: 'rolling back X in 4 minutes because …; reply stop'. The veto window comes from blast radius, reversibility, confidence and measured human response time, not a fixed timer.",
              "Show diagnosis confidence and fix confidence separately; early confident labels anchor teams.",
              "Fight alert fatigue: deduplicate, group by owner, suppress symptoms of an open root incident, support silences.",
              "Chat commands are authorized exactly like API calls.",
            ]}
          />
        </Grid>
      </Stack>
    </Stack>
  );
}

/* ------------------------------ Evaluation ------------------------------ */

function Evaluation() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Hard proof, the way the guide asks</H2>
        <Text>
          A pre-registered blind fault-injection study, external human review of every patch, selected benchmark slices, stress tests, and paired baselines and ablations. Two current weaknesses come first: v1's bench scores fixes by diff line-recall and by Sentinel's own approval, and runs #10–#30 were tuned on Marketly's 9 bugs, which therefore become a development set only.
        </Text>
        <Callout tone="info" title="Ground rules">
          Before testing, freeze the commit, the config hash and the LLM version. Pre-register hypotheses, metrics, budgets, the retry policy for infrastructure failures, and the analysis (OSF). Deny Sentinel read access to chaos CRDs, flag names and injection events, which leak the answer.
        </Callout>
      </Stack>

      <Stack gap={8}>
        <H3>Public benchmarks</H3>
        <T4
          headers={["Benchmark", "Scores", "Fit", "Caveat"]}
          rows={[
            ["AIOpsLab (kind)", "48 problems, 10 fault types incl. no-fault: detection, localization, RCA, mitigation", "High for Watcher and Brain", "Only 13 mitigation problems; no durability check"],
            ["SREGym", "90 problems incl. AIOpsLab and ITBench; deterministic mitigation oracle; ships STRATUS", "High: random draws from 3,623 fault–target pairs", "Needs an adapter (agents run in containers); Lite version is saturated"],
            ["Incident-Arena", "20 tasks; pass requires healthy SLIs under fresh traffic and after restart or re-trigger", "Medium: stretch test for config repairs", "Preprint; heavy (8 CPU, 16 GB per task); restarts fail by design"],
            ["RCAEval", "735 offline cases; 90 code-level faults; 15 baselines", "High: isolates localization", "Beat BARO, not the causal methods"],
            ["ITBench, OpenRCA, TrainTicket-22", "SRE scenarios; offline RCA; industrial faults", "Low", "Only 6 ITBench SRE scenarios self-hostable; many TrainTicket faults are logic or UI bugs"],
          ]}
        />
        <Text size="small" tone="tertiary">
          Critiques to cite: restarts 'solved' 8 ITBench problems because the faults didn't persist; most benchmarks never check durability or that doing nothing fails; public apps and bugs risk LLM memorization.
        </Text>
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Custom fault library (about 120 faults)</H3>
          <Bullets
            items={[
              "Strata: config and deploy change, image and dependency, resource, network, code bug, platform. Over-weight change-induced faults; cover at least 3 languages and 2 apps; at least 15% no-fault or noise windows and about 10% concurrent pairs.",
              "Tools: Chaos Mesh (pod, network, stress, IO, HTTP, DNS), Litmus probes as SLO oracles, Toxiproxy, flagd flags, ConfErr-style config mutations.",
              "Code bugs: prefer reverted real bug-fix commits and field-data operators (missing call, missing if, wrong value); mutation tools only generate candidates for hand curation; report results by fault origin.",
              "Qualify every fault: the reference fix passes, doing nothing fails, and a restart doesn't fix it unless labeled transient.",
              "Ground truth per fault: injector manifest, NTP-stamped times, target, mechanism, set of acceptable fixes, persistence, expected SLI impact, published as a hash in advance.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Blind study and human review</H3>
          <Numbered
            items={[
              "Outsiders (TAs, peers, industry contacts) author the pool; the team never sees it.",
              "A supervisor-appointed game master draws a stratified random sample (seed fixed after pre-registration), with faults at random times interleaved with no-fault windows.",
              "Sentinel runs autonomously; any code change ends the study and is reported.",
              "Deterministic oracles score SLO recovery, durability and scope; blinded humans judge diagnoses; ground truth is unsealed after the last run.",
              "Patch review: at least 6 external developers, 3 per patch, screened on gold patches. Mix in about 25% human-written fixes, format all identically, randomize order. Labels: correct, plausible-but-incorrect, incorrect or harmful, unknown, plus 'would merge' (1–5). Report Fleiss' κ, Krippendorff's α and raw agreement.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H3>Metrics</H3>
        <T3
          headers={["Metric", "Definition", "Treatment"]}
          rows={[
            ["Detection recall and precision", "Injected faults detected within a window; alerts matched to injections", "Wilson intervals; false alarms per day on a 72-hour soak"],
            ["Time to detect, time to mitigate", "Injection to first incident; injection to SLO restored and durable", "Median and p95 with cluster bootstrap; Kaplan–Meier for timeouts"],
            ["Localization AC@1/3", "True root cause in the top k", "Wilson"],
            ["Mitigation success", "SLO restored in time and holding after restart or re-trigger", "Wilson; McNemar against baselines"],
            ["Wrong-action rate", "Actions on controls, worsening SLIs, or out of scope", "Rule of three if zero"],
            ["Verified-fix, valid-fix, false-fix rates", "Lab-passed / needing a fix; human-majority correct; Lab-verified but judged incorrect", "False-fix is the headline safety metric"],
            ["Confidently-wrong rate", "Confidence above the action threshold but wrong", "Reliability diagram, Brier score"],
            ["Cost", "Tokens, Lab CPU-minutes, Sentinel overhead", "Pareto plot against success"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Baselines and ablations</H3>
          <Bullets
            items={[
              "No-op (plain Kubernetes), which exposes restart luck.",
              "Scripted policies: restart, rollout undo of the latest change, scale up.",
              "Robusta playbooks: the fair comparison for 'the Brain needs no AI'.",
              "Argo Rollouts or Flagger analysis-driven rollback for change-induced faults.",
              "k8sgpt analyzers (detection) and HolmesGPT (diagnosis); STRATUS through SREGym.",
              "Ablation ladder on one draw: plain Kubernetes, then + Watcher/Brain, + config fixes, + Lab, + LLM. Also Lab off vs on, and fixed 20 runs vs sequential.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Sample sizes (computed)</H3>
          <Bullets
            items={[
              "Wilson half-width at about 50%: ±20 points at n=20, ±13 at 50, ±10 at 100. Target 80–100 blind scenarios plus at least 30 controls.",
              "Zero wrong actions bounds the rate below 5% only after 60 opportunities, below 2% after 150, below 1% after 300.",
              "Paired comparison (power 0.8): about 57 scenarios to detect a 20-point gain, 85 for 15, 155 for 10. Rerun baselines rather than comparing to published numbers.",
              "Prefer more scenarios to more repeats; keep 3 runs each for consistency checks. About 100 rated patches give κ within ±0.14–0.17.",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="neutral" title="Live demo plan">
        A pre-warmed kind cluster with pre-pulled, digest-pinned images, a local registry and Gitea (no venue Wi-Fi dependency), plus a hot-standby VM and a full recording. An examiner draws an envelope from 10 rehearsed faults (disclose each one's rehearsal success rate over at least 10 runs). Show the live detection timer, the Brain mitigating with the LLM visibly off, the Lab verdict and the Fixer PR, then a no-fault control where Sentinel correctly does nothing and an out-of-scope fault it escalates. Expect: "would a restart fix it?", "show something unprepared", "is the patch correct?", "what's yours vs the LLM's?", "does it scale?", "how sure are you?" Each has an answer in the evidence above.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Plan & team ------------------------------ */

function Plan() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Phases: start with the core set, then extend</H2>
        <T4
          headers={["Phase", "Build", "Gate (done when)", "Depends on"]}
          rows={[
            ["0 · Foundations", "Data model and event-sourced Ledger; kind harness with Online Boutique, OTel Demo and Marketly (dev only); fault injector; capability detection; fence chart; reuse v1's store, GitHub App and repo-CI code", "Harness injects any fault and records ground truth; correct capability reports on kind, k3s, minikube", "Nothing"],
            ["1 · Observe", "Watcher tier 0, detectors, logs and fingerprints, topology; Brain grouping, localization and attribution in shadow mode", "Detection precision and recall, localization AC@k on the dev pool; false alarms per week in a soak", "Phase 0"],
            ["2 · Act", "Action contracts, CEL policies, owner adapters (plain, Argo CD, Flux, Helm, Rollouts), verification, guards, escalation", "Mitigation success and wrong-action rate for change-induced and resource classes", "Phase 1"],
            ["3 · Lab core", "Synthesis, seal and proof, seeding, HTTP/gRPC/Redis replay (MockServer as a temporary piece), verdict engine, A/A calibration; config-fix path end to end", "Zero isolation breaches; false-PASS at or below α on labeled patches", "Phase 2"],
            ["4 · Fixer core", "Source resolution, localization ranker, config repair, templates for Go and Python, build parity, PRs, auto-merge tier T0", "Verified-fix and false-fix rates on the dev pool; external reviewer pilot", "Phase 3"],
            ["5 · Extend", "Node agent, OBI exporter, node isolation, DNS and dependency classes, Node and Java adapters, mined patterns, optional LLM, rollout checks, autonomy graduation, own faker replacing MockServer", "Each extension ships with scenarios; coverage grows without regressions", "Phase 4"],
            ["6 · Evaluate", "Freeze; blind study; human review; stress; ablations; benchmark slices", "Pre-registered report", "Phase 5"],
            ["7 · Defend and release", "Demo rehearsal (10+ runs per envelope fault), per-module algorithm tables, color-coded diagram, open-source v2.0", "Supervisor sign-off", "Phase 6"],
          ]}
        />
      </Stack>

      <Stack gap={8}>
        <H2>Team of four: one substantial, demoable module each</H2>
        <Grid columns={4} gap={12}>
          <Card>
            <CardHeader>Member A · Watcher</CardHeader>
            <CardBody><Text size="small">Collection tiers, adaptive detection, logs and fingerprints, topology, redaction, the recorder (OBI exporter) and the replay faker. Leads the Ledger and UI. Demo: catches real incidents in noisy traffic.</Text></CardBody>
          </Card>
          <Card>
            <CardHeader>Member B · Brain</CardHeader>
            <CardBody><Text size="small">Localization, change attribution, decisions, contracts and CEL policies, owner adapters, verification, learning and autonomy, the policy fence. Leads security review. Demo: an Argo CD rollback undone when it doesn't help.</Text></CardBody>
          </Card>
          <Card>
            <CardHeader>Member C · Lab</CardHeader>
            <CardBody><Text size="small">Environment synthesis, sealing and isolation proof, data seeding, reproduction strategies, the verdict engine, racing, rollout checks. Leads the evaluation harness. Demo: sealed reproduction and a verdict with bounds.</Text></CardBody>
          </Card>
          <Card>
            <CardHeader>Member D · Fixer</CardHeader>
            <CardBody><Text size="small">Source resolution, localization ranker, config repair, templates and pattern mining, builds and signing, PRs and auto-merge, the optional LLM. Leads documentation. Demo: a verified PR for a crash.</Text></CardBody>
          </Card>
        </Grid>
        <Text size="small" tone="tertiary">Nobody does integration only. Everyone authors fault scenarios in their own area and owns their module's algorithm table.</Text>
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>What changes from Sentinel v1</H3>
          <T3
            headers={["Area", "v1", "v2"]}
            tones={["danger", "danger", "danger", "danger", "warning", "warning", "warning", "success"]}
            rows={[
              ["Sandbox", "Empty egress rule = allow-all; production Secrets copied in; sandbox gate defaults to off", "Default-deny, proven by probe; synthetic secrets; fail closed"],
              ["API and webhooks", "Auth fails open when the token is unset; signatures skipped when the secret is empty", "Fail-closed startup; mandatory HMAC"],
              ["Repo reads", "Follows symlinks", "os.OpenRoot, no git hooks"],
              ["Fix gates", "LLM-generated tests and an LLM reviewer", "Lab owns the oracle; LLM never judges"],
              ["Root cause", "Granger-based ranking", "Robust deviation plus graph reranking"],
              ["Watching", "Raw watches never restarted; follows every pod's logs", "Informers, bookmarks, log budget"],
              ["Claims", "'LLM optional' while the README requires a key; MIT license", "Truly optional LLM; Apache-2.0"],
              ["Keep", "SQLite store, GitHub App and PR code, repo-CI runner, error parsing, metrics and logging", "Reused with changes"],
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Supervisor guide checklist</H3>
          <T3
            headers={["Guide point", "Status", "Evidence"]}
            tones={["success", "success", "success", "success", "success", "success", "warning"]}
            rows={[
              ["LLM not the core", "Met", "LLM-off results come first; no LLM output can write to the cluster"],
              ["No plug-and-play", "Met", "Four green cores; reuse only at swappable edges"],
              ["Hard proof", "Planned", "Blind study, external patch review, benchmarks, ablations"],
              ["Stress testing", "Planned", "KWOK, log storms, churn, chaos on Sentinel itself"],
              ["Live demo", "Planned", "Envelope draw on a pre-warmed kind cluster, recorded fallback"],
              ["3–4 modules, one per member", "Met", "Watcher, Brain, Lab, Fixer"],
              ["Feasibility and novelty", "Confirm with supervisor", "Prior art cited; the difference is verification and determinism"],
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="warning" title="Top risks">
        Spreading depth too thin (each phase has a gate; X before Y). The Lab unavailable on clusters without NetworkPolicy enforcement (fail closed, test the matrix). Rare bugs costing hundreds of Lab runs (amplification or NOT SURE). Source resolution failing on real images (fallback chain plus annotation). Reviewer recruitment for the blind study (start early). Benchmark adapters taking longer than expected (choose slices). The scope of two implementations per extension point (ship contracts and safety limits first).
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Market ------------------------------ */

function Market() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Where Sentinel stands</H2>
        <T4
          headers={["Product", "Open source / self-hosted", "Acts automatically?", "Verifies before PR?"]}
          rows={[
            ["Datadog Bits AI SRE + Bits Code", "No / no", "Investigates every alert; fixes mostly suggested", "Partly (iterates until CI passes)"],
            ["Sentry Seer Autofix", "No / no", "Auto root cause; can draft PRs", "No (fixes CI after the PR)"],
            ["incident.io AI SRE", "No / no", "No: PRs only", "No"],
            ["Komodor Klaudia", "No / agents yes", "Yes, policy-scoped", "Checks after acting"],
            ["Azure SRE Agent", "No / no", "Yes (autonomous mode)", "Checks after acting"],
            ["Speedscale Agent Factory", "No / BYOC", "Opens PRs", "Yes, replays recorded production traffic"],
            ["Causely", "No / yes", "No (deterministic causal engine)", "No"],
            ["HolmesGPT (CNCF Sandbox)", "Yes / yes", "Read-only by default", "No"],
            ["k8sgpt operator (CNCF Sandbox)", "Yes / yes", "Alpha: image-field swap only", "Dry run plus rollout check"],
            ["Robusta", "Yes / yes", "Rule-based playbooks", "No"],
            ["STRATUS (research)", "Yes / yes", "Yes, every action has an undo", "Severity check after each step"],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Why teams hesitate, and Sentinel's answer</H3>
          <Bullets
            items={[
              "Control: 77% hesitate to let AI act without approval; 75.7% restrict agents to read-only or approval-required work (vendor surveys, soft numbers). Answer: shadow mode by default, per-namespace autonomy, published precision before writes.",
              "Accuracy: the best models solve under half of independent Kubernetes root-cause tasks. Answer: deterministic decisions with calibrated confidence and NOT SURE.",
              "Blast radius: an AI agent reportedly caused a 13-hour AWS outage in 2026 (Amazon blames permissions). Answer: the fence, reversible-only automation, velocity limits.",
              "Data: security and privacy is the top blocker. Answer: self-hosted, data stays in the cluster, local LLM or none.",
              "Cost: token spend overruns estimates. Answer: works with no LLM; hard budget caps when used.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Positioning, and claims to avoid</H3>
          <Callout tone="info">
            Sentinel is an Apache-2.0, self-hosted Kubernetes remediation controller. Deterministic rules, not an LLM, choose from a small catalog of reversible, owner-aware mitigations. A config or code fix becomes a PR only after it reproduces the failure and passes in an isolated Lab, with statistical evidence attached. An optional local or API LLM can draft candidate patches, which face the same gate. We publish our success and failure rates.
          </Callout>
          <Bullets
            items={[
              "Avoid 'first' or 'only' verification-first tool (STRATUS, Speedscale and small repos exist).",
              "Avoid 'autonomous AI SRE … without waking you up'; it invites the trust objection.",
              "Avoid 'verified = correct', statistics from a handful of scenarios, SWE-bench scores, MTTR percentages without baselines, compliance labels.",
              "Say 'no LLM output can trigger a cluster write', and make it true.",
            ]}
          />
        </Stack>
      </Grid>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Lessons from projects that stalled</H3>
          <Bullets
            items={[
              "Keptn (archived 2025): one company stopped funding it, and its event model fought GitOps. Recruit outside maintainers; work with Argo CD and Flux, not against them.",
              "Botkube: its hosted service shut down and the integrations died with it. No core feature may depend on a service you host.",
              "Pixie: a CNCF badge isn't sustainability (health score 32/100).",
              "StackStorm: 22 months between releases; integrations rotted. Keep scope narrow and test integrations.",
              "STRATUS and hobby repos: good ideas, no users. Packaging and a 5-minute demo matter as much as the algorithm.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Optional LLM and licensing</H3>
          <Bullets
            items={[
              "Default: no LLM (rollback, scale, restart, pause, config restores, templates, revert PRs, all verified).",
              "Local: vLLM serving an Apache-2.0 model around 27B parameters (Qwen3.6-27B or Qwen3.8-27B) on one 24–48 GB GPU, or gpt-oss-20b on llama.cpp for CPU or small GPUs. Vendor benchmark scores are only for shortlisting; decide on your own bench.",
              "API: start with a cheap tier and escalate; expect roughly $0.05–$1 per attempt; redact before sending; hard caps. A local GPU only saves money when busy; at small scale it buys data control.",
              "License: relicense to Apache-2.0 now (CNCF requires it; adds a patent grant). DCO sign-offs, governance and maintainer files, OpenSSF badge. CNCF Sandbox needs 3+ maintainers from 2+ employers; apply once there's an external maintainer and adopters.",
            ]}
          />
        </Stack>
      </Grid>
    </Stack>
  );
}

/* ------------------------------ Decisions ------------------------------ */

type Dec = [string, string, string, string, string];
const DECISIONS: Dec[] = [
  ["Root-cause method", "Robust deviation (BARO-style) + dependency-map reranking", "Causal discovery (PC, Granger), trace-based (MicroRank), LLM ranking", "Simple methods match or beat causal ones and work without traces", "Our own pool, rich in bad deploys and config errors, shows a different winner"],
  ["Change attribution", "Likelihood ratios + difference-in-differences", "Closest change in time; LLM ranker", "Closest-in-time was right 9.8% of the time at ING", "Calibrated precision on our pool falls below target"],
  ["Decision policy", "Expected loss + value of information; Thompson only for ties", "Fixed rule table; bandits; safe RL", "Explainable, cost-aware, no simulator needed", "Track records show systematic regret against hindsight"],
  ["Rule language", "CEL", "Rego, custom YAML DSL", "Same language as the API-server fence; bounded cost", "Rules need joins or external data"],
  ["Mitigation verification", "Anytime-valid sequential tests; concurrent control when possible", "Fixed bake time; repeated Fisher; Mann-Whitney", "Valid under continuous monitoring", "A/A runs show miscalibration"],
  ["Lab verdict", "Paired discordant-block e-process + confidence sequences", "Fixed 20 runs; one-sample SPRT", "Doesn't need the baseline rate; bounded error", "Run counts too high in practice: invest in amplification"],
  ["Dependency faking", "Own replay for HTTP, gRPC, Redis; real seeded databases", "Keploy fork, MockServer, Microcks", "Grading guide and fidelity; avoids protocol-level DB faking", "Measured fidelity of our faker trails MockServer: keep MockServer behind the interface"],
  ["Isolation", "Namespace pool + default deny + probe + user namespaces; vCluster optional", "vCluster always; separate cluster", "Portable; vCluster shared nodes aren't a security boundary", "Hostile multi-tenant use cases appear"],
  ["Capture", "OBI 1.0 opt-in payloads + our exporter", "Own eBPF; Pixie; Keploy", "Maintained, Apache-2.0, just reached 1.0", "OBI's export limits block replay: Pixie or own capture"],
  ["Fix order", "Config restore, partial revert, templates, mined patterns, LLM", "Revert first; LLM first", "Precision prior; SapFix revert rejections", "Reviewer acceptance data says otherwise"],
  ["Builds", "Repo CI provenance by default; rootless BuildKit opt-in; overlays for interpreted code", "Kaniko; ko/Jib rebuilds", "Fidelity and security; Kaniko archived", "Build-parity check fails often"],
  ["Storage", "Event-sourced Ledger (SQLite or Postgres) + CRDs for intent", "Everything in CRDs; external database", "etcd limits; auditability", "Scale requires Postgres by default"],
  ["Audit", "Merkle log with external checkpoints", "Hash chain", "Logarithmic proofs; survives restarts", "Too complex for the timeline: hash chain plus checkpoints"],
  ["Default autonomy", "Shadow first; per-namespace ladder", "Act from minute one", "Trust surveys; safety", "Users want faster starts: opt-in 'act on reversible from day one'"],
  ["License", "Apache-2.0", "MIT, AGPL", "CNCF requirement; patent grant", "A hosted business model is ever chosen"],
];

function Decisions() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Decision log: chosen, alternatives, and when to change our minds</H2>
        <Table
          headers={["Decision", "Chosen", "Alternatives", "Why", "Revisit if"]}
          rows={DECISIONS.map(([a, b, d, e, f]) => [cb(a), c(b), cs(d), c(e), cs(f)])}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Corrections this research made to earlier beliefs</H3>
          <Bullets
            items={[
              "STRATUS commits an action when severity doesn't get worse, not when it improves, so Sentinel adds an improvement requirement.",
              "Narya's A/B testing and its bandit are separate modes, and its authors call A/B safer.",
              "Argo CD 3.1.0 ignores automated.enabled: false; ApplicationSets override syncPolicy only without ignoreApplicationDifferences.",
              "OBI exports opt-in payload attributes (but never SQL result rows); open-source Keploy only mocks HTTP and MySQL.",
              "The change closest in time is a poor culprit guess (9.8% at ING).",
              "Drain is a 2019-era winner; newer parsers beat it on accuracy.",
              "nodes/proxy RBAC enables exec; kube-dns forwarding is an exfiltration path from the Lab.",
              "v1's fix gates rely on LLM-written tests and an LLM reviewer, and its sandbox gate fails open.",
              "SWE-bench Verified is retired; LLM judges accept 17–22% of overfitting patches.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Open questions for the supervisor and team</H3>
          <Bullets
            items={[
              "Is the Lab accepted as the core contribution, with fix and verify in scope (Dr. Ayman's notes once listed them as 'good to have')?",
              "Which clusters can we test on beyond laptops (student cloud credits, the CUFE AI Lab cluster)?",
              "Who acts as the blind study's game master and fault authors, and who reviews patches?",
              "Default autonomy at install: shadow only, or act on reversible actions from day one?",
              "First two Fixer languages: Go and Python?",
              "LLM: none, local, or API for the evaluation's LLM-on runs?",
            ]}
          />
        </Stack>
      </Grid>
    </Stack>
  );
}

/* ------------------------------ Sources ------------------------------ */

type S = [string, string, string];
const SOURCES: [string, S[]][] = [
  ["Incidents", [
    ["Microsoft Teams incidents (SoCC'22)", "Mitigation mostly without code fixes", "https://www.microsoft.com/en-us/research/wp-content/uploads/2022/09/3542929.3563482.pdf"],
    ["What bugs cause production cloud incidents (HotOS'19)", "Bug classes; 56% mitigated without a patch", "https://www.microsoft.com/en-us/research/publication/what-bugs-cause-production-cloud-incidents/"],
    ["Google SRE postmortem analysis", "Triggers: binary 37%, config 31%", "https://sre.google/workbook/postmortem-analysis/"],
    ["Mutiny! How does Kubernetes fail", "Kubernetes failure taxonomy", "https://arxiv.org/abs/2404.11169"],
    ["Configuration defects in Kubernetes", "719 defects, 15 categories", "https://arxiv.org/abs/2512.05062"],
    ["Kubernetes failure stories (k8s.af)", "Public postmortems", "https://k8s.af/"],
    ["Metastable failures (OSDI'22)", "Retry storms and overload", "https://www.usenix.org/conference/osdi22/presentation/huang-lexiang"],
  ]],
  ["Watcher", [
    ["SPOT/DSPOT (KDD'17)", "Calibrated tail thresholds", "https://dl.acm.org/doi/10.1145/3097983.3098144"],
    ["MSTL", "Multiple seasonal decomposition", "https://arxiv.org/abs/2107.13462"],
    ["Loghub-2.0 parser benchmark", "Drain vs newer parsers", "https://arxiv.org/abs/2308.10828"],
    ["ByteBrain log parsing", "Fast hierarchical templates", "https://arxiv.org/abs/2504.09113"],
    ["Sentry event grouping", "Fingerprint design", "https://docs.sentry.io/concepts/data-management/event-grouping/"],
    ["nodes/proxy exec risk", "Why never to request it", "https://grahamhelton.com/blog/nodes-proxy-rce"],
  ]],
  ["Brain", [
    ["BARO (FSE'24)", "Robust deviation root cause", "https://arxiv.org/abs/2405.09330"],
    ["RCAEval (WWW'25)", "Root-cause benchmark", "https://arxiv.org/abs/2412.17015"],
    ["Root cause critique (ASE'24)", "Simple beats causal", "https://arxiv.org/abs/2408.13729"],
    ["Gandalf (NSDI'20)", "Change attribution at Azure", "https://www.usenix.org/conference/nsdi20/presentation/li"],
    ["ChangeRCA", "Change-induced incident localization", "https://dl.acm.org/doi/full/10.1145/3643728"],
    ["Narya (OSDI'20)", "Mitigation A/B tests and bandits", "https://www.usenix.org/system/files/osdi20-levy.pdf"],
    ["STRATUS (NeurIPS'25)", "Undo-if-worse mitigation", "https://arxiv.org/abs/2506.02009"],
    ["Netflix sequential testing", "Anytime-valid count tests", "https://arxiv.org/abs/2205.14762"],
  ]],
  ["Lab", [
    ["Confidence sequences (Howard et al.)", "Time-uniform intervals", "https://arxiv.org/abs/1810.08240"],
    ["Safe testing / e-values", "Optional stopping", "https://arxiv.org/abs/1906.07801"],
    ["Qi et al. (ISSTA'15)", "Plausible patches delete functionality", "https://doi.org/10.1145/2771783.2771791"],
    ["PATCH-SIM", "Overfitting patch detection", "https://arxiv.org/abs/1706.09120"],
    ["Anduril (SOSP'24)", "Reproducing production failures", "https://doi.org/10.1145/3694715.3695979"],
    ["NetworkPolicy docs", "Semantics and allow-all egress", "https://kubernetes.io/docs/concepts/services-networking/network-policies/"],
    ["User namespaces", "hostUsers: false", "https://kubernetes.io/docs/concepts/workloads/pods/user-namespaces/"],
  ]],
  ["Fixer", [
    ["SapFix (ICSE-SEIP'19)", "Templates, mutation, reverts at Meta", "https://doi.org/10.1109/ICSE-SEIP.2019.00039"],
    ["Getafix (OOPSLA'19)", "Mined fix patterns", "https://arxiv.org/abs/1902.06111"],
    ["Passerine (Google)", "Agentic repair at Google", "https://arxiv.org/abs/2501.07531"],
    ["Abstain and Validate", "Abstention and validation gains", "https://arxiv.org/abs/2510.03217"],
    ["CrashLocator (ISSTA'14)", "Crash-stack expansion", "https://dl.acm.org/doi/10.1145/2610384.2610386"],
    ["Kustomize build metadata", "Origin annotations for config repair", "https://kubectl.docs.kubernetes.io/references/kustomize/kustomization/buildmetadata/"],
    ["SWE-bench Verified retirement", "Why public benchmarks mislead", "https://openai.com/index/why-we-no-longer-evaluate-swe-bench-verified/"],
  ]],
  ["Platform, evaluation, market", [
    ["RBAC good practices", "Escalation pitfalls", "https://kubernetes.io/docs/concepts/security/rbac-good-practices/"],
    ["ValidatingAdmissionPolicy", "The API-server fence", "https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/"],
    ["AI Oops (telemetry manipulation)", "Attacks on LLM-driven AIOps", "https://arxiv.org/abs/2508.06394"],
    ["KWOK", "Simulated nodes for stress tests", "https://kwok.sigs.k8s.io/"],
    ["AIOpsLab", "Benchmark", "https://github.com/microsoft/AIOpsLab"],
    ["SREGym", "Benchmark", "https://arxiv.org/abs/2605.07161"],
    ["Incident-Arena", "Durability-checked benchmark", "https://arxiv.org/abs/2610.00648"],
    ["Le et al. human patch evaluation", "Review methodology", "https://arxiv.org/abs/1805.05983"],
    ["CNCF Sandbox", "Requirements", "https://github.com/cncf/sandbox"],
  ]],
];

function Sources() {
  return (
    <Stack gap={16}>
      <H2>Key sources</H2>
      <Text tone="secondary">A curated subset of the roughly 300 sources the eight research tracks used. Vendor claims are marked as such where they appear in the text.</Text>
      {SOURCES.map(([group, list]) => (
        <Stack key={group} gap={6}>
          <H3>{group}</H3>
          <Table headers={["Source", "Used for", "Link"]} rows={list.map(([n, u, href]) => [cb(n), cs(u), <Link href={href}>open</Link>])} />
        </Stack>
      ))}
    </Stack>
  );
}
