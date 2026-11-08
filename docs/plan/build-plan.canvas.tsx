import {
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
  Pill,
  Row,
  Stack,
  Table,
  Text,
  useCanvasState,
  useHostTheme,
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

function Checklist({ items }: { items: string[] }) {
  const theme = useHostTheme();
  return (
    <Stack gap={6}>
      {items.map((s, i) => (
        <Row key={i} gap={8} align="start">
          <span
            style={{
              minWidth: 14,
              height: 14,
              marginTop: 3,
              borderRadius: 3,
              border: `1px solid ${theme.stroke.primary}`,
              display: "inline-block",
            }}
          />
          <Text style={{ flex: 1, minWidth: 0 }}>{s}</Text>
        </Row>
      ))}
    </Stack>
  );
}

const TABS = [
  ["start", "Before code"],
  ["phases", "Build phases"],
  ["tracks", "Parallel tracks"],
  ["how", "How we work"],
] as const;

export default function BuildPlan() {
  const [tab, setTab] = useCanvasState<string>("plan-tab", "start");
  return (
    <Stack gap={18} style={{ padding: 4, maxWidth: 1080 }}>
      <Stack gap={6}>
        <H1>Sentinel: the full build plan</H1>
        <Text tone="secondary">
          The complete plan from today to the defense and the open-source release. The design itself lives in the companion canvas "Sentinel: the complete project, end to end". The AI agent builds the code; you review, decide, provide accounts, and handle the human parts (supervisor, evaluators, defense). Phases end at measured gates, not dates.
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
      {tab === "start" && <BeforeCode />}
      {tab === "phases" && <Phases />}
      {tab === "tracks" && <Tracks />}
      {tab === "how" && <HowWeWork />}
    </Stack>
  );
}

/* ------------------------------ Before code ------------------------------ */

function BeforeCode() {
  return (
    <Stack gap={22}>
      <Stack gap={8}>
        <H2>Decisions already locked</H2>
        <Table
          headers={["Topic", "Decision"]}
          rows={[
            [cb("Repository"), c("A new repo under a GitHub organization, Apache-2.0 from day one. Sentinel v1 stays as the prototype you learned from.")],
            [cb("Name"), c("Pick a new one first: Microsoft Sentinel, HashiCorp Sentinel and Alibaba Sentinel (microservice flow control) already own the name.")],
            [cb("Language and stack"), c("Go for the system; Helm for install; SQLite or Postgres behind an event-sourced Ledger; a web UI and a CLI.")],
            [cb("Fixer languages"), c("Go, Python and JavaScript/TypeScript first; more through the language adapter later.")],
            [cb("Autonomy presets"), c("observe (default: writes nothing, reports what it would do), assist (reversible actions automatically in allowed namespaces, approval for the rest; used for the demo and evaluation), autopilot (action types that earned trust act on their own).")],
            [cb("AI"), c("Deterministic engines in every slot now. Every decision point is a swappable slot with a typed, probability-based contract, an engine registry, shadow runs, and replay from the Ledger, so Jev-style models or LLM agents can plug in later. The safety shell (policies, contracts, fence, verification, Lab) never moves.")],
            [cb("Who builds"), c("The AI agent writes the code; you review every PR and own the decisions; each teammate presents one module at the defense using a written module guide.")],
            [cb("Test clusters"), c("kind in GitHub Actions on every PR; k3s and minikube locally; a bigger VM or the CUFE server as a self-hosted runner for heavy suites; AKS on student credits for the managed-cluster run; EKS if you can provide it later.")],
            [cb("Evaluators"), c("Recruited by you (Reddit, communities, TAs, alumni), credibility-checked: public GitHub profiles, screening on known patches, a written rubric, blind to which system wrote what.")],
          ]}
        />
      </Stack>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H2>Step 0: do these before any code</H2>
          <Checklist
            items={[
              "Choose the project name.",
              "Create the GitHub organization and the empty repo; give Cursor access; keep GitHub Actions enabled.",
              "Supervisor meeting: show the architecture diagram and the measurable claims; confirm the scope (Lab as the headline, fix and verify in scope); confirm that AI-assisted development is allowed and how to disclose it; ask who can act as game master for the blind study.",
              "Assign each teammate the module they'll present: Watcher, Brain, Lab, Fixer.",
              "Apply for Azure for Students credits.",
              "Send the repo link and say go.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H2>Needed later (not blocking Phase 0)</H2>
          <Checklist
            items={[
              "A test GitHub org or repo plus a GitHub App, so Sentinel can open real PRs (needed by Phase 4).",
              "A bigger machine for heavy suites: the CUFE server or a VM with about 8 CPUs and 32 GB, attached as a self-hosted runner (needed by Phase 3).",
              "Outside evaluators: one game master, 3–5 fault authors, at least 6 patch reviewers (start recruiting now; needed by Phase 6).",
              "Optional: an LLM key or a GPU (the CUFE AI Lab has NVIDIA GPUs) for the AI engines in Phase 5.",
              "EKS access, if available, for an extra managed-cluster run.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H2>Who does what</H2>
        <Table
          headers={["Task", "AI agent", "You", "Teammates", "Supervisor"]}
          rows={[
            [cb("Write code, tests, CI, charts, docs"), c("Does it"), c("Reviews and merges"), cs("Read their module guide"), cs("—")],
            [cb("Design decisions and trade-offs"), c("Proposes with evidence"), c("Decides"), cs("—"), c("Confirms scope")],
            [cb("Accounts, secrets, clusters"), cs("Uses what it's given"), c("Provides"), cs("—"), cs("—")],
            [cb("Evaluators and blind study"), c("Builds the harness and protocol"), c("Recruits"), cs("Help recruit"), c("Game master or appoints one")],
            [cb("Defense"), c("Writes module guides, Q&A bank, demo scripts"), c("Leads"), c("Each presents one module"), c("Examines")],
          ]}
        />
      </Stack>
    </Stack>
  );
}

/* ------------------------------ Phases ------------------------------ */

type Phase = { id: string; title: string; build: string[]; gate: string[]; you: string };

const PHASES: Phase[] = [
  {
    id: "P0",
    title: "Phase 0 · Foundations",
    build: [
      "Repo scaffolding: Go module, module layout (watcher, brain, lab, fixer, ledger, policy, owners, engines), lint, unit tests, release tooling.",
      "Core data model (signal, incident, evidence, hypothesis, action, verdict, fix, track record) and the event-sourced Ledger on SQLite.",
      "Slot interfaces, the engine registry and a shadow-run skeleton, so later engines plug in without rewrites.",
      "Capability detection and the capability report (API discovery, permission checks, node facts, dry-run probes, NetworkPolicy enforcement, owners present).",
      "Helm charts: sentinel-crds, sentinel, and sentinel-fence (the API-server policies that limit Sentinel itself).",
      "Test harness: kind cluster scripts, Online Boutique, a scenario format with recorded ground truth, a fault injector (manifest mutations, Chaos Mesh, flags), and a GitHub Actions workflow that runs it on every PR.",
      "Reusable v1 pieces ported with their bugs fixed (store, GitHub client, repo-CI runner, error parsing).",
    ],
    gate: [
      "CI injects a scenario on kind and records ground truth.",
      "The capability report is correct on kind (and on k3s and minikube when run locally).",
      "A test proves the fence blocks a forbidden write.",
    ],
    you: "Review the layout and data model carefully; they're the hardest things to change later.",
  },
  {
    id: "P1",
    title: "Phase 1 · Observe (Watcher + Brain in shadow mode)",
    build: [
      "Tier-0 collection: trimmed informers, persisted events, log budget, deduplication.",
      "Log pipeline: multiline assembly, redaction, template mining, exception fingerprints, new-error-after-deploy test.",
      "Adaptive detectors: count baselines, seasonal residuals with calibrated tails, false-discovery control, persistence and 'went away' checks, new-vs-old rollout comparison, cold start from sibling replicas.",
      "Dependency map from config and logs; incident grouping; root-cause retrieval and reranking; change attribution.",
      "Shadow report, a read-only incident timeline UI, and a CLI.",
    ],
    gate: [
      "Detection precision and recall, and localization accuracy, on the development fault pool.",
      "False alarms per service per week measured in a 72-hour noisy soak.",
    ],
    you: "Run the UI on a kind cluster and break things; tell me what's confusing.",
  },
  {
    id: "P2",
    title: "Phase 2 · Act (stop the bleeding)",
    build: [
      "Action contracts and CEL policies, with the autonomy presets (observe, assist, autopilot).",
      "Owner adapters: plain Kubernetes, Argo CD, Flux, Helm, Argo Rollouts and Flagger, each with its pause, rollback, revert-PR and resume procedure.",
      "Verification on user-facing signals (concurrent control when possible, anytime-valid tests), undo when no improvement.",
      "Guards: action budgets, capacity-removal cap, circuit breakers, kill switch; escalation and Slack/GitHub notifications; track records.",
    ],
    gate: [
      "Mitigation success and wrong-action rate for change-induced and resource incidents.",
      "End-to-end tests of the Argo CD and Flux procedures, including ApplicationSet and parent-Kustomization cases.",
    ],
    you: "Install Argo CD in your test cluster and watch a rollback happen; approve the policy defaults.",
  },
  {
    id: "P3",
    title: "Phase 3 · Lab",
    build: [
      "Environment synthesis from the live workload: dependency closure, classification, frontier.",
      "Sealing from a pre-created namespace pool: default-deny network, Pod Security 'restricted' with user namespaces, per-Lab DNS without upstream forwarding, Lab certificate authority with per-runtime trust injection, fake secrets with honeytokens.",
      "The probe matrix (positive controls and forbidden destinations), plus tripwire logging.",
      "Dependencies: real copies of stateless services; throwaway Postgres, MySQL, Redis and Kafka seeded from recordings; replay for HTTP and gRPC (MockServer behind the interface at first).",
      "Reproduction by trigger class, and the verdict engine: confidence sequences, the paired e-process, racing, non-inferiority checks, latency and memory tests, deletion controls, A/A calibration. Rollout checks for the Brain.",
    ],
    gate: [
      "Zero isolation breaches, including a red-team exfiltration attempt.",
      "False-PASS rate at or below α on labeled good, bad and deletion-style patches.",
      "Config-fix path working end to end.",
    ],
    you: "Attach the bigger machine as a self-hosted runner; Lab statistics need it.",
  },
  {
    id: "P4",
    title: "Phase 4 · Fixer",
    build: [
      "Source resolution chain with the agreement rule; the localization ranker.",
      "Config and manifest repair: find the writer and the exact file and line (Kustomize origin annotations, Helm value perturbation), comment-preserving edits, render-diff check.",
      "Language adapters for Go, Python and JavaScript/TypeScript (stack parsing, frame mapping incl. source maps, templates, checks, build strategy).",
      "Builds with the repo's Dockerfile or production-image overlays, the build-parity check, signing.",
      "PR delivery with evidence; auto-merge tiers, canary rollout and automatic Git revert.",
    ],
    gate: [
      "Verified-fix and false-fix rates on the development pool.",
      "A pilot round of outside patch review.",
    ],
    you: "Provide the test GitHub org and GitHub App; run the first reviewer pilot.",
  },
  {
    id: "P5",
    title: "Phase 5 · Extend and harden",
    build: [
      "Tier-1 node agent and the tier-2 OBI exporter for captured requests.",
      "Our own faker replacing MockServer, with measured fidelity against it.",
      "More incident classes: node isolation, DNS and dependency failures, certificate expiry, and more.",
      "Mined fix patterns; autonomy graduation; the Merkle audit log; high availability and scale work; UI v1.",
      "Optional AI engines in shadow (an LLM fix generator, a Jev-style incident classifier), compared against the deterministic engines.",
    ],
    gate: [
      "Every extension ships with scenarios and passes; no regressions in the full suite.",
      "First stress-test curves.",
    ],
    you: "Decide which AI engines (if any) to enable for the LLM-on runs.",
  },
  {
    id: "P6",
    title: "Phase 6 · Evaluate",
    build: [
      "Freeze the commit, config and model versions; publish the pre-registration.",
      "The blind fault study (outside authors, game master, hidden ground truth).",
      "Outside human review of every generated patch, mixed with human fixes.",
      "Stress tests (KWOK object scale, log storms, churn, chaos on Sentinel itself).",
      "Ablations (LLM off and on, engines per slot, Lab off and on, fixed vs sequential) and baselines (no-op, scripted, Robusta-style, Argo Rollouts, k8sgpt, HolmesGPT, STRATUS).",
      "Benchmark slices (AIOpsLab, SREGym, RCAEval, Incident-Arena); the managed-cluster run on AKS; a real deployment with real traffic for as long as possible.",
    ],
    gate: ["The pre-registered evaluation report, including failures."],
    you: "Run the people side: game master, fault authors, reviewers.",
  },
  {
    id: "P7",
    title: "Phase 7 · Defend and release",
    build: [
      "Demo rehearsals (at least 10 runs per envelope fault), a recorded fallback, an offline setup (pre-pulled images, local registry and Gitea).",
      "The thesis and report: problem, related work, design per module with algorithm tables, evaluation, limitations; the color-coded diagram.",
      "Module guides and the Q&A bank for each presenter.",
      "Open-source v1.0: docs site, Helm repository, governance files, contribution guide.",
    ],
    gate: ["Supervisor sign-off; public release."],
    you: "Run the rehearsals with your teammates.",
  },
];

function Phases() {
  return (
    <Stack gap={14}>
      <H2>Build phases</H2>
      <Text tone="secondary">
        The order is the agreed strategy: a complete loop for the starting incident types (change-induced and resource incidents) first, then extensions. Each phase ends when its gate is met and measured.
      </Text>
      {PHASES.map((p, i) => (
        <CollapsibleSection key={p.id} title={p.title} defaultOpen={i === 0}>
          <Grid columns="3fr 2fr" gap={16}>
            <Stack gap={6}>
              <Text size="small" weight="semibold">Build</Text>
              <Bullets items={p.build} />
            </Stack>
            <Stack gap={10}>
              <Card>
                <CardHeader>Gate: done when</CardHeader>
                <CardBody>
                  <Bullets items={p.gate} />
                </CardBody>
              </Card>
              <Text size="small" tone="secondary">Your part: {p.you}</Text>
            </Stack>
          </Grid>
        </CollapsibleSection>
      ))}
    </Stack>
  );
}

/* ------------------------------ Tracks ------------------------------ */

function Tracks() {
  return (
    <Stack gap={22}>
      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H2>Evaluation track (runs from Phase 0)</H2>
          <Bullets
            items={[
              "Phase 0: scenario format and ground-truth recording; development fault pool (Marketly's bugs are development-only).",
              "Phases 1–5: every new capability adds scenarios; the full suite runs on each PR, heavy suites nightly on the self-hosted runner.",
              "During Phase 4: draft the pre-registration (hypotheses, metrics, budgets, analysis) and run the reviewer pilot.",
              "Phase 6: freeze, blind study, human review, stress, ablations, benchmarks, managed-cluster and real-deployment runs.",
              "Targets: 80–100 blind scenarios plus 30+ no-fault controls; zero wrong actions in 60+ opportunities; at least 6 reviewers with 3 per patch.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H2>Defense track (runs from Phase 1)</H2>
          <Bullets
            items={[
              "A module guide per module, updated at every gate: what it does, the algorithms and why, the alternatives considered, the measured results, likely questions.",
              "Each teammate reads their guide and runs their module's demo before Phase 6.",
              "A Q&A bank built from the likely examiner questions ('would a restart fix it?', 'what's yours vs the LLM's?', 'is the patch correct?', 'does it scale?').",
              "Supervisor updates at each phase gate, with the diagram and the numbers.",
            ]}
          />
        </Stack>
      </Grid>

      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H2>Infrastructure track</H2>
          <Table
            headers={["Where", "Used for"]}
            rows={[
              [cb("GitHub Actions (kind)"), c("Unit tests and end-to-end scenarios on every PR; KWOK stress smoke tests")],
              [cb("Local k3s and minikube"), c("Capability-report checks; minikube's default networking tests the 'Lab refuses to run' path")],
              [cb("Self-hosted runner (CUFE server or 8-CPU VM)"), c("Lab statistics, long soaks, full suites, the blind study")],
              [cb("AKS (student credits)"), c("Managed-cluster run; NetworkPolicy enforcement checks")],
              [cb("EKS (if provided)"), c("Second managed cluster, strict-mode network policy")],
              [cb("GHCR and a local registry, Gitea"), c("Images and offline demo")],
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H2>After graduation</H2>
          <Bullets
            items={[
              "Public v1.0: docs, Helm repository, Artifact Hub listing, a 5-minute quickstart that works on kind.",
              "Community basics: issues and good-first-issues, DCO sign-offs, governance and maintainer files, a public roadmap, OpenSSF badge.",
              "AI engines as opt-in plug-ins, promoted only after shadow runs prove them.",
              "Named adopters and an external maintainer, then a CNCF Sandbox application (needs 3+ maintainers from 2+ organizations).",
              "No core feature depends on a hosted service you run (the Botkube lesson).",
            ]}
          />
        </Stack>
      </Grid>
    </Stack>
  );
}

/* ------------------------------ How we work ------------------------------ */

function HowWeWork() {
  const tones: (TableRowTone | undefined)[] = ["warning", "warning", "warning", "warning", "warning", "warning"];
  return (
    <Stack gap={22}>
      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H2>Working rules</H2>
          <Bullets
            items={[
              "Small PRs, one coherent slice each; CI (unit plus kind end-to-end) must pass before review.",
              "You review and merge; nothing lands without your approval.",
              "Each session ends with a short report: what changed, what was measured, what's next, anything blocked.",
              "Each phase ends with a demo script, its numbers, updated docs and an updated module guide.",
              "Every changed decision goes into the decision log with its reason and a 'revisit if' condition.",
              "No calendar estimates: progress is measured by gates.",
            ]}
          />
        </Stack>
        <Stack gap={8}>
          <H2>Quality bar for every module</H2>
          <Bullets
            items={[
              "One hard, documented algorithmic core with its alternatives and measured results.",
              "Typed contracts at the boundaries, so engines and edges stay swappable.",
              "Fail closed on missing prerequisites, with the reason shown.",
              "Tests: unit tests for logic, scenario tests for behavior, A/A tests for statistics.",
              "Everything it decides is written to the Ledger with its evidence.",
            ]}
          />
        </Stack>
      </Grid>

      <Stack gap={8}>
        <H2>Top risks and how the plan handles them</H2>
        <Table
          headers={["Risk", "Handling"]}
          rowTone={tones}
          rows={[
            [cb("Plumbing eats the time meant for algorithms"), c("The evaluation harness comes first; phase gates force a working loop before extensions.")],
            [cb("The Lab can't run on clusters without NetworkPolicy enforcement"), c("Fail closed with the reason; test the matrix; report it honestly.")],
            [cb("Rare or request-specific bugs are hard to reproduce"), c("Trigger classes, amplification, opt-in capture; report coverage per class; NOT SURE is a valid result.")],
            [cb("Source resolution fails on real images"), c("Fallback chain plus a one-time annotation; diagnosis-only issue otherwise.")],
            [cb("Evaluator recruitment is slow"), c("Start recruiting at Step 0; credibility checks; pilot in Phase 4.")],
            [cb("Teammates can't explain the code at the defense"), c("Module guides at every gate; each teammate runs their demo before Phase 6; Q&A bank.")],
          ]}
        />
      </Stack>

      <Callout tone="info" title="The next action">
        Finish Step 0 (name, org and repo, supervisor meeting, teammate presentation roles, Azure credits). Then send the repo link and say go, and Phase 0 starts.
      </Callout>
    </Stack>
  );
}
