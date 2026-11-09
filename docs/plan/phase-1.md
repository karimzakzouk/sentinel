# Phase 1 plan: Observe

The Watcher and the Brain run in shadow mode: they detect, group, explain and
record what Sentinel *would* do, and change nothing. Phase 1 is built as six
vertical slices. Each one works end to end, is scored against the scenario
pool, and lands before the next starts.

The design it implements is in `end-to-end.canvas.tsx` (Watcher and Brain
tabs). Deviations get an entry in `docs/decisions.md`.

## Slices

### 1. Kubernetes facts → signals → incidents

- **Collection**: informers for Pods, Deployments, ReplicaSets, StatefulSets
  and events in the watched namespaces, with `managedFields` and unused spec
  stripped before caching.
- **Facts**: container terminations deduplicated by (pod UID, container,
  restart count); waiting reasons (image pull, crash loop, config errors);
  OOM kills; running-but-not-ready pods; scheduling and probe-failure events.
- **Count baselines**: a Gamma–Poisson posterior predictive per workload and
  fact type, with a prior pooled across workloads (empirical Bayes). A fact
  becomes a signal with its tail probability; Benjamini–Hochberg controls
  false discoveries across series. The only knob is the false-alarm budget.
- **Change log**: rollouts (new ReplicaSet revisions, with what changed in the
  template), scale changes and ConfigMap changes (hashes of values, never the
  values), recorded in the Ledger.
- **Incidents**: signals join an incident when they share a workload or are
  close in time on connected workloads; the gap is learned, starting from a
  prior.
- **Brain v0**: `brain.classify` (fault class distribution from facts and
  change evidence), `brain.attribute` (likelihood-ratio ranking of recent
  changes: type prior, onset gap, share of failing pods on the new revision),
  and a shadow decision: the action it would take, "do nothing", or
  "escalate".
- **Proof**: the smoke scenarios produce one incident each with the right
  fault class and change; the controls and the pod kill produce no action.

### 2. Logs

- Previous and current logs fetched within a budget when a container
  terminates, restarts or flips readiness.
- Multiline assembly (Go, Java, Python, Node stack traces), two-pass
  redaction (key rules, provider regexes, entropy, JWT/PEM checks) tested with
  canary secrets, Drain-style templates, Sentry-style exception fingerprints.
- Evidence records hold a redacted summary and a hash, never raw lines.
- New error after deploy: a Poisson rate-ratio test of new pods against old.

Design as built:

- **Fetch triggers.** A container restart or OOM kill fetches the previous
  container's logs (the kubelet keeps them only until the next restart), once
  per pod, container and restart count. A pod not ready past its grace period
  fetches current logs once per readiness episode. Each fetch asks for at most
  300 lines and 64 KiB, with timestamps.
- **Budget.** A token bucket allows 30 fetches a minute across the cluster,
  two at a time. Fetches over budget are dropped and counted, never queued
  without bound.
- **Rollout samples.** When a Deployment or StatefulSet gets a new revision,
  the old revision's pods (up to three) are sampled at once for their last five
  minutes, before the rollout replaces them. The new revision's pods are
  sampled one and three minutes after they start. Exposure is pod-seconds of
  log covered.
- **New error after deploy.** For each error-like template or exception
  fingerprint, a conditional binomial test asks whether its rate on new pods
  exceeds its rate on old pods (rate ratio above 1). Bonferroni across the
  templates tested in that rollout at a family level of 10⁻³. A significant
  template is a signal on the workload; the rollout's change is the candidate
  cause, and the fault class is `deploy.new-errors`.
- **Evidence.** A fetch becomes a summary: pod, container, time range, line
  and record counts, the top templates with one redacted sample each, the
  exceptions with fingerprint, types and in-app frames, redaction counts by
  rule, and the SHA-256 of the raw bytes. The Brain keeps summaries per
  workload for 30 minutes and records them in the Ledger only when they join
  an incident. Raw lines never leave the fetch.
- **Permissions.** The controller needs `get` on `pods/log` in watched
  namespaces; the capability report checks it, and without it log features
  turn off and slice 1 keeps working.
- **Proof.** The smoke crash-on-start incident carries the container's error
  as a template. A new smoke scenario crashes after printing canary secrets
  of every kind; the end-to-end test exports the Ledger and finds none of
  them. A new Online Boutique scenario points the frontend at a wrong port
  for the product catalog: no pod fails, slice 1 sees nothing, and the
  rate-ratio test finds the new error and blames the change.

