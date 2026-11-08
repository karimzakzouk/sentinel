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
  Stat,
  Table,
  Text,
  computeDAGLayout,
  useCanvasState,
  useHostTheme,
  useMemo,
} from "cursor/canvas";
import type { TableRowTone } from "cursor/canvas";

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
          <span style={{ color: theme.text.tertiary, minWidth: 16, lineHeight: "20px" }}>{i + 1}.</span>
          <Text style={{ flex: 1, minWidth: 0 }}>{s}</Text>
        </Row>
      ))}
    </Stack>
  );
}

const TABS = [
  { id: "verdict", label: "Verdict" },
  { id: "design", label: "Design" },
  { id: "lab", label: "Lab (the core)" },
  { id: "eval", label: "Evaluation" },
  { id: "plan", label: "Build plan" },
  { id: "critique", label: "Critique" },
  { id: "sources", label: "Sources" },
];

export default function SentinelBlueprint() {
  const [tab, setTab] = useCanvasState<string>("tab", "verdict");
  return (
    <Stack gap={20} style={{ padding: 4, maxWidth: 1100 }}>
      <Stack gap={6}>
        <H1>Sentinel, redesigned from scratch</H1>
        <Text tone="secondary">
          Based on every reachable page of the Graduation Project Notion (including archive, market survey, Bad Deploys sub-pages and old task boards), about 60 cited claims checked against primary sources, a prior-art search, and a review of Sentinel v1 and the marketly-org repos. November 8, 2026.
        </Text>
      </Stack>
      <Row gap={6} wrap>
        {TABS.map((t) => (
          <Pill key={t.id} active={tab === t.id} onClick={() => setTab(t.id)}>
            {t.label}
          </Pill>
        ))}
      </Row>
      <Divider />
      {tab === "verdict" && <VerdictTab />}
      {tab === "design" && <DesignTab />}
      {tab === "lab" && <LabTab />}
      {tab === "eval" && <EvalTab />}
      {tab === "plan" && <PlanTab />}
      {tab === "critique" && <CritiqueTab />}
      {tab === "sources" && <SourcesTab />}
    </Stack>
  );
}

/* ------------------------------ Verdict ------------------------------ */

function VerdictTab() {
  return (
    <Stack gap={20}>
      <Callout tone="info" title="The verdict">
        Build Sentinel around the Lab. Keep the four parts from your Notion (Watcher, Brain, Lab, Fixer), but make the sealed Lab and its statistical verdicts the core contribution. The Brain stops the bleeding with reversible, owner-aware actions and no AI. The Fixer proposes config fixes first and code fixes second, each with a reproduction and a verdict attached. The LLM is optional: it only generates candidate fixes and never decides, acts, judges, or writes the test it is graded by.
      </Callout>

      <Grid columns={4} gap={16}>
        <Stat value="≈80%" label="of serious Teams incidents ended without a code or config fix (SoCC '22)" />
        <Stat value="56%" label="of bug-caused Azure incidents were mitigated without a patch (HotOS '19)" />
        <Stat value="18–26%" label="realistic proven-fix rate on real bugs (Google Passerine, Meta)" />
        <Stat value="0" label="isolation breaches the Lab may ever allow" tone="success" />
      </Grid>

      <Stack gap={10}>
        <H2>Why this shape</H2>
        <Numbered
          items={[
            "Most incidents end with a rollback, restart or infrastructure change, so stopping the bleeding automatically covers the most ground, and it needs no AI.",
            "Your professors asked for it. Dr. Youssef wants a sandbox-focused project with the LLM outside the core. Dr. Ayman wants fixes verified beyond 'the unit tests pass'.",
            "Your own market map found that existing tools diagnose and suggest, and very few prove. Proof is also what makes 'act on its own, page humans rarely' acceptable.",
            "Every part can be measured on its own, which answers your own critique that the contribution is thin without an evaluation framework.",
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Honest positioning</H2>
        <Grid columns={2} gap={16}>
          <Card>
            <CardHeader>What is genuinely yours</CardHeader>
            <CardBody>
              <Bullets
                items={[
                  "A deterministic Brain that acts correctly through Argo CD, Flux, Helm or plain Kubernetes: pause the owner, roll back, revert in Git, resume.",
                  "A Lab whose verdicts carry explicit error bounds: sequential tests instead of a fixed 20 runs, calibrated against two copies of the bad version.",
                  "Trigger-aware reproduction: startup, any-request, specific-request and load-dependent bugs each get the cheapest reproduction that can work.",
                  "Open, reproducible evaluation on public benchmarks plus your own scenario library, including the confidently-wrong rate.",
                ]}
              />
            </CardBody>
          </Card>
          <Card>
            <CardHeader>What already exists (cite it, don't claim it)</CardHeader>
            <CardBody>
              <Bullets
                items={[
                  "End-to-end detect, fix and PR: Datadog Bits, Sentry Seer Autofix, incident.io, and Speedscale Agent Factory, which already reproduces bugs from traffic snapshots.",
                  "Record and replay: Hindsight (NSDI '23), Keploy, Speedscale, Ant Group (ICSE-SEIP '22).",
                  "Mitigation tested against a control group: Narya (OSDI '20). Undo when worse: STRATUS (NeurIPS '25).",
                  "Accepting patches with a sequential test: Stockfish Fishtest has used SPRT for years.",
                ]}
              />
            </CardBody>
          </Card>
        </Grid>
      </Stack>

      <Callout tone="neutral" title="One-sentence pitch">
        Sentinel stops the bleeding with reversible, verified actions, and every permanent fix it proposes arrives with a reproduction and a statistically bounded verdict.
      </Callout>

      <Callout tone="danger" title="Fix these first (security)">
        <Stack gap={4}>
          <Text>The Notion pages 'Infrastructure' and 'CUFE AI Lab (public info collected)' contain a lab server password in plain text. Delete it and change the password.</Text>
          <Text>The marketly-org infra README publishes an API token. Rotate it.</Text>
          <Text>Sentinel v1's sandbox NetworkPolicy serializes to allow-all egress and copies production Secrets into the sandbox. Do not reuse that code as-is.</Text>
        </Stack>
      </Callout>

      <Text size="small" tone="tertiary">
        Not readable: one meeting-notes page linked from 'Meetings with Dr Ayman' is not shared with the Notion connection (and neither is the page ID in the original link). Images and the SentinelAI_v2.pptx attachment were not opened. Unrelated idea pages (Arabic Vision, Team Matching) were skipped.
      </Text>
    </Stack>
  );
}

/* ------------------------------ Design ------------------------------ */

function ArchitectureDiagram() {
  const theme = useHostTheme();
  const W = 176;
  const H = 52;
  const labels: Record<string, [string, string]> = {
    signals: ["Cluster signals", "pods, events, logs, requests"],
    watcher: ["Watcher", "collect, group, redact"],
    brain: ["Brain", "decide, act, verify (no AI)"],
    owners: ["Owner adapters", "Argo CD, Flux, Helm, K8s"],
    fixer: ["Fixer", "localize, candidates"],
    lab: ["Lab", "seal, reproduce, verdict"],
    pr: ["Pull request", "evidence for reviewers"],
    ledger: ["Ledger", "incidents, audit, timeline"],
  };
  const layout = useMemo(
    () =>
      computeDAGLayout({
        nodes: Object.keys(labels).map((id) => ({ id })),
        edges: [
          { from: "signals", to: "watcher" },
          { from: "watcher", to: "brain" },
          { from: "brain", to: "owners" },
          { from: "brain", to: "fixer" },
          { from: "brain", to: "ledger" },
          { from: "fixer", to: "lab" },
          { from: "lab", to: "pr" },
        ],
        direction: "horizontal",
        nodeWidth: W,
        nodeHeight: H,
        rankGap: 40,
        nodeGap: 16,
        padding: 8,
      }),
    [],
  );
  return (
    <svg
      width="100%"
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      style={{ display: "block", maxWidth: layout.width }}
    >
      <defs>
        <marker id="sentinel-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
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
            strokeWidth={1.25}
            fill="none"
            markerEnd="url(#sentinel-arrow)"
          />
        );
      })}
      {layout.nodes.map((n) => {
        const core = n.id === "lab";
        const [title, sub] = labels[n.id];
        return (
          <g key={n.id}>
            <rect
              x={n.x}
              y={n.y}
              width={W}
              height={H}
              rx={6}
              fill={core ? theme.fill.secondary : theme.fill.quaternary}
              stroke={core ? theme.accent.primary : theme.stroke.secondary}
              strokeWidth={core ? 1.5 : 1}
            />
            <text x={n.x + W / 2} y={n.y + 21} textAnchor="middle" fontSize={12} fontWeight={600} fill={theme.text.primary}>
              {title}
            </text>
            <text x={n.x + W / 2} y={n.y + 37} textAnchor="middle" fontSize={10.5} fill={theme.text.secondary}>
              {sub}
            </text>
          </g>
        );
      })}
    </svg>
  );
}

