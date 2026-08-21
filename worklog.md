---
Task ID: scan-G
Agent: general-purpose
Task: Deep-read agent loop + CLIs + chart + bench

Work Log:
- internal/agent/: agent.go (621 L), agent_test.go, pipeline_adapter.go, pipeline_adapter_test.go, parallel.go, stuck_detector.go, critic.go, condense.go, investigator.go, mcts.go, v3_test.go, e2e_test.go (12 files)
- cmd/sentinel/: wiring.go (683 L), callbacks.go (376 L), recover.go (32 L) (3 files)
- cmd/sentinelctl/: main.go (266 L), client.go, client_test.go, incidents.go, incidents_test.go, agent.go (501 L), agent_test.go, app.go (494 L), config.go, metrics.go, helpers.go, helpers_test.go (11 files)
- cmd/sentinel-bench/: main.go, runner.go, runner_test.go, scenario.go, client.go, diff.go, diff_test.go (7 files)
- chart/: Chart.yaml, values.yaml, README.md, templates/{deployment,rbac,crd,ingress,networkpolicy,pdb,secrets,servicemonitor,NOTES.txt}.yaml (10 files)
- sentinel-bench/: README.md, scenarios/MANIFEST.yaml, S001/{scenario.yaml,evidence.md,ground-truth.diff,cluster/deployment.yaml,cluster/service.yaml} (S001's cluster/configmap.yaml does NOT exist — scenario.yaml only lists deployment.yaml + service.yaml under setup.apply), S002/{scenario.yaml,evidence.md,ground-truth.diff,cluster/deployment.yaml,cluster/service.yaml}, S003/{scenario.yaml,evidence.md,ground-truth.diff,cluster/deployment.yaml,cluster/service.yaml,cluster/configmap.yaml} (15 actual scenario files + README + MANIFEST = 17 files read; task listed 16)
- scripts/sync-version.py (1 file)
- Total: 60 files read end-to-end.

Key findings:

=== 1. AGENT LOOP (Phase 11) ===
The new `internal/agent` package is an N-turn ReAct loop that replaces the legacy 2-round LLM pipeline in `internal/agents/investigator.go`.

**Types (agent.go):**
- `Agent struct { router *llm.Router; cfg AgentConfig; tools []Tool; toolFuncs map[string]ToolFunc }`
- `AgentConfig { MaxTurns, CompactThreshold int; TurnTimeout time.Duration; NumPlans int; CriticThreshold float64 }`
- `InvestigationResult { RootCause string; Confidence float64; SourceFiles []string; Hypotheses []string; TurnsUsed int; TotalTokens int; Trace []Turn }`
- `Tool { Name, Description, Parameters string }`, `ToolResult { Tool, Output string; Success bool }`, `ToolCall { Tool string; Args map[string]any }`, `Turn { TurnNumber int; LLMResponse string; ToolCall *ToolCall; ToolResult *ToolResult; Duration time.Duration }`
- `ToolFunc = func(ctx context.Context, args map[string]any) ToolResult`

**Constants:** `DefaultMaxTurns=20`, `DefaultTurnTimeout=30s`, `DefaultCompactThreshold=10`, `MaxFilePreviewLines=100`, `maxRetriesPerTurn=2`.

**System prompts:** `agentSystemRole` enforces JSON-only output (Tool or Done), `agentInstructions` shows the JSON schema, `correctionPrompt` is sent on malformed output.

**Loop phases (Agent.Run):**
1. **Planning:** If `NumPlans > 1`, calls `generatePlans` (mcts.go) → `pickBestPlan`. If `NumPlans == 1` (default = v2 behavior), a single `router.FrontierText` call produces a plan.
2. **Execution:** Loop `turnNum=1..MaxTurns`:
   - Inner retry loop (up to `maxRetriesPerTurn=2`) calls `router.FrontierText` and `parseLLMResponse`.
   - On malformed JSON, appends assistant response + `correctionPrompt` and retries (does NOT count as a turn — verified by `TestAgent_E2E_RetryOnGarbageOutput`: 4 LLM calls / 2 TurnsUsed).
   - On `parsed.Done`: optional critic review (only if `CriticThreshold > 0 && !criticUsed && Confidence < threshold && turnNum < MaxTurns`); returns `InvestigationResult`.
   - On `parsed.Tool`: calls `executeTool`, records in `StuckDetector`, appends tool result + optional stuck message to conversation.
   - Every `turnNum >= CompactThreshold && turnNum%5 == 0`: calls `condenseContext`.
3. **Turn cap:** Returns `InvestigationResult{Confidence:0, RootCause:"Agent did not complete within %d turns. Last response: ..."}` (with explicit guard for `len(turns)==0`).

**parseLLMResponse** has 3 strategies: direct JSON unmarshal → extract from markdown code block (` ```json ... ``` `) → extract first balanced `{...}` object. All require valid `llmResponse{ Done bool; Tool string; Args map[string]any; RootCause string; Confidence float64; FilesToChange []string }`.

**Tools registered:**
- `RegisterCodeIntelTools(idx *codeintel.Index)` → `find_definition`, `find_callers`, `find_callees`, `list_symbols`.
- `RegisterRepoTools(repoPath string)` → `read_file` (max 100 lines via `repo.ReadFile`), `search_code` (regex via `repo.Search`, default 10 results), `list_files` (via `repo.WalkTree`, max 100).

**Differences from legacy investigator:**
- Legacy `agents.Investigator.Run` does 2 LLM rounds (hypothese → root cause) with hardcoded prompts; agent loop runs up to 20 turns with tools.
- Legacy investigator owns K8s+GitHub+vectorStore plumbing internally; agent package is intentionally thin (no K8s/GitHub deps) and is bridged by `PipelineAdapter`.
- Legacy investigator auto-applies past fixes via `applyKnownFix`; the adapter explicitly DOES NOT — surfaces them as `state.RetrievedIncidents` context for the agent loop instead.

**MCTS (mcts.go):** Simplified "best-of-N" planning, not full tree search. `generatePlans` makes N independent LLM calls with prompt "Plan #i — try a different approach". `pickBestPlan` uses `scorePlan`: +2 per file-extension match (.go/.py/.rs/.ts/.java/.rb/.cpp/.yaml/.yml), +1 per action keyword (read/search/find/trace/check/inspect/examine), +3 per pod-log keyword match (via `extractKeywords`), −1 per vague word. `extractKeywords` parses `file:line` patterns and CamelCase identifiers from logs.

**Critic (critic.go):** Reflexion pattern. `criticReview(ctx, rootCause, files, podLogs, conversation) (approved bool, critique string)`. Sends a separate LLM call with prompt "You are a senior SRE reviewing another agent's root cause analysis". On parse failure or LLM error, returns `(true, "")` — never blocks. Comment cites Reflexion paper (HumanEval 80%→91%) + ALMAS multi-agent.

**Condenser (condense.go):** `condenseContext(ctx, conv, systemPrompt) []map[string]string`. If `len(conv) <= 6` returns unchanged. Extracts tool-result messages, asks LLM "Compress the following tool results into a brief summary... under 200 words". Falls back to `compactConversation` (naive concatenation) on LLM error. Returns `[system+agentSystemRole, summary, last-2-messages]`. Cited as OpenHands "Context Condenser" pattern.

**Parallel tool execution (parallel.go):** `ExecuteToolsParallel(ctx, calls []ToolCall) []ToolResult` runs each tool in its own goroutine with `recover()` capturing panics as error results. `formatParallelResults` concatenates as `Tool X result:\nY`. Exported for future native function-calling support.

**Stuck detector (stuck_detector.go):** `StuckDetector{ history []toolCallEntry; maxHist int }` (default 10). `Record(tool, args, result)` appends `toolCallEntry{tool, argsKey, success, truncated output 200}`. `CheckStuck() string` returns "" if OK, else a recovery prompt. Four detection rules:
- `detectRepeatedSameCall(3)`: 3+ identical (tool, argsKey) calls → "called the same tool with the same arguments 3+ times".
- `detectRepeatedTool(4)`: 4+ calls to same tool with different args → "called the same tool 4+ times with different arguments".
- `detectRepeatedErrors(3)`: 3+ failed/empty results (uses `looksLikeEmptyResult` checking for "no matches"/"no definition"/"not found"/etc.).
- `detectAlternating(4)`: alternates between two distinct tools 4+ times each.

**PipelineAdapter (pipeline_adapter.go):** Bridges `agent.Investigator` into `agents.InvestigatorRunner` interface. Fields: `router, cfg, k8s *k8s.Client, gitHubToken, cloner Cloner, allowedRepos, gitHost, defaultBranch, vectorStore agents.VectorStore`. Compile-time assertion: `var _ agents.InvestigatorRunner = (*PipelineAdapter)(nil)`.

`Run(ctx, state)` flow:
1. Fetch pod logs via `k8s.FetchCrashingPodLogs` (fallback `extractPodLogs(state)`).
2. Vector store search for similar incidents (top-5). High-similarity (>ConfidenceHighMatch + success + not hash fallback) only logs — does NOT auto-apply (deliberate departure from legacy). Medium-similarity (>ConfidenceMediumMatch) appends to `state.RetrievedIncidents`.
3. Discover repo via `agents.DiscoverRepo(state, gitHost)`. Empty/not-allowed → `runWithoutRepo` (calls `agents.GenerateHypothesesUniversal(podLogs, state)` and returns).
4. Resolve branch via `resolveBranch` priority: `sentinel.io/git-commit` → `sentinel.io/branch` → `defaultBranch` (default "main").
5. Clone via `a.cloner.Clone(ctx, repo, branch)`; on error → `runWithoutRepo`. `defer a.cloner.Cleanup(repoPath)`.
6. `inv := NewInvestigator(router, repoPath, cfg); return inv.Run(ctx, state)`.

`String()` method renders `agent.PipelineAdapter{k8s=yes|no,gitHost=...,defaultBranch=...,allowlist=yes|no}`.

**Investigator (investigator.go):** Thin wrapper. `Run(ctx, state)`:
1. `extractPodLogs(state)` — empty → `agents.GenerateHypothesesUniversal` and return.
2. `codeintel.BuildIndex(repoPath, 500)` — on error → fallback to error parser.
3. Constructs `Agent`, registers `CodeIntelTools` + `RepoTools`, builds system prompt via `buildSystemPrompt` (service/namespace/crash logs truncated to 4000 + symbol list + parsed error from `agents.ParseError`).
4. `result, err := a.Run(ctx, systemPrompt)`. On error → fallback to error parser.
5. Populates `state.Hypotheses = []schemas.Hypothesis{{HID:"H1", Statement: result.RootCause, Confidence: result.Confidence, SourceFiles: result.SourceFiles}}`. If `Confidence >= 0.5` sets `state.RootCause` and `state.Status = StatusRootCaused`.
6. `state.SourceFiles`, `state.FetchedCode` (reads each source file via `repo.ReadFile`), `state.AgentTrace` (one entry per turn with `agent:"agent_loop", turn, tool, result_len`).

**Tests:** `e2e_test.go` has `TestAgent_E2E_KeyErrorDiagnosis` (scripted LLM with 3 responses: plan → read_file → done; asserts 3 LLM calls), `TestAgent_E2E_RetryOnGarbageOutput` (4 LLM calls / 2 turns), `TestAgent_E2E_HitTurnCap` (MaxTurns=5 → Confidence=0, root_cause contains "did not complete"). `v3_test.go` covers `pickBestPlan`/`scorePlan`/`extractKeywords`/`formatParallelResults`. `pipeline_adapter_test.go` covers happy path, multi-turn, turn cap, vector store surfacing, branch resolution priority, context cancellation.

=== 2. WIRING STRUCT (cmd/sentinel/wiring.go) ===
Collapses v5's 17 adapter structs into a single `wiring` struct holding concrete pointers to every initialized component. The comment says: "All adapter logic is expressed as methods on *wiring, so the wiring is inspectable in a single place and there is no adapter-of-adapter indirection."

**Full field set:**
```go
type wiring struct {
    mu  sync.RWMutex
    cfg *config.Config

    // Storage + vector memory.
    store       *feedback.Store
    vectorStore *feedback.VectorStore
    agentsVS    *agentsVectorStore

    // Infrastructure clients.
    k8sClient   *k8s.Client
    embedder    emb.Embedder
    router      *llm.Router
    rtCfg       *runtimecfg.Manager
    broadcaster events.Broadcaster

    // Agents + safety.
    pipeline    *agents.Pipeline
    policy      *safety.Policy
    alertSender *agents.AlertSender
    safeActions *safeactions.Controller

    // Executors.
    ghExec      *executors.GitHubExecutor
    k8sExec     *executors.K8sExecutor
    sandbox     *executors.SandboxRunner
    kaniko      *executors.KanikoBuilder
    remediation *executors.RemediationController

    // Detection + correlation.
    tracker        *detection.ActiveTracker
    clusterer      *detection.Clusterer
    detector       *detection.Detector
    correlator     *correlation.Correlator
    outcomeWatcher *feedback.OutcomeWatcher
    crdWatcher     *watcher.Watcher

    // Chatops + PR review + auto-merge.
    chatopsGH        *chatops.GitHubClientImpl
    chatopsStatus    chatops.StatusCommentManager
    chatopsReactions chatops.ReactionsClient
    prReviewer       *prreview.Reviewer
    autoMergeGH      *automerge.GitHubClientImpl
    autoMergePolicy  *automerge.Policy
    autoMergeMerger  *automerge.Merger
    amReceiver       *alertmanager.Receiver
    slackBot         *slackbot.Bot

    // API server.
    apiServer *api.Server

    // Tier 1 (optional, nil when disabled).
    promQuerier    *observability.PrometheusQuerier
    postMerge      *postmerge.Watcher
    sloWatcher     *slo.Watcher
    causalAnalyzer *causal.Analyzer
    admissionSrv   *http.Server

    // Tier 2 (optional, nil when disabled).
    attestor    *attest.Signer
    chaosRunner *chaos.Runner

    // Concurrency primitives for the investigation handler.
    ready  *atomic.Bool
    invSem chan struct{}
    invWG  sync.WaitGroup
}
```

**Methods:** `Config()` (RLock), `mutateConfig(fn)` (Lock).

**Interfaces satisfied (compile-time assertions at file end):**
- `chatops.Store` — `FindIncidentByPR`, `LogCommandEvent`, `UpdateOverrides`.
- `chatops.AutoMergeClient` — `Track`, `Untrack`, `EvaluateNow`, `Status`, `RecordRegression`, `ClearCooldown`.
- `chatops.PipelineRunner` — `RetryFixProposal`, `Reinvestigate`.
- `chatops.SandboxRunner` — `RunForIncident`.
- `automerge.Store` — `UpdateAutoMergeStatus`.
- `automerge.OutcomeWatcher` — `WatchPostMerge`.
- `alertmanager.SignalHandler` — `HandleAlert` (looks up `sentinel.io/repo` annotation via K8s when not in payload, then `clusterer.Send` with `SignalLogError`).
- `admission.IncidentMemory` — `SimilarIncidents` (vector search ≥ threshold).
- `causal.DepGraphProvider` — `GraphFor` (returns `&causal.Graph{Nodes: nodes}` — currently a runtime-inferred graph).
- `chaos.K8sClient` — `ListPods`, `DeletePod`, `GetConfigMap`, `PatchConfigMap`, `RestartDeployment` (adds `sentinel.io/chaos-restart-at` annotation), `ListDeployments`, `ListConfigMaps`.
- `api.ProvidersStatusGetter` — `ProviderStatuses` (from `router.Pool().Status()`).
- `api.AutoMergeStatusGetter` — `AutoMergeStatus` (combines `autoMergePolicy.Status()` + `autoMergeMerger.TrackedPRs()`).
- `agents.VectorStore` — `agentsVectorStore` (separate type because Go forbids two methods with the same name and different return types on one receiver).
- `prreview.VectorStore` — `prreviewVectorStore` (same reason).

=== 3. CALLBACKS (cmd/sentinel/callbacks.go) ===
Six methods on `*wiring`:

- **`buildChatopsDeps() *chatops.Deps`** — assembles Deps bag from wired components. `Store, AutoMerge, Pipeline, Sandbox` all point to `w` (the wiring itself); `GitHub=chatopsGH`, `Router`, `Reactions`, `StatusComment`, `Rollback=k8sExec`, `PauseController=slackBot`.

- **`buildAdmissionServer() *http.Server`** — constructs the validating admission webhook. `admission.NewHandler(w, nil)` (CVEScanner=nil; issue #181 still open). Mux: `/validate` (decodes `admissionv1.AdmissionReview`, calls `admHandler.Handle`, writes back) and `/healthz`. Returns `&http.Server{Addr: cfg.AdmissionWebhookAddr, Handler: mux}`.

- **`configureGitHubApp()`** — wires GitHub App (if all 3 of `GitHubAppID`/`GitHubAppPrivateKey`/`GitHubAppInstallID` set, runtime cfg first then static cfg) into `prReviewer.WithGitHubApp(app)`, `ghExec.WithTokenProvider(app)`, `chatopsGH.WithTokenProvider(app)`, `autoMergeGH.WithTokenProvider(app)`. `*github.App` directly satisfies `chatops.TokenProvider` + `automerge.TokenProvider`. Logs hint if not configured.

- **`applyRuntimeConfig(rc *runtimecfg.RuntimeConfig)`** — the `runtimecfg.Manager` apply callback (synchronous from `rtCfg.Save`):
  1. **LLM router reconfigure:** Falls back to current baseURL when `rc.LLMBaseURL` is empty (warns if provider changed without baseURL). Calls `w.router.Update(baseURL, apiKey, fastModel, frontierModel)`.
  2. **Config mutation** under write lock: `SandboxEnabled/Required/MaxRetries`, `InvestigatorTimeoutSec`, `EvidenceGathererTimeoutSec`, `AnalystTimeoutSec`, `FixProposerTimeoutSec`, `ConfidenceLoopBackThreshold`, `LLMMaxRetries`, `UseAgentLoop`.
  3. **Phase 11 hot-toggle:** When `rc.UseAgentLoop` flips, constructs `agent.NewPipelineAdapter(router, k8sClient, GitHubToken, agentsVS, AgentConfig{MaxTurns:DefaultMaxTurns, TurnTimeout: cfg.InvestigatorTimeoutSec, CompactThreshold:DefaultCompactThreshold})` and calls `pipeline.SetAgentLoopRunner(adapter)` (or `nil` to revert). No restart needed.
  4. `logging.Init(rc.LogLevel)` if set.
  5. **GitHub App reconfiguration** when all 3 fields provided — same wiring as `configureGitHubApp()`.

- **`handleSLOIncident(inc slo.Incident)`** — converts SLO burn-rate incident into `schemas.AnomalyCluster` with `Detector:"slo_burn_rate"`, `Score: inc.LongBurn/inc.Threshold`, dimensions `{long_burn, short_burn, threshold}`, annotations `sentinel.io/detector:slo`, `sentinel.io/rule:<ruleName>`, `sentinel.io/slo:<target>`. Severity: `page`→`SeverityCritical`, else `SeverityWarning`. Spawns `go w.handleInvestigation(ctx, cluster, "slo", inc.Namespace)`.

- **`runChaosInvestigation(ctx, exp chaos.Experiment, namespace) chaos.Outcome`** — picks first deployment in namespace via `ListDeployments`, builds synthetic AnomalyCluster with `Detector:"chaos"`, `Score:1.0`, annotations `sentinel.io/chaos:true` + `sentinel.io/what-broke:<exp.Name()>`. If `attestor != nil`, signs an `attest.Attestation{Action:"chaos.investigation_triggered", Evidence:[{Kind:"chaos", Payload:{service,namespace,experiment}}]}`. Runs `w.handleInvestigation` in goroutine with 10-minute timeout (timeout still sets `outcome.Notes="investigation pipeline triggered but did not complete within 10m timeout"`).

=== 4. RECOVER (cmd/sentinel/recover.go) ===
Tiny helper (32 lines). `recoverPanic(name string)` — intended for `defer recoverPanic("sandbox reaper")` at the top of every goroutine. Calls `recover()`; on non-nil, logs via `logging.Error("goroutine panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))`. Without this, a panicking goroutine kills the whole Sentinel process.

=== 5. sentinelctl CLI ===
Thin HTTP client for Sentinel's API. Built as a single binary at `cmd/sentinelctl/main.go`.

**Client (client.go):** `Client{baseURL, token, jsonOut, http *http.Client}`. Methods: `Do(ctx, method, path, body, authRequired)` (sets `Authorization: Bearer <token>` when authRequired+token set; surfaces 401 with "pass --token" hint when token is empty, or "token is incorrect" when set; 403 → "token lacks required scope"; ≥400 → "API returned N: <body>"), `Get`, `PostJSON` (handles `json.RawMessage`/`[]byte`/default marshal), `DoRaw` (custom method + pre-encoded body, returns `*http.Response` for endpoints needing exact control e.g. /settings).

**main.go dispatch:** Reads `SENTINEL_SERVER` (default http://localhost:8000), `SENTINEL_API_TOKEN`. `findSubcommand` skips global flags (`--server`, `--token`, `--timeout` take values; `--json` doesn't) and returns first non-flag token. `extractGlobalFlags` supports both `--flag value` and `--flag=value`. Subcommands: `incidents|i`, `trigger|t`, `pause`, `resume`, `stats|s`, `config|cfg`, `metrics|m`, `app`, `agent`, `version|v`, `help|h|--help|-h`. Meta flags `--help/-h`/`--version/-V` recognized anywhere.

**incidents.go:**
- `IncidentFilters{Status, Service string; Last time.Duration; Limit int}` (default Limit=50; auto-bumped to 500 if `--last` set).
- `ParseIncidentFilters` accepts `--status`, `--service`, `--last` (with `parseLastDuration` supporting `Nd/Nh/Nm` + Go duration), `--limit` in both `--flag value` and `--flag=value` forms.
- `ListIncidents` — GETs `/incidents?limit=N`, renders a tabwriter table (`ID SERVICE NAMESPACE STATUS CONFIDENCE CREATED`) or raw JSON. Client-side filtering via `filterIncidents`. ID truncated to 8 chars via `shortID`.
- `GetIncident` — GETs `/incidents/{id}`, renders key/value block + root cause + fix + execution + agent trace.
- `Trigger(service, namespace, reason)` — POSTs `/admin/investigate`, prints cluster ID.
- `Pause(durationMin, reason)` — POSTs `/admin/pause` with `duration_min` + `reason`.
- `Resume()` — POSTs `/admin/resume`.
- `Stats()` — GETs `/metrics-summary`, prints active/resolved_24h/failed/total.

**agent.go (501 lines):** Subcommands that run LOCALLY (no Sentinel server connection; imports `internal/agent` + `internal/codeintel` directly):
- `agent investigate <repo-path> [--logs file] [--service X] [--namespace N] [--max-turns N] [--turn-timeout D] [--compact-threshold N] [--log-level X]` — reads pod logs from `--logs` or stdin (if piped), constructs LLM router from `SENTINEL_LLM_API_KEY`/`SENTINEL_LLM_BASE_URL`/`SENTINEL_LLM_PROVIDER`/`SENTINEL_LLM_FAST_MODEL`/`SENTINEL_LLM_FRONTIER_MODEL` env vars, builds `schemas.AgentState` with `sentinel.io/repo` + `sentinel.io/branch:main` annotations, runs `agent.NewInvestigator`, marshals result to indented JSON.
- `agent codeintel <repo-path> [--max-files N] [--json]` — builds index, prints file count/symbols/unique names/call sites + symbol list.
- `agent symbols <repo-path>` — just the symbol list.
- `agent callers <repo-path> <name>` — `idx.FormatCallers(name)`.
- `agent callees <repo-path> <name>` — `idx.FindCallees(name)`.
- `agent definition <repo-path> <name>` — `idx.FindDefinition(name)`, formatted per kind (KindMethod shows receiver, KindFunction shows params, KindClass, KindType).
- Helper `reorderFlagsFirst` works around Go's `flag` package stopping at first non-flag arg.

**app.go (494 lines):** GitHub App manifest flow:
- `app setup [--github-host H] [--webhook-url U] [--setup-url U]` — 3-step flow: (1) `freePort()` → localhost callback server on random port, Codespaces-aware via `codespacesCallbackURL` (uses `CODESPACE_NAME` + `GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN`, runs `gh codespace ports visibility N:public`). (2) `appprovision.DefaultManifest` → `CreateAppURL` → `openBrowser` (xdg-open/wslview/open/rundll32 by OS). (3) `waitForCallback` (5min timeout) → `exchangeCodeForApp` (POST to `https://github.com/app-manifests/{code}/conversions`) → `postAppCredentials` (POST `/settings/github-app` with app_id, private_key PEM, webhook_secret). Prints install URL for user to visit.
- `app set-install <id>` — POSTs `/settings/github-app-install`.
- `app status` — GETs `/settings/github-app`, prints "configured ✓" + App ID + Slug + Install ID, or "NOT configured (using PAT)".
- `app remove` — DELETEs `/settings/github-app`.

**config.go:** `config view` GETs `/settings` and prints sorted key/value table; `config set <json|file>` reads inline JSON (if starts with `{` or `[`) or file path, validates locally, POSTs as `json.RawMessage` to `/settings`.

**metrics.go:** `metrics` GETs `/metrics` and writes the Prometheus exposition-format body verbatim to stdout (no parsing — bytes identical to what Prometheus scrapes, important for `promtool` pipelines).

**helpers.go:** `str` (extracts strings/float64/bool from JSON-decoded map), `getenv`, `fail`, `formatTime` (RFC3339 → "2006-01-02 15:04:05" UTC), `formatDuration` (largest whole units), `truncate` (rune-aware), `parseLastDuration` (`Nd/Nh/Nm` + Go duration).

=== 6. sentinel-bench ===
Standalone eval harness binary at `cmd/sentinel-bench/`. Talks to Sentinel via HTTP only; shells out to `kind` and `kubectl` for cluster lifecycle. Imports only `internal/logging` + `internal/version` (no other internal packages).

**main.go:** Subcommands `list`, `validate`, `run`, `results`. Global flags: `--server`, `--token`, `--cluster`, `--scenario` (repeatable), `--timeout` (default 15m), `--scenarios-dir` (default `./sentinel-bench/scenarios`), `--results-dir` (default `./sentinel-bench/results`), `--json`. Run wall-clock budget: 4h context.

**scenario.go:** YAML schema:
- `Manifest{Version int; Scenarios []ManifestScenario}` (top-level `MANIFEST.yaml`).
- `ManifestScenario{ID, Slug, Category, Severity, Difficulty, Origin, GroundTruthSHA256 string; TimeBudget Duration; TokenBudget int; CostBudgetUSD float64}`.
- `Scenario` (full): includes Manifest entry + parsed `scenario.yaml` fields + `Setup{Cluster{KindVersion, KubernetesVersion, Nodes}, Apply []string, SourceRepo, SourceRef, SourcePath}`, `Trigger{Type, Alert map[string]any}`, `Assertions []Assertion{Type, Weight, Threshold}`, `GroundTruthPath`, `EvidencePath`.
- `Duration` custom type with `UnmarshalYAML` parsing "15m"/"2h30m".
- `LoadAllScenarios(scenariosDir)` → reads `MANIFEST.yaml` → for each entry calls `LoadScenario`.
- `Validate()` checks: ID/Slug non-empty, assertions non-empty with weight ∈ [0,1], required files exist (`ground_truth.diff`, `evidence.md`, `scenario.yaml`, all `setup.apply` targets), and **ground-truth SHA256 matches manifest** (drift detection).
- `ServiceName()`/`Namespace()`/`Reason()` extract from `trigger.alert`.

**runner.go:** Core orchestration:
- Interfaces: `ClusterProvisioner{Create, Delete}`, `Applier{Apply}`, `DiffFetcher{Fetch}`.
- `Runner{Sentinel *Client; Provisioner; Applier; DiffFetcher; ScenariosDir; ResultsDir; PollInterval}`.
- `Run(ctx, RunOptions{ScenarioIDs, Timeout, ClusterName, KeepCluster, SentinelVersion}) (*Report, error)`:
  - Loads + filters scenarios.
  - Probes Sentinel version via `GET /healthz` (falls back to "unknown").
  - For each scenario calls `runOne`.
  - Tally: `pr_opened`→DBR, `pr_ci_green`→VPR, `diff_similarity`→TAR, `sentinel_automerge_approved`→FMR. Errors don't count in numerator but DO count in denominator.
  - `writeReport` writes `results/<version>/<date>/{results.json, summary.md}`.
- `runOne`: Validate → cluster lifecycle (create ephemeral `sentinel-bench-<ID>-<unix>` KinD cluster unless `--cluster` given; defer delete with 60s cleanup ctx) → `Applier.Apply(manifests)` → `Sentinel.Trigger` → `pollIncident` (every `PollInterval`, terminal states: verified/resolved/failed/rejected/escalated) → `DiffFetcher.Fetch(repo, prNumber)` if PR opened → `evaluateAssertions`.
- `evaluateAssertions`: switches on Type — `pr_opened` checks `inc.HasPR()` (PRNumber != nil && > 0); `pr_ci_green` checks `inc.CIGreen()` (proxy: `SandboxVerified`); `diff_similarity` computes `DiffSimilarity(prDiff, gtDiff) >= threshold`; `sentinel_automerge_approved` checks `inc.AutoMergeApproved()` (`AutoMergeStatus == "approved"`).
- `scenarioScore` = weighted sum of passed assertions / total weight.
- Default implementations: `kindProvisioner` (shells out to `kind create/delete cluster --name N`), `kubectlApplier` (shells out to `kubectl apply -f ...`), `githubDiffFetcher` (GETs `https://api.github.com/repos/{repo}/pulls/{N}` with `Accept: application/vnd.github.v3.diff`, optional bearer token).
- `renderMarkdown` produces a Markdown table report with metrics + per-scenario assertion pass/fail.

**client.go (sentinel-bench):** Thin HTTP client. `Trigger` POSTs `/admin/investigate` with `InvestigateRequest{Service, Namespace, Reason}` → returns `InvestigateResponse{Status, Service, ClusterID}`. `ListIncidents(limit)`, `GetIncident(id)`. `Incident` struct is a strict subset of `internal/schemas.Incident`: `IncidentID, Status, PRRepo, PRNumber *int, PRBranch, PRURL, AutoMergeStatus, SandboxVerified, SandboxSummary, FixType`. Predicates: `IsTerminal()` (verified/resolved/failed/rejected/escalated), `HasPR()` (PRNumber != nil && >0), `AutoMergeApproved()` (status=="approved"), `CIGreen()` (SandboxVerified).

**diff.go (TAR metric):** Line-recall, not AST tree-edit-distance.
```
                sum over distinct lines L of min(prCount[L], gtCount[L])
lineRecall = ----------------------------------------------------------------
                          sum over distinct lines L of gtCount[L]
```
`changedLineCounts(diff)` builds a multiset `map[line]int`, skipping `+++`/`---` file headers, `@@` hunk headers, `diff `/`index ` git metadata. The leading `+`/`-` marker is preserved so an addition is distinct from a deletion of the same text. Returns 0.0 for empty ground truth (avoids divide-by-zero). Threshold for `diff_similarity` assertion: 0.80.

**Tests:** `runner_test.go` uses `mockSentinel` httptest.Server + `fakeProvisioner`/`fakeApplier`/`fakeDiffFetcher`. Covers happy path (all 4 metrics = 100%), no-PR (all 0%), partial failure (only DBR=100%), trigger error (status="error", metrics don't count), poll timeout (clean timeout, not error), poll-waits-for-terminal (returns "investigating" first 2 polls), SHA256 drift rejection, `evaluateAssertions` unit test, `scenarioScore`, `filterScenarios`, `Incident.IsTerminal`/predicates.

**Diff tests (diff_test.go):** Identical=1.0, disjoint=0.0, empty GT=0.0, partial overlap=0.5, superset PR=1.0 (recall only), multiset multiplicity=0.75, addition distinct from deletion=0.0, headers skipped, real S001 case (1 of 2 lines = 0.5).

**Metrics summary (4 numbers):**
- **DBR** — Detection-to-Branch Rate: % scenarios where agent opened any PR within budget.
- **VPR** — Verified-Pass Rate: % scenarios where PR CI passed.
- **TAR** — Truth-Aligned Rate: % scenarios where PR diff achieved ≥80% line-recall vs ground truth.
- **FMR** — Full-Merge Rate: % scenarios where Sentinel's own auto-merge policy approved the PR. Headline number. Always FMR ≤ VPR ≤ TAR ≤ DBR.

=== 7. HELM CHART ===
**Chart.yaml:** apiVersion v2, name `sentinel`, description "Sentinel — The Autonomous AI SRE. One binary, one pod, 43MB.", version 1.6.0, appVersion "1.6.0", home github.com/karimzakzouk/sentinel. Keywords: sre, ai, automation, kubernetes, self-healing.

**values.yaml (~50 knobs):**
- `image.{registry, repository, tag, pullPolicy}` (docker.io/karimzakzouk/sentinel:1.6.0, Always).
- `sentinel.githubToken`, `sentinel.apiToken`, `sentinel.{githubAppId, githubAppPrivateKey, githubAppInstallId}`, `sentinel.allowedGithubRepos`.
- `sentinel.llm.{provider:"zai", baseUrl, apiKey, fastModel:"glm-4-flash", frontierModel:"glm-4.5", embeddingModel, embeddingDim:1536, embeddingProvider, embeddingApiKey, embeddingBaseUrl, semanticCache:false, semanticCacheThreshold:0.97, providers:[]}`.
- `sentinel.autoMerge.{enabled:false, namespaces:[], minConfidence:0.85, requireCI:true, ciTimeoutSec:900, maxPerHour:3, cooldownMin:60}`.
- `sentinel.safeActions.{enabled:false, namespaces:[], maxPerHour:3, maxScaleDeltaPct:50}`.
- `sentinel.fixVerify.{enabled:true, prometheusUrl}`.
- `sentinel.{dockerHubUsername, dockerHubToken, kanikoRegistry:"docker.io", gitHost:"github.com", githubApiUrl, defaultBranch:"main", highRiskServices, skipNamespaces (long CSV of system ns), kanikoInitImage:"alpine/git:2.43.0", kanikoExecutorImage:"gcr.io/kaniko-project/executor:v1.24.0", maxConcurrentInvestigations:3}`.
- `sentinel.rbac.{writeNamespaces:[], readSecrets:false}` (opt-in cluster-wide Secrets read for sandbox).
- `sentinel.{productionNamespaces:"prod,production", confidenceThresholdAutonomous:0.92, confidenceThresholdHumanReview:0.70, panicCpuThresholdPct:95.0, panicConnectionsThreshold:9000}`.
- `sentinel.observability.{prometheusUrl, lokiUrl, jaegerUrl, tempoUrl}`.
- `sentinel.tier1.{postMergeWatch.enabled:false, sloWatch.{enabled:false, slos:[]}, causalRCA.enabled:false, admissionWebhook.{enabled:false, addr:":8443", certDir:"/data/admission-tls"}}`.
- `sentinel.tier2.{attest.{enabled:false, logDir:"/data/attest"}, llmCostRouter.enabled:false, chaos.{enabled:false, namespaces:[], intervalMin:60}}`.
- `sentinel.{logLevel:"INFO", llmRetry.{maxRetries:3, baseDelayMs:500}, sandboxProbePaths:"/,/healthz,/readyz", sandbox.{enabled:true, required:false, maxRetries:2}, slackWebhook, slackBot.{signingSecret, botUserID, botToken}, githubWebhookSecret}`.
- `resources.{requests:{cpu:50m, memory:64Mi}, limits:{cpu:500m, memory:256Mi}}`.
- `storage.size:5Gi`, `serviceAccount.{create:true, name:""}`, `rbac.create:true`.
- `networkPolicy.enabled:false`, `ingress.{enabled:false, className:nginx, annotations:{}, hosts:[{host:sentinel.local, paths:[{path:/, pathType:Prefix}]}], tls:[]}`.
- `nodeSelector:{}`, `tolerations:[]`, `affinity:{}`.
- `metrics.serviceMonitor.{enabled:false, interval:"30s", scrapeTimeout:"10s", labels:{}}`.
- `podDisruptionBudget.{enabled:false, maxUnavailable:1}`.

**deployment.yaml:** Single-pod Deployment (replicas:1, strategy:Recreate). serviceAccountName from values or release name. `terminationGracePeriodSeconds:45`. Pod securityContext: `runAsNonRoot:true, runAsUser:65532, runAsGroup:65532, fsGroup:65532, seccompProfile: RuntimeDefault`. Container: image from values, ~50 env vars (every knob from values.yaml mapped to `SENTINEL_*`), `containerPort:8000 name:http`, liveness `/healthz` (initialDelay 5s, period 10s), readiness `/readyz` (initialDelay 3s, period 5s). VolumeMounts: `/data` (PVC) + `/tmp` (emptyDir). Container securityContext: `allowPrivilegeEscalation:false, readOnlyRootFilesystem:true, capabilities.drop:[ALL]`. Two extra resources in same file: `PersistentVolumeClaim` (5Gi RWO) + `Service` (ClusterIP, port 8000 → http).

**rbac.yaml:** ServiceAccount + ClusterRole (read-only on pods, pods/log, services, configmaps, events, namespaces, nodes, deployments, statefulsets, daemonsets, replicasets, sentinelincidents). When `sentinel.sandbox.enabled`: adds create/update/patch/delete on deployments/statefulsets/pods/services/configmaps + roles/rolebindings + namespaces (create/delete). When `sentinel.rbac.readSecrets`: adds get/list/watch on secrets. Also: networkpolicies CRUD, istio virtualservices CRUD. Plus `ClusterRoleBinding`. Plus `Role`/`RoleBinding` for Kaniko in Sentinel namespace (CRUD on configmaps/pods/services/secrets). For each `sentinel.rbac.writeNamespaces`: creates `Namespace` (labeled `sentinel.io/managed:"true"`), `Role` (CRUD on deployments/statefulsets/pods/configmaps/services/sentinelincidents), `RoleBinding`.

**crd.yaml:** `CustomResourceDefinition` for `sentinelincidents.sentinel.io`. Kind `SentinelIncident`, shortNames `["si", "incident"]`, scope `Namespaced`, version `v1alpha1` (served+storage). Spec: `service` (required, minLength 1), `namespace`, `reason`, `trigger` (enum: manual, crd_watcher, alertmanager), `repo`. Status: `phase, incidentId, rootCause, confidence, prUrl, lastUpdated` (with `x-kubernetes-preserve-unknown-fields:true`). Subresource `status: {}`. Printer columns: Service, Phase, Confidence, PR, Age.

**ingress.yaml:** Standard networking.k8s.io/v1 Ingress. ingressClassName, tls, rules with hosts + paths (default pathType Prefix). Backend service = release name, port name `http`.

**networkpolicy.yaml:** Deny-by-default. policyTypes: Ingress+Egress. Ingress: allow from any pod in namespace; if ingress.enabled, also allow from `ingress-nginx` namespace. Egress: allow DNS (UDP+TCP 53) to any namespace; allow HTTPS (443) + K8s API (6443) to 0.0.0.0/0.

**pdb.yaml:** policy/v1 PodDisruptionBudget. Uses `minAvailable` if set, else `maxUnavailable` (default 1). Selector matches `app.kubernetes.io/name:sentinel`.

**secrets.yaml:** Single Opaque Secret `(<release>-secrets)` with `stringData`: `githubToken, llmApiKey, dockerHubToken, slackSigningSecret, slackBotToken, githubWebhookSecret, apiToken, githubAppPrivateKey`. Plus separate `kaniko-docker-hub-creds` Secret of type `kubernetes.io/dockerconfigjson` (only when `dockerHubToken` set), with auth URL derived from `kanikoRegistry` (defaults to `https://index.docker.io/v1/`, custom registry gets `https://<registry>/v1/`).

**servicemonitor.yaml:** monitoring.coreos.com/v1 ServiceMonitor. Selects service by `app.kubernetes.io/name:sentinel` + instance label. Endpoint: port `http`, path `/metrics`, interval (default 30s), scrapeTimeout (default 10s). Extra labels from values.

**NOTES.txt:** Post-install instructions: get API token via `kubectl exec deploy/<release> -- printenv SENTINEL_API_TOKEN`, port-forward `svc/<release> 8000:8000`, browse to localhost:8000. Shows annotated Deployment example with `sentinel.io/repo` + `sentinel.io/branch` annotations. Lists chatops commands `/sentinel help|status|explain|trace|retry-sandbox`.

=== 8. BENCH SCENARIOS ===
Three scenarios (all `category: crashloop`, `severity: sev2`, `origin: synthetic`, `time_budget: 15m`, `token_budget: 200000`, `cost_budget_usd: 2.00`, all 4 assertions weighted 0.25 each, threshold 0.80 for diff_similarity):

**S001 — crashloop-image-typo (trivial):**
- Service: `sample-app` in `prod`. Image `ghcr.io/sentinel-bench/sample-python-app:v1.2.3` (typo: actual tag is `v1.23`).
- Symptom: ImagePullBackOff → CrashLoopBackOff, `restarts:7`.
- Source repo: `https://github.com/sentinel-bench/sample-python-app`, source path `deployment/k8s.yaml`.
- Cluster manifests: `cluster/deployment.yaml` (Namespace + Deployment with replicas:3, broken image, `containerPort:8080`), `cluster/service.yaml` (Service port 80 → 8080). **No configmap.yaml exists** — scenario.yaml's `setup.apply` only lists deployment + service.
- Ground-truth diff (SHA256 `4495a654...`): one-line patch on `deployment/k8s.yaml` line 15:
  ```diff
  -          image: ghcr.io/sentinel-bench/sample-python-app:v1.2.3
  +          image: ghcr.io/sentinel-bench/sample-python-app:v1.23
  ```

**S002 — python-keyerror-env-var (moderate):**
- Service: `payments-api` in `prod`. Flask app reads `os.environ['DATABASE_URL']` but Deployment sets `DATABASE_URLL` (double-L typo). Image `ghcr.io/sentinel-bench/payments-api:v1.4.2`.
- Symptom: Python `KeyError: 'DATABASE_URL'` traceback on import, `restarts:9`.
- Source repo: `https://github.com/sentinel-bench/sample-python-app`, source path `deployment/k8s.yaml`.
- Cluster manifests: `cluster/deployment.yaml` (Namespace + Deployment with `DATABASE_URLL` env var + `FLASK_ENV=production`), `cluster/service.yaml`.
- Ground-truth diff (SHA256 `3e1d7043...`): one-line rename on `deployment/k8s.yaml` line 25:
  ```diff
  -           - name: DATABASE_URLL
  +           - name: DATABASE_URL
  ```

**S003 — go-nil-pointer-panic (moderate):**
- Service: `inventory-api` in `prod`. Go service panics on `cfg.Server.Port` when ConfigMap omits `server:` section. Image `ghcr.io/sentinel-bench/inventory-api:v1.2.0`.
- Symptom: `panic: runtime error: invalid memory address or nil pointer dereference` at `main.go:48`, `restarts:11`.
- Source repo: `https://github.com/sentinel-bench/sample-go-service`, source path `main.go`.
- Cluster manifests: `cluster/deployment.yaml` (Namespace + Deployment with `CONFIG_PATH=/etc/inventory/config.yaml` env + volumeMount + ConfigMap volume), `cluster/service.yaml`, `cluster/configmap.yaml` (ConfigMap `inventory-api-config` with `config.yaml:` data containing only `log_level:info` + `region:us-east-1`, deliberately missing `server:` section).
- Ground-truth diff (SHA256 `ed292e70...`): 3-line nil-check addition on `main.go` after line 42 (`cfg := loadConfig(configPath)`):
  ```diff
  +       if cfg.Server == nil {
  +               log.Fatalf("config: server section is required in %s", configPath)
  +       }
  +
           port := cfg.Server.Port
  ```

=== 9. sync-version.py ===
Single source of truth for Sentinel versioning. The `VERSION` file (e.g. `1.6.0`) is canonical; the script reads it and rewrites every other place the version appears.

**Usage:**
- `python3 scripts/sync-version.py` — sync everything to VERSION file.
- `python3 scripts/sync-version.py --check` — exit 1 if drift detected (CI mode).
- `python3 scripts/sync-version.py --bump 1.1.0` — set VERSION + sync everything (validates X.Y.Z regex).

**Files synced (SYNC_TARGETS):**
1. `VERSION` (kind=`raw`) — overwrite with `version + "\n"`.
2. `chart/Chart.yaml` (kind=`chart_yaml`) — patches `^version:.*$` → `version: X.Y.Z` and `^appVersion: ".*"$` → `appVersion: "X.Y.Z"` (regex multiline, count=1).
3. `chart/values.yaml` (kind=`values_tag`) — patches `^(\s*tag:\s*)"[^"]*"$` → `\1"X.Y.Z"`.
4. `chart/values.yaml` (kind=`comment_ver`) — patches `(helm install sentinel sentinel/sentinel --version )[\d.]+` → `\1X.Y.Z`.
5. `web/package.json` (kind=`pkg_json`) — JSON parse, set `data["version"]`, write back with `json.dumps(data, indent=2) + "\n"`.
6. `README.md` (kind=`readme_install`) — patches `(helm install sentinel sentinel/sentinel --version )[\d.]+` → `\1X.Y.Z`.

**Implementation:** `patch(path, pattern, repl, check)` uses `re.sub(pattern, repl, src, count=1, flags=re.MULTILINE)`; returns False if `new == src` (no change); only writes when `not check`. `sync_one` returns `(changed, msg)` with status prefix `OK`/`DRIFT`/`SKIP`. CI mode (`--check`) never writes — only reports drift count and exits 1 if any drift. `--bump` validates `\d+\.\d+\.\d+` regex; if invalid, exits 2.

**Notable:** The Dockerfile is intentionally NOT patched (comment says "no version in source; ldflags injects at build"). The `internal/version` package is set via `-ldflags` at build time from the VERSION file.

Stage Summary:
- Phase 11 agent loop is a fully-realized N-turn ReAct loop with planning, multi-plan MCTS (opt-in, off by default for v2 backward compat), Reflexion-style critic (opt-in via CriticThreshold), LLM-based context condenser (OpenHands pattern), parallel tool execution, 4-rule stuck detector, and panic-recovery. The `PipelineAdapter` bridges it into the pipeline as an `agents.InvestigatorRunner`, sharing the same K8s/GitHub/vector-store plumbing as the legacy investigator and hot-toggling at runtime via `applyRuntimeConfig`.
- The `wiring` struct is the central dependency-injection point, replacing v5's 17 adapter structs with concrete pointers + thin methods. Compile-time assertions pin every satisfied interface.
- `sentinelctl` is a thin HTTP client (incidents/trigger/pause/resume/stats/config/metrics/app) PLUS a local agent loop runner (`agent investigate/codeintel/symbols/callers/callees/definition`) that imports internal/agent directly — useful for development without standing up K8s+GitHub+Slack.
- `sentinel-bench` is a standalone eval harness: KinD cluster lifecycle, kubectl apply, HTTP trigger, poll until terminal, fetch PR diff from GitHub, evaluate 4 assertions (pr_opened, pr_ci_green, diff_similarity ≥0.80, sentinel_automerge_approved), compute 4 metrics (DBR/VPR/TAR/FMR, headline = FMR). Diff metric is line-recall multiset intersection. 3 scenarios ship: S001 trivial image-typo, S002 moderate env-var typo, S003 moderate Go nil-pointer panic.
- Helm chart deploys as a single-pod Recreate Deployment (43MB image, ~50MB RAM) with strict securityContext (runAsNonRoot:65532, readOnlyRootFilesystem, drop ALL caps), per-namespace write Roles, opt-in Secrets read, SentinelIncident CRD, optional NetworkPolicy/Ingress/PDB/ServiceMonitor.
- Versioning is centralized via `sync-version.py` — VERSION file is canonical, 5 other files synced via regex/JSON patches; CI runs `--check` to fail on drift.