### 3. Metrics and localization

- metrics.k8s.io when present (metrics-server is added to the test clusters).
- Dependency map from configuration (service addresses in environment
  variables and ConfigMaps) and from logs (hosts in connection errors).
- `brain.localize`: BARO-style robust deviation scoring to retrieve suspects,
  reranked on the dependency map, returned as a ranking with a confidence set.
- **Proof**: Online Boutique's Redis scenario is localized to Redis, not to
  where the errors appear.

What the Redis scenario actually shows, measured before designing: the cart
service stays ready (its health check does not touch Redis) and logs Redis
exceptions; the frontend logs about one error a second; Kubernetes reports
the `redis-cart` Service with no endpoints; and the scale to zero is a
recorded change. Design as built, in order:

- **Service endpoints.** A Service with a selector whose ready endpoints drop
  to zero is a state fact (`service.no-endpoints`) on the workloads its
  selector matches, counted per tick like other states. A Service that
  always has none learns that as its baseline.
- **Dependency map from configuration.** Environment values in pod templates
  that name a Service in the namespace (`redis-cart:6379`,
  `http://cartservice:7070`, the Service's DNS names) become edges from the
  workload to the Service's workloads. Edges carry their source and a
  reliability; logs (hosts in connection errors) add edges in slice 4, fused
  by noisy-OR.
- **Incidents name their impact.** An incident on a workload lists the
  workloads that depend on it, transitively.
- **`dependency.unavailable` and restoring scale.** A new fault class for a
  depended-on workload with no endpoints, and a new action, `scale.restore`,
  for a culprit that is a scale change: rolling back a template does not bring
  replicas back.
- **`brain.localize`.** Ranks candidate root causes for an incident over the
  workload and what it depends on: each candidate's own anomaly evidence,
  weighted toward upstream workloads. Metrics join the scoring once
  metrics-server is in the test clusters.

### 4. Rate detectors

Seasonal decomposition with median/MAD residuals, SPOT/DSPOT tail
probabilities, Benjamini–Hochberg across series, CUSUM persistence and a
Mann–Kendall "went away" check; cold start from sibling replicas.

The first rate built is the one the scenarios lacked: a workload's error
rate while its pods stay up. Design as built (decision 36):

- **Error sampling.** Every minute, the last minute of one ready pod's logs
  per workload, rotating through the pods. One pod at a time keeps the count
  a per-pod rate, which scaling does not change, and costs one fetch per
  workload a minute. Samples are low priority: the collector queues them,
  one per workload, and serves them while its budget keeps five fetches for
  crashes, readiness flips and rollouts.
- **`log.errors` facts and exposure.** Each error record in a sample is a
  fact; each sample also reports the seconds of logs it read. Error series
  count that exposure instead of the clock, so a sample the budget delays
  changes how much is known, not the rate, and a window without samples is
  no evidence either way.
- **Guards for bursty, workload-specific levels.** The Gamma–Poisson test
  stays, with a weak prior instead of the pooled one, a 30-minute warm-up,
  an effect floor (twice the expected count) with hysteresis (the episode
  ends under one and a half times), and an hour's limit on an episode, after
  which the level is learned as the new normal.
- **`runtime.errors`.** Errors at an unusual rate with no change to blame is
  a class of its own, escalated rather than rolled back.
- **Proof**: in the Redis scenario, the cart service's and the frontend's
  errors join Redis's incident instead of opening their own.

Seasonality (SPOT on decomposed residuals) waits for the soak: a daily cycle
needs days of data to matter, and the soak will say whether the ratio floor
is enough.

### 5. Shadow report, CLI and read-only UI

- `sentinel incidents` and `sentinel report shadow`.
- A read-only incident timeline in a React and TypeScript UI built into the
  binary. It listens on localhost inside the pod, so the only way in is
  `kubectl port-forward`, which Kubernetes RBAC already guards; no login code
  and nothing exposed on the network. Proper sign-in comes with UI v1.

### 6. Scoring

`harness score` runs the scenario pool, reads Sentinel's incidents from the
Ledger export, and computes against the hidden ground truth: per-event
detection precision and recall (no point adjustment), AC@1, AC@3 and Avg@5 for
localization, change-attribution hit rate, and false alarms per service per
week from the controls.

## Gate

- Detection precision and recall, and localization accuracy, on the
  development fault pool (scored by `harness score`).
- False alarms per service per week in a 72-hour noisy soak. This needs a
  machine that can run for three days; until then, shorter soaks give an
  estimate with its interval.