function DesignTab() {
  const tierTone: (TableRowTone | undefined)[] = [undefined, undefined, undefined, "info"];
  return (
    <Stack gap={22}>
      <Stack gap={10}>
        <H2>Architecture</H2>
        <ArchitectureDiagram />
        <Text size="small" tone="tertiary">
          Runtime layout: one controller Deployment (Brain, Fixer, Lab orchestrator, Ledger API), an optional node-agent DaemonSet, short-lived Lab namespaces, and a separate builder namespace. The Ledger records every step from every part; only the Brain edge is drawn. The Lab also serves the Brain by checking rollouts.
        </Text>
      </Stack>

      <Stack gap={10}>
        <H2>Works on any cluster: capability tiers</H2>
        <Table
          headers={["Tier", "Needs", "Gives you", "Works on"]}
          rowTone={tierTone}
          rows={[
            [cb("0 · API only"), c("Kubernetes API access"), c("Pod status, events (watched and stored, because the API keeps them only 1 hour), logs including the previous container, owner detection, every Brain action"), c("Every conformant cluster, including EKS Fargate, GKE Autopilot, AKS Automatic and OpenShift's default security")],
            [cb("1 · Node agent"), c("DaemonSet with read-only hostPath"), c("Log tailing from `/var/log/pods`, cgroup v2 memory, CPU throttling, OOM kills and pressure, redaction on the node"), c("kind, k3s, minikube, EKS on EC2, GKE Standard, AKS Standard; OpenShift after an SCC grant; Autopilot gets logs only")],
            [cb("2 · eBPF"), c("OBI 1.0: privileged or BPF capabilities, kernel 5.8+ with BTF"), c("Per-version request rate, errors and latency; traces for the dependency map; opt-in capture of HTTP/1 JSON bodies and SQL text for replay"), c("Same as tier 1, except GKE Autopilot (only allowlisted Grafana Beyla) and AKS Automatic")],
            [cb("Lab"), c("NetworkPolicy actually enforced; a registry the nodes can pull from"), c("Sealed reproduction and fix verification"), c("kind 0.24+, k3s, GKE Autopilot, AKS Automatic, OpenShift by default; EKS, GKE Standard, AKS Standard and minikube after enabling a policy engine")],
          ]}
        />
        <Callout tone="info">
          The install prints a capability report and never pretends. If NetworkPolicy is not enforced, the Lab refuses to run candidates and every verdict is capped at 'not sure'.
        </Callout>
      </Stack>

      <Stack gap={14}>
        <H2>The parts</H2>

        <CollapsibleSection title="Watcher: collect what matters, store almost nothing" defaultOpen>
          <Bullets
            items={[
              "Tier 0: informers on Pods, Deployments, ReplicaSets, StatefulSets, Jobs, HPAs, Nodes and Argo/Flux objects. Persist Events. Fetch logs of failing pods, including the previous container. Probe for metrics.k8s.io (absent by default on EKS, kind and minikube).",
              "Snapshot every referenced ConfigMap version (redacted) and hash Secrets. Kubernetes keeps no ConfigMap history and `rollout undo` does not restore it.",
              "Tier 1: tail `/var/log/pods`; assemble multi-line stack traces per language (Google's fluentd plugin for this is archived; use Fluent Bit's parsers as the reference); read cgroup v2 files, falling back when `memory.peak` is missing (kernels before 5.19).",
              "Fingerprint crashes by exception type plus top in-app frames. Group log lines with a Drain-style miner as a fast baseline only: Drain is no longer the most accurate parser (LILAC and LibreLog beat it on Loghub-2.0).",
              "Redact before anything leaves the node: entropy and key-name rules (detect-secrets thresholds 4.5 for base64, 3.0 for hex), hashed user IDs, sanitized SQL text. Test with planted fake secrets and expect zero hits in everything Sentinel stores or sends.",
              "No time-series database. Keep short rolling windows in memory, use Prometheus if it is already there, and store only incidents and evidence.",
              "Detect crash loops from status reasons and restart-count changes, not from backoff timing, which is configurable per node since Kubernetes 1.35.",
            ]}
          />
        </CollapsibleSection>

        <CollapsibleSection title="Brain: decide and stop the bleeding, with no AI" defaultOpen>
          <Stack gap={12}>
            <Bullets
              items={[
                "One incident per root cause: group by workload, fingerprint and time window; build a dependency map from ownership, env vars, Service DNS names, logs and traces; open one incident on the upstream cause, with the others attached as symptoms.",
                "Severity: Google's burn-rate windows when a target exists (page at 14.4× over 1 h and 5 min, or 6× over 6 h and 30 min; ticket at 1× over 3 days and 6 h). Otherwise state-based rules, with an override for low-traffic services.",
                "What changed: a change log of pod templates, images, ConfigMaps, Secrets (hash only), HPAs and Argo/Flux revisions. A deploy is blamed only if symptoms start after it and appear only on the new revision's pods.",
                "Rules as data: your 51 scenarios plus B9–B12 become YAML rules, each with a test that injects the scenario and checks the decision.",
                "Reversible actions only: owner-aware rollback, rolling restart, bounded scale-out, temporary memory-limit raise (only when memory is flat at the limit, not climbing), pause a rollout, abort an Argo Rollout, restore a ConfigMap snapshot, turn a flag off when OpenFeature or flagd is present.",
                "Preconditions: namespace allowlist (system namespaces always denied); previous revision recorded as healthy; no irreversible change in the release (B9: migrations, contract changes); back off after a recent human edit; circuit breaker after two failed actions; tier-0 services need approval or an 'acting in 5 minutes unless stopped' timer.",
                "Verify the way STRATUS does, without the LLM: snapshot, act, watch error-budget burn with a sequential test, undo and escalate if it got worse or did nothing.",
                "Track record: a Beta(successes, failures) per action type and workload kind decides when an action may run unattended.",
              ]}
            />
            <Card>
              <CardHeader>Rolling back through whatever owns the workload</CardHeader>
              <CardBody>
                <Table
                  framed={false}
                  headers={["Owner", "Detect by", "Procedure"]}
                  rows={[
                    [cb("Argo Rollouts / Flagger"), c("ownerReferences on the ReplicaSet"), c("Defer to their analysis or call abort; still open a Git revert PR.")],
                    [cb("Argo CD"), c("`argocd.argoproj.io/tracking-id` annotation or `app.kubernetes.io/instance` label"), c("Set `automated.enabled=false` through the Argo API, roll back by history ID, verify, open a revert PR, re-enable auto-sync only after it merges. ApplicationSet-generated apps need `ignoreApplicationDifferences` on syncPolicy; otherwise escalate.")],
                    [cb("Flux HelmRelease"), c("`helm.toolkit.fluxcd.io/name` labels"), c("Suspend (with Flux's field manager), `helm rollback`, verify, revert Git, resume.")],
                    [cb("Flux Kustomization"), c("`kustomize.toolkit.fluxcd.io/name` labels"), c("Suspend, `rollout undo`, verify, revert Git, resume.")],
                    [cb("Helm without GitOps"), c("`meta.helm.sh` annotations plus the release Secret"), c("`helm rollback`, which also restores ConfigMaps. Argo-rendered charts carry `managed-by: Helm` too, so never trust that label alone.")],
                    [cb("Plain Deployment"), c("None of the above"), c("`rollout undo` only if the pod template changed. For ConfigMap-only changes, restore Sentinel's snapshot and restart the affected pods.")],
                  ]}
                />
              </CardBody>
            </Card>
          </Stack>
        </CollapsibleSection>

        <CollapsibleSection title="Fixer: propose, never merge by default" defaultOpen>
          <Bullets
            items={[
              "Abstain unless the cause is a config value or a crash in the app's own code, the source can be found, and the Lab has reproduced the problem.",
              "Find the source with a fallback chain, because the OCI labels the Notion plan relies on are usually missing (none of Online Boutique, OTel Demo, Bookinfo or Sock Shop carry them): image labels or annotations, then BuildKit provenance attestation, then binary metadata (Go `vcs.revision`, Java `git.properties`), then SHA-looking image tags, then a one-time `sentinel.dev/source` annotation, otherwise a diagnosis-only issue. The good commit comes from the previous ReplicaSet's image.",
              "Localize: stack frames at the bad commit intersected with diff(good..bad), ranked; tree-sitter extracts the function, its callers and its signatures.",
              "Candidates, best evidence first: restore a previous config value; templates (null guard, missing key, bounds check, unhandled error or timeout with a fallback, renamed setting) that copy how nearby code handles the case; LLM candidates (5–10, with failure output fed back, capped by Lab time rather than '2 attempts'); partial revert as a labeled fallback. In SapFix, developers declined all 18 revert proposals.",
              "Cheap filters before the Lab: compile, the repo's own unit tests, in-process replay when possible. Only the best 1–3 candidates reach the Lab.",
              "Build what production builds: the repo's Dockerfile (rootless BuildKit where allowed) or patch the production image (crane append for code-only Python and Node fixes). Rebuilding with ko or Jib changes the artifact. Upstream Kaniko is archived.",
              "Pull request: diff, a deterministic regression test made from the captured request (in the repo's own framework when possible), the evidence bundle, the verdict and the risks. Reviewers come from CODEOWNERS.",
              "Auto-merge is opt-in per fix class after a clean track record, starting with config restores.",
            ]}
          />
        </CollapsibleSection>

        <CollapsibleSection title="Ledger: memory, audit and the timeline">
          <Bullets
            items={[
              "SQLite (single replica) plus evidence bundles on a volume. Lab artifacts expire after 3 days; incidents are kept.",
              "One timeline per incident: signals, decision, action, verification, Lab runs, PR. This is also the demo screen for the defense.",
              "One Slack or webhook message per incident, updated in a thread.",
              "Sentinel's own security: API auth is mandatory (v1 failed open), webhook signatures are mandatory (v1 skipped them when the secret was empty), file reads are confined with `os.OpenRoot` (v1 followed symlinks), and RBAC is split between controller, Lab and builder with least privilege.",
            ]}
          />
        </CollapsibleSection>
      </Stack>

      <Stack gap={10}>
        <H2>Autonomy policy</H2>
        <Text tone="secondary">This is how 'fix as much as possible, page humans rarely' becomes concrete.</Text>
        <Table
          headers={["Kind of action", "Default", "Can it be relaxed?"]}
          rowTone={["success", "success", "success", "info", "danger", "neutral"]}
          rows={[
            [c("Restart, scale out, pause a rollout, abort a canary"), c("Automatic in allowlisted namespaces; undone if nothing improves"), c("Already automatic")],
            [c("Rollback through the owner"), c("Automatic when the change-correlation gate passes and the release is reversible"), c("Tier-0 services: approval or a stop timer")],
            [c("Restore a ConfigMap value, temporary memory-limit raise"), c("Automatic, plus a PR to make it permanent"), c("Already automatic")],
            [c("Code fix"), c("Pull request with evidence"), c("Opt-in auto-merge per fix class after a clean record")],
            [c("Anything irreversible: deleting data, migrations, secret rotation, node actions"), c("Never; escalate with evidence"), c("No")],
            [c("Third-party outage"), c("No action; one status note that closes itself"), c("No")],
          ]}
        />
        <Text size="small" tone="tertiary">
          A human is paged only when users are hurting right now and a human could do something Sentinel cannot.
        </Text>
      </Stack>
    </Stack>
  );
}

/* ------------------------------ Lab ------------------------------ */

function LabTab() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>The Lab is the project</H2>
        <Text>
          Everything else feeds the Lab or acts on its verdicts. It answers one question with explicit error bounds: does this bug really happen, and does this candidate really fix it without breaking normal behavior?
        </Text>
      </Stack>

      <Stack gap={10}>
        <H3>The eight steps of a Lab run</H3>
        <Table
          headers={["Step", "What happens", "Why"]}
          rows={[
            [cb("1. Seal"), c("A new namespace per run: default-deny ingress and egress except the stub service, ResourceQuota and LimitRange, Pod Security 'restricted', no service-account token, generated fake secrets (production secrets are never copied), gVisor or Kata RuntimeClass when available."), cs("Replaying a checkout request against real dependencies could charge a real card.")],
            [cb("2. Prove isolation"), c("A probe Job must fail to reach the internet, external DNS, the API server, node IPs, cloud metadata (169.254.169.254) and production ClusterIPs, over TCP and UDP. Repeated at teardown."), cs("NetworkPolicy without an enforcing plugin silently does nothing; on EKS, new pods start open unless strict mode is on.")],
            [cb("3. Virtualize dependencies"), c("Sentinel's own stub server answers HTTP calls from recordings. Databases come from migrations, a read-only schema copy, or an opt-in masked subset. Caches start empty. Unrecorded calls are logged as misses."), cs("Open-source Keploy only mocks HTTP and MySQL and has no Kubernetes mode, so it cannot be the engine.")],
            [cb("4. Classify the trigger"), c("Startup crash, any-request crash, specific-request crash, load or time dependent, data dependent."), cs("Picks the cheapest reproduction that can work.")],
            [cb("5. Reproduce"), c("Run the exact bad image by digest. A sequential test stops as soon as the bug is shown to fire, or shown not to."), cs("A fixed 20 runs misses a bug that fires 5% of the time in 36% of attempts.")],
            [cb("6. Race candidates"), c("Candidates run interleaved. Each is eliminated as soon as the same sequential test shows it still fails."), cs("Cheap or obviously wrong candidates don't burn the budget.")],
            [cb("7. Judge behavior"), c("Three arms: two copies of the bad (or previous good) version and the candidate. Differences that also appear between the two copies are noise. Latency, errors and memory are judged with effect sizes, not only p-values."), cs("Catches fixes that pass by deleting functionality: 104 of 110 'plausible' GenProg patches did exactly that.")],
            [cb("8. Verdict"), c("Pass, fail or not sure, with the evidence bundle. 'Not sure' never becomes a PR."), cs("The Lab never guesses.")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H3>Reproduction depends on the trigger</H3>
        <Table
          headers={["Trigger", "What the Lab needs", "Example", "Expected coverage"]}
          rowTone={["success", "success", "warning", "warning", "danger"]}
          rows={[
            [cb("Startup crash"), c("Only the image and its config"), c("B2 missing env var, bad config value, bad probe"), c("High")],
            [cb("Any-request crash"), c("Generic traffic to health or index routes"), c("Crash in middleware or a shared code path"), c("High")],
            [cb("Specific-request crash"), c("The captured request: an OBI JSON body at tier 2, or a request a human pastes in"), c("KeyError on one malformed payload (D3)"), c("Medium; depends on capture")],
            [cb("Load or time dependent"), c("Recorded request shapes replayed at rate, with concurrency"), c("Leak (A2), race (D8), pool exhaustion (A8)"), c("Medium; needs the statistical tests")],
            [cb("Data dependent"), c("The database rows the request touched"), c("A bug that only one odd row triggers"), c("Low unless the team opts into masked subsets")],
          ]}
        />
        <Text size="small" tone="tertiary">Report coverage per class. An honest 'not sure' rate is part of the result, not a failure of the project.</Text>
      </Stack>

      <Stack gap={10}>
        <H2>The verdict procedure: the algorithm you can point at</H2>
        <Text>
          It combines known statistics (Wald's sequential test, Diffy's noise baseline, Kayenta-style judgment). What is yours is applying them to fix verification for non-deterministic production bugs, with three verdicts and explicit error bounds. Call it new engineering, not new mathematics.
        </Text>
        <Bullets
          items={[
            "Reproduction: test 'the bug fires at most 1% of the time' against 'it fires at least p₁' on the bad image. Stop when the likelihood ratio crosses ln 19 either way (α = β = 0.05) or the budget runs out, which means 'not sure'. Rare bugs get amplified with load or concurrency.",
            "Fix acceptance: test 'the candidate fails as often as the bad image' against 'the candidate fails at most 1% of the time', with the same stopping rule. The chart below shows how many passing runs that takes.",
            "Behavior: replay recorded normal requests interleaved across the three arms. A response field counts as changed only if candidate-vs-baseline differs more than baseline-vs-baseline.",
            "Latency: Brunner–Munzel or Mann-Whitney with a Hodges–Lehmann interval and a tolerance band (Kayenta uses ±25%), plus an effect size (Vargha–Delaney A12) and a bootstrap check on p99, because rank tests barely notice tail regressions.",
            "Memory: the Theil–Sen slope of memory under replayed load, compared between arms. A leak fix must flatten the slope.",
            "Several metrics at once: Holm correction. Pass only if every check passes; fail if the trigger still fires; otherwise not sure.",
            "Calibration: regularly run two identical versions against each other to measure the real false-alarm rate, and publish it.",
          ]}
        />
        <Grid columns={2} gap={20}>
          <Stack gap={6}>
            <Text weight="semibold">Passing runs needed to accept a fix</Text>
            <BarChart
              categories={["90%", "50%", "20%", "10%", "5%", "2%"]}
              series={[
                { name: "Sequential test (SPRT)", data: [2, 5, 14, 31, 72, 291] },
                { name: "Fixed rule in the Notion plan", data: [20, 20, 20, 20, 20, 20], tone: "neutral" },
              ]}
              showValues
              height={220}
            />
            <Text size="small" tone="tertiary">
              Horizontal axis: how often the bug fires on the bad version. Vertical axis: consecutive passing runs needed (count). Computed with Wald's SPRT, α = β = 0.05, where 'fixed' means failing 1% of the time or less. Not measured data. A fixed 20 runs wastes runs on clear crashes and cannot prove fixes for rare bugs.
            </Text>
          </Stack>
          <Stack gap={6}>
            <Text weight="semibold">Chance that 20 runs never see the bug at all</Text>
            <BarChart
              categories={["90%", "50%", "20%", "10%", "5%", "2%"]}
              series={[{ name: "Probability of zero failures in 20 runs", data: [0, 0, 1.2, 12.2, 35.8, 66.8], tone: "warning" }]}
              valueSuffix="%"
              height={220}
            />
            <Text size="small" tone="tertiary">
              Horizontal axis: how often the bug fires. Vertical axis: probability (%) that 20 runs on the bad version show zero failures, computed as (1 − p)^20. Also, 0 failures in 20 runs only shows the failure rate is below 13.9% (95% bound).
            </Text>
          </Stack>
        </Grid>
      </Stack>

      <Stack gap={10}>
        <H3>Isolation depends on the cluster</H3>
        <Table
          headers={["Cluster", "NetworkPolicy enforced out of the box?", "What to do"]}
          rowTone={["success", "success", "warning", "warning", "success", "warning", "warning", "success", "success"]}
          rows={[
            [cb("kind"), c("Yes, since v0.24 (kindnetd)"), c("Nothing")],
            [cb("k3s"), c("Yes (embedded kube-router)"), c("Nothing, unless started with `--disable-network-policy`")],
            [cb("minikube"), c("No (bridge CNI by default)"), c("Start with `--cni=calico`")],
            [cb("EKS"), c("No; opt-in, and new pods start open unless strict mode is set"), c("Enable network policy with `NETWORK_POLICY_ENFORCING_MODE=strict`; not available on Fargate")],
            [cb("GKE Autopilot"), c("Yes (Dataplane V2)"), c("Nothing")],
            [cb("GKE Standard"), c("Only with Dataplane V2 or Calico"), c("Choose Dataplane V2 at creation or enable Calico")],
            [cb("AKS Standard"), c("No"), c("Enable a policy engine; it can be added later but reimages nodes")],
            [cb("AKS Automatic"), c("Yes (Cilium)"), c("Nothing")],
            [cb("OpenShift"), c("Yes (OVN-Kubernetes)"), c("Nothing")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H3>Building candidate images</H3>
        <Table
          headers={["Method", "Strength", "Limits"]}
          rows={[
            [cb("Repo's Dockerfile with rootless BuildKit"), c("Highest fidelity to what production runs"), c("Needs Unconfined seccomp and AppArmor plus user namespaces; violates Pod Security 'baseline'")],
            [cb("crane append onto the production image"), c("Exact production base; unprivileged; fast"), c("Code-only Python and Node fixes; not for dependency, native-module or bundled changes")],
            [cb("ko or Jib"), c("Fast and unprivileged"), c("Only when the repo already builds this way; otherwise the Lab tests a different artifact")],
            [cb("Kaniko"), c("Familiar from v1"), c("Upstream archived June 2025; forks exist (Chainguard, osscontainertools); runs as root")],
          ]}
        />
        <Text size="small" tone="tertiary">
          Push to a registry the nodes already trust (ECR, GAR, ACR, GHCR, or OpenShift's registry); a registry inside kind needs per-node containerd configuration. Build in a separate namespace, because candidate code runs during the build.
        </Text>
      </Stack>

      <Grid columns={2} gap={16}>
        <Card>
          <CardHeader>RolloutCheck (for the Brain)</CardHeader>
          <CardBody>
            <Text>
              While old and new pods run side by side, compare them with an anytime-valid sequential test on error counts (Netflix, KDD 2022) instead of re-running Fisher's test every interval, which inflates false alarms. If Argo Rollouts or Flagger is present, use their verdict.
            </Text>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>Checking live mitigations</CardHeader>
          <CardBody>
            <Text>
              The Lab cannot rehearse restarts or scaling, so verify them in production: snapshot, act, compare burn rate, undo if worse. Optionally act on some pods first and compare against untouched ones (Narya did this at Azure in 2020). Watch for load shifting onto the control pods and for fresh pods looking healthy no matter what.
            </Text>
          </CardBody>
        </Card>
      </Grid>
    </Stack>
  );
}

/* ------------------------------ Evaluation ------------------------------ */

function EvalTab() {
  return (
    <Stack gap={22}>
      <Stack gap={10}>
        <H2>Test systems</H2>
        <Table
          headers={["System", "Use it for", "Why", "Caveats"]}
          rows={[
            [cb("Online Boutique (11 services; Go, C#, Node, Python, Java)"), c("Main target for planted code bugs and the Fixer"), c("Small services, active (v0.10.7, Sep 2026), rebuilds from source with skaffold, runs on a laptop"), cs("Only two built-in faults; plant your own")],
            [cb("OpenTelemetry Demo (about 20 services, 11 languages)"), c("Watcher and Brain"), c("Very active (3.1.0, Sep 2026), rich telemetry, flagd failure flags such as paymentFailure, cartFailure, emailMemoryLeak, failedReadinessProbe, kafkaQueueProblems"), cs("Flags are runtime switches, not code bugs; needs about 6 GB RAM")],
            [cb("Marketly (your 9 services, 8 languages)"), c("Polyglot Fixer cases with ground truth"), c("Planted bugs with expected fixes already written down; Argo CD and a kind CI harness exist"), cs("Only 4 of 9 bugs fire; images are private; scoring is a regex on the diff; harness is tied to v1 and one person's secrets")],
            [cb("AIOpsLab and ITBench problems on kind"), c("Brain mitigation, comparable with STRATUS (69.2% and 50% with GPT-4o)"), c("Public and runnable locally"), cs("Mostly infrastructure and misconfiguration faults")],
            [cb("SREGym-Lite (17 problems; 8 vCPU, 16 GB)"), c("Lab and Fixer on public code-level cases"), c("Includes TrainTicket SQL bugs, a missing stale-cache fallback and a lock leak"), cs("The full SREGym prefers a real cluster")],
            [cb("TrainTicket (41 services)"), c("Optional stretch"), c("22 replicated industrial faults"), cs("Needs about 32 GB; slow builds")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Scenario library</H2>
        <Text>
          Your 51 scenarios plus B9–B12 become an automated suite that runs on kind in CI. Most expected outcomes stay as written. These are the changes I recommend:
        </Text>
        <Table
          headers={["Scenario", "Notion says", "Change to"]}
          rows={[
            [cb("A1 OOM from a low limit"), cs("Escalate"), c("Temporary raise capped by the quota when memory is flat at the limit, plus a PR")],
            [cb("A2 memory leak"), cs("Restart and escalate"), c("Restart, then a Lab leak test (memory slope under replayed load) and a fix PR when it reproduces")],
            [cb("A5 manual scale-down"), cs("Scale up"), c("Back off because a human edited it (F4); ask with a stop timer")],
            [cb("B1–B6 bad deploys"), cs("Rollback"), c("Rollback through the owner, plus restore-don't-invent forward fixes when the diagnosis is certain")],
            [cb("B7 bad ConfigMap value"), cs("Escalate"), c("Restore Sentinel's snapshot and restart the affected pods")],
            [cb("B9 rollback-unsafe release"), cs("Added later"), c("Never auto-rollback; detect migrations and contract changes before acting")],
            [cb("C7 third-party 5xx"), cs("Escalate or no-op"), c("No action, one status note, closes itself; page only if a human could help")],
            [cb("D8 race condition"), cs("Escalate"), c("Statistical reproduction under concurrency; fix PR if the sequential test passes")],
            [cb("D9 bug in a library"), cs("Escalate"), c("Try reverting the dependency bump when the diff contains one")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Metrics</H2>
        <Table
          headers={["Part", "Metric", "Target or note"]}
          rowTone={[undefined, undefined, "danger", undefined, undefined, undefined, "info"]}
          rows={[
            [cb("Watcher and Brain"), c("Detection recall and false alarms, including the E1–E7 non-incidents"), c("Report per category")],
            [cb("Brain"), c("Time to mitigate, mitigation success, actions undone by the no-regression check, wrong-action rate"), c("Wrong actions close to 0")],
            [cb("Lab"), c("Isolation breaches"), c("Must be 0")],
            [cb("Lab"), c("Verdict accuracy on planted good and bad fixes, including deletion-style fixes"), c("Every deletion-style fix must fail")],
            [cb("Lab"), c("Observed false-pass rate vs the configured α; runs per verdict vs a fixed 20"), c("Shows the error bound holds in practice")],
            [cb("Fixer"), c("Eligibility, proven-fix rate, false-fix rate (passed the Lab but wrong per ground truth), acceptance by reviewers outside the team, cost per incident"), c("Expect roughly 15–25% proven fixes on eligible real bugs, higher on planted simple bugs")],
            [cb("Overall"), c("Share of incidents that needed a human; confidently-wrong rate"), c("The headline numbers")],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Baselines</H3>
          <Bullets
            items={[
              "Plain Kubernetes, which only restarts.",
              "An LLM-only agent (HolmesGPT with remediation, or a STRATUS-style loop) on the same scenarios.",
              "Fixed 20-run verification against the sequential procedure.",
              "Sentinel with the LLM switched off, to show how much the deterministic parts do alone.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H3>Cluster matrix</H3>
          <Text>
            kind, k3s and minikube with Calico on laptops, plus one managed cluster if credits allow (the Marketly repo already has AKS Terraform). Show the capability report on each; that is the evidence for 'works on any cluster'. Ask whether the CUFE AI Lab cluster can host a real deployment.
          </Text>
        </Stack>
      </Grid>
    </Stack>
  );
}

/* ------------------------------ Build plan ------------------------------ */

function PlanTab() {
  return (
    <Stack gap={22}>
      <Stack gap={10}>
        <H2>Phases: each ends with something you can demo and measure</H2>
        <Table
          headers={["Phase", "Build", "Done when", "Needs"]}
          rows={[
            [cb("0"), c("Skeleton reusing v1's store, GitHub App and repo-CI code; kind harness with Online Boutique and a scenario injector; capability preflight"), c("The harness injects any scenario and records ground truth"), cs("Nothing")],
            [cb("1"), c("Watcher tier 0 and Brain detection, grouping and classification, in observe-only mode"), c("Detection recall and false-alarm numbers for categories A–E"), cs("Phase 0")],
            [cb("2"), c("Brain actions through every owner type, preconditions, no-regression verification"), c("Mitigation success and wrong-action rate; Argo and Flux procedures demoed"), cs("Phase 1")],
            [cb("3"), c("Lab: sealing and probe, startup and manifest experiments, HTTP stubs, sequential verdicts; config-fix path end to end"), c("Config fixes proven and opened as PRs; zero isolation breaches"), cs("Phase 2")],
            [cb("4"), c("Fixer code path: source resolution, localization, templates, builds, Lab verification, PRs; then LLM candidates"), c("Proven-fix and false-fix rates on planted code bugs"), cs("Phase 3")],
            [cb("5"), c("Node agent (tier 1), OBI integration and request capture (tier 2), database seeding, load replay, RolloutCheck"), c("Coverage of specific-request and load-dependent bugs"), cs("Phase 4")],
            [cb("6"), c("Stretch: vaccination, canary the cure, opt-in auto-merge for config restores, managed-cluster matrix"), c("Each stretch item measured on its own"), cs("Phase 5")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Team of four, split by part</H2>
        <Grid columns={4} gap={12}>
          <Card>
            <CardHeader>Person A</CardHeader>
            <CardBody>
              <Text size="small">Watcher (tiers 0–2), Ledger, timeline UI</Text>
            </CardBody>
          </Card>
          <Card>
            <CardHeader>Person B</CardHeader>
            <CardBody>
              <Text size="small">Brain: rules, owner adapters, safety, no-regression checks</Text>
            </CardBody>
          </Card>
          <Card>
            <CardHeader>Person C</CardHeader>
            <CardBody>
              <Text size="small">Lab: sealing, probe, stubs, statistics, RolloutCheck</Text>
            </CardBody>
          </Card>
          <Card>
            <CardHeader>Person D</CardHeader>
            <CardBody>
              <Text size="small">Fixer: source resolution, localization, candidates, builds, PRs</Text>
            </CardBody>
          </Card>
        </Grid>
        <Text size="small" tone="tertiary">
          Everyone owns one scenario category in the evaluation harness. Your current Notion split is by scenario category (bad deploys, dependency failures, infra, safety), which suits the harness but not the build.
        </Text>
      </Stack>

      <Stack gap={10}>
        <H2>What to keep from Sentinel v1</H2>
        <Table
          headers={["Packages", "Verdict", "Reason"]}
          rowTone={["success", "success", "success", "success", "success", "warning", "warning", "warning", "warning", "danger"]}
          rows={[
            [c("feedback (SQLite store), api, web"), cb("Reuse with changes"), c("New schema; fix the fail-open auth")],
            [c("github, repo, codeintel"), cb("Reuse with changes"), c("Fix symlink-following file reads")],
            [c("repoci, syntaxvalidate, depanalysis, metrics, logging, errs, events, version"), cb("Reuse as-is"), c("Solid utilities")],
            [c("errorparse, correlation, safety, slo, k8s, observability"), cb("Reuse with changes"), c("Seeds for the Watcher and Brain")],
            [c("agent, llm"), cb("Reuse with changes"), c("Optional LLM tier of the Fixer")],
            [c("detection"), cb("Rewrite"), c("No events, cgroups or capture; its watch is never restarted after it closes")],
            [c("safeactions"), cb("Rewrite"), c("Ignores GitOps owners; picks the ReplicaSet by label and age")],
            [c("executors (sandbox, Kaniko)"), cb("Rewrite"), c("Allow-all NetworkPolicy, copied production secrets, no replay or statistics; Kaniko archived")],
            [c("agents (LLM investigator and proposer)"), cb("Rewrite"), c("The LLM no longer decides")],
            [c("automerge, postmerge, chatops, prreview, slackbot, attest, admission, chaos, causal, emb, llmcost, tracing"), cb("Drop"), c("Outside the new scope (about 14k lines)")],
          ]}
        />
        <Text size="small" tone="tertiary">v1 is 64.9k lines of Go and builds and tests cleanly. Roughly a third is worth keeping.</Text>
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>Cut lines if you fall behind</H3>
          <Text>
            Drop in this order: managed-cluster matrix, canary the cure, vaccination, tier 2 capture (keep startup and any-request reproduction), LLM candidates (keep templates and config restores). Never drop the Lab's sealing and verdicts, owner-aware rollback, or the evaluation harness.
          </Text>
        </Stack>
        <Stack gap={8}>
          <H3>Decisions the team must make</H3>
          <Bullets
            items={[
              "Confirm with Dr. Ayman that the Lab is the core; his notes list fix and verify as 'good to have'.",
              "Default autonomy: I recommend acting immediately on reversible actions in allowlisted namespaces, with automatic undo, and PR-only for code.",
              "First two Fixer languages: Go and Python. Both are in Online Boutique and Marketly, and Python fixes can patch the production image.",
              "LLM by API or self-hosted: decide by measuring fix rate, false-fix rate and cost on your own bug set, as your Fix Module page already describes.",
              "A real cluster: the CUFE AI Lab cluster, or Marketly's AKS Terraform with student credits (or credits from the Siemens support request, if it is still open).",
            ]}
          />
        </Stack>
      </Grid>

      <Callout tone="neutral" title="What to tell the professors">
        Sentinel is a Kubernetes reliability system whose core is a sealed Lab. When something breaks, Sentinel first restores service with reversible, rule-based actions that go through the team's own deployment tools. It then reproduces the failure in isolation, and only proposes a permanent fix after a sequential statistical test shows the bug is gone and normal behavior is unchanged. Every part is evaluated separately on public benchmarks and our own scenario library, including how often it is confidently wrong.
      </Callout>
    </Stack>
  );
}

/* ------------------------------ Critique ------------------------------ */

function CritiqueTab() {
  return (
    <Stack gap={22}>
      <Stack gap={10}>
        <H2>Where I was wrong earlier in this conversation</H2>
        <Table
          headers={["What I said", "What is true"]}
          rows={[
            [c("OBI only records routes, status codes and timings"), c("OBI 1.0 (Oct 30, 2026) can export opt-in HTTP JSON bodies, raw SQL text and headers. It never exports SQL result rows or gRPC bodies.")],
            [c("Build your own eBPF recorder for technical depth"), c("Use OBI for capture and build only what it can't export. The depth belongs in the Lab, which is also what your market map and Dr. Youssef point to.")],
            [c("Use free Keploy for the Lab's fake dependencies"), c("Open-source Keploy mocks only HTTP and MySQL, has no Kubernetes mode, and needs eBPF capabilities.")],
            [c("Few tools prove fixes in an isolated sandbox"), c("Speedscale Agent Factory (May 2026) reproduces bugs from traffic snapshots and confirms fixes; incident.io runs a sandboxed coding agent.")],
            [c("Vaccination: maybe nobody on earth has done it"), c("Partly new at best: Big Sleep, BugStone and CodeQL variant analysis find bug variants; Filibuster and ChAP inject faults automatically.")],
            [c("ProofRace is a new algorithm"), c("Modest: Stockfish Fishtest has accepted patches by SPRT for years. The new part is applying it to verifying fixes for production bugs.")],
            [c("Canary the cure is new"), c("Narya (OSDI 2020) already A/B-tested mitigations against a control group at Azure.")],
            [c("Partial revert first (I went along with the Notion order)"), c("SapFix tried templates and mutation first, and developers declined all 18 revert proposals.")],
            [c("Swinging between 'Notion is the answer key' and 'Notion doesn't answer this'"), c("Neither. Notion answers many questions well and gets several wrong; see the next table.")],
            [c("Roleplay numbers (40 MB/hour, r = 0.94) and the Datadog line-count comparison"), c("Illustrations and guesses, not measurements.")],
          ]}
        />
        <Text size="small" tone="tertiary">
          Held up under checking: Argo CD blocks rollback while auto-sync is on; a GitOps revert PR alone is too slow to stop the bleeding; a fixed 20 runs is statistically weak; one LLM fix attempt costs cents (about $0.01 to $0.55 depending on the model).
        </Text>
      </Stack>

      <Stack gap={10}>
        <H2>Where the Notion plan is wrong or risky</H2>
        <Table
          headers={["Notion says", "Evidence", "Change"]}
          rows={[
            [c("Keploy and Testcontainers for stubs"), cs("Keploy's open-source edition: HTTP and MySQL only, no Kubernetes; Testcontainers needs a Docker daemon"), c("Own HTTP stub server; databases as pods built from migrations")],
            [c("Diffy-style A/B design, crediting Diffy"), cs("opendiffy is now CC BY-NC-ND: no modified redistribution"), c("Implement the three-arm comparison yourselves and cite Diffy")],
            [c("20 runs, all must fail or pass"), cs("Misses a 5% bug 36% of the time; 0 of 20 only bounds the failure rate below 13.9%"), c("Sequential test with a budget and a 'not sure' outcome")],
            [c("Fisher exact test for old vs new versions"), cs("Repeating it every interval inflates false alarms; Netflix moved to anytime-valid sequential tests"), c("Sequential test, minimum effect size, Holm correction")],
            [c("Mann-Whitney for latency"), cs("Sensitive to variance and skew differences and to ties; blind to p99"), c("Add a Hodges–Lehmann band, an A12 effect size, a bootstrap p99 check, interleaved requests")],
            [c("Partial revert first"), cs("SapFix reverts were declined 18 of 18; the Brain's rollback already mitigates"), c("Forward fixes first; revert as a labeled fallback")],
            [c("At most 2 LLM attempts"), cs("Gains continue up to about 40 samples when a verifier filters them (Agentless)"), c("5–10 candidates, budgeted by Lab time")],
            [c("Read repo and commit from OCI labels"), cs("Missing on all four standard demo apps checked"), c("Fallback chain plus a one-time annotation")],
            [c("Build with ko, Jib or crane"), cs("ko and Jib change the artifact; upstream Kaniko is archived"), c("Repo's Dockerfile or patch the production image")],
            [c("GitOps changes only through a revert PR"), cs("Too slow at 3 a.m.; Argo blocks rollback while auto-sync is on"), c("Pause, roll back through the owner, revert PR, resume")],
            [c("Drain is the most accurate log parser"), cs("A 2019 result; LILAC and LibreLog beat it on Loghub-2.0; Drain3 is dormant; Loki's Go port is AGPL"), c("Drain-style grouping as a fast baseline only")],
            [c("fluent-plugin-detect-exceptions for stack traces"), cs("Archived"), c("Own multi-line assembly, with Fluent Bit's parsers as the reference")],
            [c("OTel Operator auto-instrumentation"), cs("Needs annotations and pod restarts"), c("Opt-in only, not part of zero setup")],
            [c("Watcher page reads node files; Dependency Failures page says API only"), cs("Each is right for different clusters"), c("Tiers 0, 1 and 2 with a capability report")],
            [c("Gandalf: 92.4% precision, 100% recall"), cs("Data-plane rollouts only; control plane is 94.9% and 99.8%"), c("Cite both")],
            [c("Escalate A1, B7, C4–C8, D8–D10"), cs("Several have safe automatic actions"), c("Revisit per scenario (Evaluation tab)")],
            [c("Fix and verify are 'good to have' (Dr. Ayman's notes)"), cs("Detection and diagnosis are crowded; verification is the gap your own market map found"), c("Explain to Dr. Ayman why the Lab is the core")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Your own 'Let's destroy it' list, answered</H2>
        <Table
          headers={["Critique (Ideas page)", "Answer in this design"]}
          rows={[
            [cb("Huge scope"), c("Phases with exit criteria; every phase can be demoed and measured; explicit cut lines.")],
            [cb("The LLM confidently produces wrong answers"), c("The LLM never decides or judges; the Lab verifies; the confidently-wrong rate is a headline metric.")],
            [cb("The rollback problem"), c("Owner-aware procedures, a reversibility gate (B9), ConfigMap snapshots, undo when the burn rate does not improve.")],
            [cb("Novel contribution is thin without an evaluation framework"), c("The evaluation harness is a deliverable: scenario library on kind, public benchmarks, baselines.")],
            [cb("PagerDuty AIOps, Shoreline, Moogsoft and Squadcast do it, kind of"), c("They correlate alerts and run runbooks; none verify fixes. Position honestly: open, deterministic, statistically verified, measured.")],
            [cb("'Might end up trivial' (Dr. Youssef)"), c("A demo that only restarts pods would be trivial. Sealing, replay and sequential verdicts are not, and they are measurable.")],
          ]}
        />
      </Stack>

      <Stack gap={10}>
        <H2>Novelty verdicts for the ideas I pitched</H2>
        <Table
          headers={["Idea", "Verdict", "Closest prior work"]}
          rowTone={["warning", "warning", "warning", "danger", "danger"]}
          rows={[
            [cb("Vaccination: find and prove the same bug in other services"), c("Partly new (the combination)"), cs("Big Sleep (2024), BugStone (2025), CodeQL multi-repo variant analysis, Filibuster (SoCC '21), Anduril (SOSP '24)")],
            [cb("Statistical fix verification"), c("Partly new (modest)"), cs("Stockfish Fishtest SPRT, Uber Dr.Fix (fixed 1,000 reruns, PLDI '25), Netflix sequential tests (KDD '22)")],
            [cb("Canary the cure"), c("Partly new (narrow)"), cs("Narya (OSDI '20), STRATUS (NeurIPS '25)")],
            [cb("Rewind the outage"), c("Established"), cs("Hindsight (NSDI '23), Keploy, Speedscale, Ant Group (ICSE-SEIP '22)")],
            [cb("End-to-end detect, fix, PR"), c("Established"), cs("Datadog Bits, Sentry Seer, incident.io, Speedscale Agent Factory, SapFix (2019)")],
          ]}
        />
        <Text>
          Dr. Youssef's advice fits: the project should be different, not new research. Its difference is the proof, the determinism and the evaluation.
        </Text>
      </Stack>
    </Stack>
  );
}

/* ------------------------------ Sources ------------------------------ */

type Src = [string, "Verified" | "Corrected" | "Prior art" | "Benchmark", string, string];

const SOURCES: Src[] = [
  ["Nearly 80% of 152 Teams incidents mitigated without a code or config fix; rollback 22.4%", "Verified", "SoCC '22 paper", "https://www.microsoft.com/en-us/research/wp-content/uploads/2022/09/3542929.3563482.pdf"],
  ["Software bugs caused close to 40% of high-severity Azure incidents; 56% mitigated without a patch", "Verified", "HotOS '19", "https://dl.acm.org/doi/10.1145/3317550.3321438"],
  ["'Stopping the bleeding should be your first priority'", "Verified", "SRE book: Effective Troubleshooting", "https://sre.google/sre-book/effective-troubleshooting/"],
  ["'Roughly 70% of outages are due to changes in a live system'", "Verified", "SRE book: Introduction", "https://sre.google/sre-book/introduction/"],
  ["Burn-rate windows for a 99.9% SLO (14.4×, 6×, 1×)", "Verified", "SRE workbook: Alerting on SLOs", "https://sre.google/workbook/alerting-on-slos/"],
  ["Argo CD cannot roll back while automated sync is enabled", "Verified", "Argo CD auto-sync docs", "https://argo-cd.readthedocs.io/en/stable/user-guide/auto_sync/"],
  ["Argo CD tracks resources by tracking-id annotation or instance label", "Corrected", "Argo CD resource tracking", "https://argo-cd.readthedocs.io/en/stable/user-guide/resource_tracking/"],
  ["Flux HelmRelease remediation fires only when a Helm action fails", "Verified", "Flux HelmRelease docs", "https://fluxcd.io/flux/components/helm/helmreleases/"],
  ["Anytime-valid sequential testing for canaries", "Verified", "Netflix, KDD '22", "https://arxiv.org/abs/2205.14762"],
  ["STRATUS: 69.2% (AIOpsLab), 50% (ITBench), Transactional No-Regression", "Verified", "STRATUS, NeurIPS '25", "https://arxiv.org/abs/2506.02009"],
  ["NetworkPolicy without an implementing controller has no effect", "Verified", "Kubernetes docs", "https://kubernetes.io/docs/concepts/services-networking/network-policies/"],
  ["kind enforces NetworkPolicy since v0.24", "Verified", "kind v0.24.0 release", "https://github.com/kubernetes-sigs/kind/releases/tag/v0.24.0"],
  ["EKS network policy is opt-in; strict mode needed for new pods", "Verified", "EKS docs", "https://docs.aws.amazon.com/eks/latest/userguide/cni-network-policy-configure.html"],
  ["Open-source Keploy mocks only HTTP and MySQL; Kubernetes recording is Enterprise", "Corrected", "Keploy comparison", "https://keploy.io/integration-testing"],
  ["About 1.5% of Google test runs report a flaky result", "Verified", "Google Testing Blog", "https://testing.googleblog.com/2016/05/flaky-tests-at-google-and-how-we.html"],
  ["104 of 110 plausible GenProg patches were equivalent to deleting functionality", "Verified", "Qi et al., ISSTA '15", "https://people.csail.mit.edu/rinard/paper/issta15.pdf"],
  ["Kayenta judges with Mann-Whitney and a ±25% tolerance band", "Verified", "Spinnaker canary judge", "https://spinnaker.io/docs/guides/user/canary/judge/"],
  ["OBI 1.0 exports opt-in payload attributes (JSON bodies, SQL text), never SQL result rows", "Corrected", "OBI attribute docs", "https://opentelemetry.io/docs/zero-code/obi/configure/metrics-traces-attributes/"],
  ["OBI on Kubernetes needs privileged or BPF capabilities and kernel 5.8+", "Verified", "OBI Kubernetes setup", "https://opentelemetry.io/docs/zero-code/obi/setup/kubernetes/"],
  ["Kubernetes keeps Events for 1 hour by default", "Verified", "kube-apiserver reference", "https://kubernetes.io/docs/reference/command-line-tools-reference/kube-apiserver/"],
  ["cgroup v1 deprecated in Kubernetes 1.35", "Verified", "Kubernetes cgroups docs", "https://kubernetes.io/docs/concepts/architecture/cgroups/"],
  ["Drain outperformed by LILAC on Loghub-2.0", "Corrected", "LILAC, FSE '24", "https://dl.acm.org/doi/10.1145/3643733"],
  ["GKE Autopilot blocks privileged pods and hostPath except read-only /var/log", "Verified", "GKE Autopilot security", "https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-security"],
  ["EKS Fargate does not run DaemonSets", "Verified", "EKS Fargate docs", "https://docs.aws.amazon.com/eks/latest/userguide/fargate.html"],
  ["SapFix: developers declined all 18 revert proposals; templates and mutation came first", "Verified", "SapFix paper", "https://discovery.ucl.ac.uk/id/eprint/10084761/1/SapFix-Automated-End-to-End-Repair-at-Scale-v2.pdf"],
  ["Passerine: 17.9% correct on human-reported bugs at Google", "Verified", "Passerine", "https://arxiv.org/abs/2501.07531"],
  ["Meta Engineering Agent: 25.5% of generated fixes landed", "Verified", "Meta, 2025", "https://arxiv.org/abs/2507.18755"],
  ["Agentless: sampling 40 patches with a verifier keeps adding fixes", "Verified", "Agentless", "https://arxiv.org/abs/2407.01489"],
  ["Abstain and Validate: up to +39 percentage points", "Verified", "Google, ICSE-SEIP '26", "https://arxiv.org/abs/2510.03217"],
  ["OCI source and revision annotations (rarely present in practice)", "Verified", "OCI image spec", "https://github.com/opencontainers/image-spec/blob/main/annotations.md"],
  ["Narya A/B-tested mitigations against a control group", "Prior art", "Narya, OSDI '20", "https://www.usenix.org/conference/osdi20/presentation/levy"],
  ["Traffic-snapshot reproduction and confirmed fixes", "Prior art", "Speedscale Agent Factory", "https://speedscale.com/blog/traffic-context-confirmed-fix-3-minutes/"],
  ["Sequential (SPRT) acceptance of patches", "Prior art", "Stockfish Fishtest", "https://official-stockfish.github.io/docs/fishtest-wiki/Fishtest-Mathematics.html"],
  ["Seed-patch variant finding with LLM-written rules", "Prior art", "BugStone", "https://arxiv.org/abs/2510.14036"],
  ["SRE benchmark with code-level problems", "Benchmark", "SREGym", "https://arxiv.org/abs/2605.07161"],
  ["Agent benchmark on kind (DeathStarBench, Astronomy Shop)", "Benchmark", "AIOpsLab", "https://arxiv.org/abs/2501.06706"],
];

function SourcesTab() {
  const toneFor: Record<Src[1], TableRowTone> = {
    Verified: "success",
    Corrected: "warning",
    "Prior art": "info",
    Benchmark: "neutral",
  };
  return (
    <Stack gap={12}>
      <H2>Key claims and where they were checked</H2>
      <Text tone="secondary">
        Verified means the primary source says it. Corrected means the Notion page or I had it wrong and the row states the true version. Prior art and benchmarks are things to cite, not claims to defend.
      </Text>
      <Table
        headers={["Claim", "Status", "Source"]}
        rowTone={SOURCES.map((s) => toneFor[s[1]])}
        rows={SOURCES.map(([claim, status, label, href]) => [
          c(claim),
          cb(status),
          <Link href={href}>{label}</Link>,
        ])}
      />
    </Stack>
  );
}
