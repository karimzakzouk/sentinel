// Package main — wiring.
//
// wiring.go collapses v5's 17 adapter structs (chatopsPipelineAdapter,
// chatopsSandboxAdapter, chatopsAutoMergeClient, prReviewVectorStoreAdapter,
// autoMergeOutcomeAdapter, alertmanagerAdapter, causalDepGraphAdapter,
// chaosK8sAdapter, incidentMemoryAdapter, ...) into a single wiring struct
// holding concrete pointers to every initialized component. Each adapter is
// now a thin method (2-7 lines) on *wiring that delegates to the concrete
// pointer. The only adapter types still defined here are the two VectorStore
// adapters (agents.VectorStore + prreview.VectorStore) — those cannot be
// methods on *wiring because Go forbids two methods with the same name and
// different return types.
package main

import (
        "context"
        "encoding/json"
        "fmt"
        "net/http"
        "sync"
        "sync/atomic"
        "time"

        "github.com/google/uuid"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/types"

        "github.com/karimzakzouk/sentinel/internal/admission"
        "github.com/karimzakzouk/sentinel/internal/agents"
        "github.com/karimzakzouk/sentinel/internal/alertmanager"
        "github.com/karimzakzouk/sentinel/internal/api"
        "github.com/karimzakzouk/sentinel/internal/attest"
        "github.com/karimzakzouk/sentinel/internal/automerge"
        "github.com/karimzakzouk/sentinel/internal/causal"
        "github.com/karimzakzouk/sentinel/internal/chaos"
        "github.com/karimzakzouk/sentinel/internal/chatops"
        "github.com/karimzakzouk/sentinel/internal/config"
        "github.com/karimzakzouk/sentinel/internal/correlation"
        "github.com/karimzakzouk/sentinel/internal/depanalysis"
        "github.com/karimzakzouk/sentinel/internal/detection"
        "github.com/karimzakzouk/sentinel/internal/emb"
        "github.com/karimzakzouk/sentinel/internal/events"
        "github.com/karimzakzouk/sentinel/internal/executors"
        "github.com/karimzakzouk/sentinel/internal/feedback"
        "github.com/karimzakzouk/sentinel/internal/k8s"
        "github.com/karimzakzouk/sentinel/internal/llm"
        "github.com/karimzakzouk/sentinel/internal/logging"
        "github.com/karimzakzouk/sentinel/internal/observability"
        "github.com/karimzakzouk/sentinel/internal/postmerge"
        "github.com/karimzakzouk/sentinel/internal/prreview"
        "github.com/karimzakzouk/sentinel/internal/runtimecfg"
        "github.com/karimzakzouk/sentinel/internal/safeactions"
        "github.com/karimzakzouk/sentinel/internal/safety"
        "github.com/karimzakzouk/sentinel/internal/slackbot"
        "github.com/karimzakzouk/sentinel/internal/slo"
        "github.com/karimzakzouk/sentinel/internal/watcher"
)

// wiring holds concrete pointers to every initialized Sentinel component.
// All adapter logic is expressed as methods on *wiring, so the wiring is
// inspectable in a single place and there is no adapter-of-adapter
// indirection.
//
// The mu field protects cfg and any other field that can be mutated by the
// runtimecfg apply callback while investigation goroutines read it. Reads
// from goroutines use RLock via Config(); the apply callback uses Lock via
// mutateConfig().
type wiring struct {
        mu  sync.RWMutex
        cfg *config.Config

        // Storage + vector memory.
        store       *feedback.Store
        vectorStore *feedback.VectorStore
        // agentsVS is the agents.VectorStore adapter over vectorStore. Held
        // on the wiring struct so applyRuntimeConfig can reuse it when
        // hot-toggling the agent loop — without it we'd have to re-construct
        // the adapter from vectorStore on every config change.
        agentsVS *agentsVectorStore

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
        promQuerier     *observability.PrometheusQuerier
        postMerge       *postmerge.Watcher
        sloWatcher      *slo.Watcher
        causalAnalyzer  *causal.Analyzer
        admissionSrv    *http.Server
        depGraphProvider depanalysis.DepGraphProvider

        // Tier 2 (optional, nil when disabled).
        attestor    *attest.Signer
        chaosRunner *chaos.Runner

        // Concurrency primitives for the investigation handler.
        ready  *atomic.Bool
        invSem chan struct{}
        invWG  sync.WaitGroup
}

// Config returns the current config under a read lock. Callers must not
// mutate the returned struct; use mutateConfig for that.
func (w *wiring) Config() *config.Config {
        w.mu.RLock()
        defer w.mu.RUnlock()
        return w.cfg
}

// mutateConfig holds the write lock while fn mutates the config in place.
// Used by the runtimecfg apply callback.
func (w *wiring) mutateConfig(fn func(cfg *config.Config)) {
        w.mu.Lock()
        defer w.mu.Unlock()
        fn(w.cfg)
}

// --- chatops.Store ---

// FindIncidentByPR satisfies chatops.Store. Loads the incident, then maps
// the schemas types into chatops's flat Incident view.
func (w *wiring) FindIncidentByPR(ctx context.Context, repo string, prNumber int) (*chatops.Incident, error) {
        inc, err := w.store.FindIncidentByPR(ctx, repo, prNumber)
        if err != nil || inc == nil {
                return nil, err
        }
        out := &chatops.Incident{
                ID:        inc.IncidentID.String(),
                Service:   inc.Cluster.Service,
                Namespace: inc.Cluster.Namespace,
                Status:    string(inc.Status),
        }
        if inc.State.RootCause != nil {
                out.RootCause = inc.State.RootCause.Statement
                out.Confidence = inc.State.RootCause.Confidence
                out.HasSchemaMigration = inc.State.RootCause.HasSchemaMigration
        }
        if inc.State.FixProposal != nil {
                out.FixType = string(inc.State.FixProposal.FixType)
                if inc.State.FixProposal.BaseBranch != nil {
                        out.BaseBranch = *inc.State.FixProposal.BaseBranch
                }
                if inc.State.FixProposal.BranchName != nil {
                        out.PRBranch = *inc.State.FixProposal.BranchName
                }
        }
        if inc.State.ExecutionResult != nil {
                if inc.State.ExecutionResult.PRURL != nil {
                        out.PRURL = *inc.State.ExecutionResult.PRURL
                }
                if inc.State.ExecutionResult.PRNumber != nil {
                        out.PRNumber = *inc.State.ExecutionResult.PRNumber
                }
                out.SandboxVerified = inc.State.ExecutionResult.SandboxVerified
                if inc.State.ExecutionResult.SandboxSummary != nil {
                        out.SandboxSummary = *inc.State.ExecutionResult.SandboxSummary
                }
        }
        out.PRRepo = repo
        out.AgentTrace = inc.State.AgentTrace
        for _, h := range inc.State.Hypotheses {
                out.Hypotheses = append(out.Hypotheses, chatops.Hypothesis{
                        ID:         h.HID,
                        Statement:  h.Statement,
                        Confidence: h.Confidence,
                        Falsified:  h.Falsified,
                })
        }
        if inc.State.GovernanceDecision != nil {
                out.PolicyVerdict = string(inc.State.GovernanceDecision.Verdict)
        }
        return out, nil
}

// LogCommandEvent satisfies chatops.Store.
func (w *wiring) LogCommandEvent(ctx context.Context, ev *chatops.CommandEvent) error {
        fbEv := &feedback.CommandEvent{
                IncidentID:       ev.IncidentID,
                PRNumber:         ev.PRNumber,
                PRRepo:           ev.PRRepo,
                Command:          ev.Command,
                Args:             ev.Args,
                Author:           ev.Author,
                AuthorPermission: ev.AuthorPermission,
                Result:           ev.Result,
                ResultDetail:     ev.ResultDetail,
                Timestamp:        ev.Timestamp,
        }
        return w.store.LogCommandEvent(ctx, fbEv)
}

// UpdateOverrides satisfies chatops.Store.
func (w *wiring) UpdateOverrides(ctx context.Context, incidentID string, sandboxOverride, ciOverride bool) error {
        id, err := uuid.Parse(incidentID)
        if err != nil {
                return fmt.Errorf("parse incident ID: %w", err)
        }
        return w.store.UpdateOverrides(ctx, id, sandboxOverride, ciOverride)
}

// --- automerge.Store ---

// UpdateAutoMergeStatus satisfies automerge.Store. The interface takes a
// string incident ID; the underlying store takes a parsed UUID.
func (w *wiring) UpdateAutoMergeStatus(ctx context.Context, incidentID, status string) error {
        return w.store.UpdateAutoMergeStatus(ctx, uuid.MustParse(incidentID), status)
}

// --- automerge.OutcomeWatcher ---

// WatchPostMerge satisfies automerge.OutcomeWatcher. Spawns the
// feedback.OutcomeWatcher (10min/24h recovery check) in a goroutine so the
// merger's tick loop is not blocked.
func (w *wiring) WatchPostMerge(ctx context.Context, incidentID, service, namespace string) {
        if w.outcomeWatcher == nil {
                return
        }
        go func() {
                defer recoverPanic("outcome watcher")
                w.outcomeWatcher.WatchWithDetails(ctx, service, namespace, incidentID, true, "code_fix", "")
        }()
}

// --- chatops.AutoMergeClient ---

// Track satisfies chatops.AutoMergeClient.
func (w *wiring) Track(incidentID, repo string, prNumber int, baseBranch string, input chatops.AutoMergeInput) {
        w.autoMergeMerger.Track(&automerge.TrackedPR{
                IncidentID: incidentID,
                Repo:       repo,
                PRNumber:   prNumber,
                BaseBranch: baseBranch,
                Input: automerge.Input{
                        IncidentID:          input.IncidentID,
                        Service:             input.Service,
                        Namespace:           input.Namespace,
                        FixType:             input.FixType,
                        HasSchemaMigration:  input.HasSchemaMigration,
                        IsHighRisk:          input.IsHighRisk,
                        Confidence:          input.Confidence,
                        SandboxVerified:     input.SandboxVerified,
                        PolicyVerdict:       input.PolicyVerdict,
                        AutoMergeAnnotation: input.AutoMergeAnnotation,
                        SandboxOverride:     input.SandboxOverride,
                        CIOverride:          input.CIOverride,
                },
        })
}

// Untrack satisfies chatops.AutoMergeClient.
func (w *wiring) Untrack(repo string, prNumber int) {
        w.autoMergeMerger.Untrack(repo, prNumber)
}

// EvaluateNow satisfies chatops.AutoMergeClient.
func (w *wiring) EvaluateNow(ctx context.Context, repo string, prNumber int) {
        w.autoMergeMerger.EvaluateNow(ctx, repo, prNumber)
}

// Status satisfies chatops.AutoMergeClient.
func (w *wiring) Status() chatops.AutoMergeStatus {
        s := w.autoMergePolicy.Status()
        return chatops.AutoMergeStatus{
                Enabled:        s.Enabled,
                Namespaces:     s.Namespaces,
                MinConfidence:  s.MinConfidence,
                RequireCIGreen: s.RequireCIGreen,
                MaxPerHour:     s.MaxPerHour,
                MergesThisHour: s.MergesThisHour,
                CooldownActive: s.CooldownActive,
                CooldownReason: s.CooldownReason,
        }
}

// RecordRegression satisfies chatops.AutoMergeClient.
func (w *wiring) RecordRegression(reason string) {
        w.autoMergePolicy.RecordRegression(reason)
}

// ClearCooldown satisfies chatops.AutoMergeClient.
func (w *wiring) ClearCooldown() {
        w.autoMergePolicy.ClearCooldown()
}

// --- chatops.PipelineRunner ---

// RetryFixProposal satisfies chatops.PipelineRunner. Loads the incident,
// then re-runs only the fix proposer with the current state.
func (w *wiring) RetryFixProposal(ctx context.Context, incidentID string) error {
        inc, err := w.store.GetIncident(ctx, uuid.MustParse(incidentID))
        if err != nil || inc == nil {
                return fmt.Errorf("incident not found: %w", err)
        }
        return w.pipeline.RetryFixProposal(ctx, &inc.State)
}

// Reinvestigate satisfies chatops.PipelineRunner. Loads the incident,
// clears hypotheses/evidence/root-cause, and re-runs the full pipeline.
func (w *wiring) Reinvestigate(ctx context.Context, incidentID string) error {
        inc, err := w.store.GetIncident(ctx, uuid.MustParse(incidentID))
        if err != nil || inc == nil {
                return fmt.Errorf("incident not found: %w", err)
        }
        inc.State.Hypotheses = nil
        inc.State.Evidence = nil
        inc.State.RootCause = nil
        inc.State.FixProposal = nil
        inc.State.Iterations = 0
        return w.pipeline.Run(ctx, &inc.State)
}

// --- chatops.SandboxRunner ---

// RunForIncident satisfies chatops.SandboxRunner. Loads the incident and
// invokes the sandbox executor.
func (w *wiring) RunForIncident(ctx context.Context, incidentID string) (bool, string, error) {
        inc, err := w.store.GetIncident(ctx, uuid.MustParse(incidentID))
        if err != nil || inc == nil {
                return false, "incident not found", err
        }
        if inc.State.FixProposal == nil {
                return false, "no fix proposal on this incident", nil
        }
        result := w.sandbox.Run(ctx, inc)
        return result.Verified, result.Summary, nil
}

// --- api.ProvidersStatusGetter ---

// ProviderStatuses satisfies api.ProvidersStatusGetter.
func (w *wiring) ProviderStatuses() []any {
        pool := w.router.Pool()
        if pool == nil {
                return []any{}
        }
        statuses := pool.Status()
        out := make([]any, len(statuses))
        for i, s := range statuses {
                out[i] = s
        }
        return out
}

// --- api.AutoMergeStatusGetter ---

// AutoMergeStatus satisfies api.AutoMergeStatusGetter. Combines the policy
// status with the merger's tracked-PR snapshot for the web UI.
func (w *wiring) AutoMergeStatus() any {
        status := w.autoMergePolicy.Status()
        tracked := w.autoMergeMerger.TrackedPRs()
        trackedOut := make([]map[string]any, 0, len(tracked))
        for _, pr := range tracked {
                trackedOut = append(trackedOut, map[string]any{
                        "incident_id": pr.IncidentID,
                        "repo":        pr.Repo,
                        "pr_number":   pr.PRNumber,
                        "base_branch": pr.BaseBranch,
                        "added_at":    pr.AddedAt,
                        "last_eval":   pr.LastEval,
                        "ci_state":    pr.Input.CIStatus.State,
                        "confidence":  pr.Input.Confidence,
                        "service":     pr.Input.Service,
                        "namespace":   pr.Input.Namespace,
                })
        }
        return map[string]any{
                "enabled":          status.Enabled,
                "namespaces":       status.Namespaces,
                "min_confidence":   status.MinConfidence,
                "require_ci_green": status.RequireCIGreen,
                "max_per_hour":     status.MaxPerHour,
                "merges_this_hour": status.MergesThisHour,
                "cooldown_active":  status.CooldownActive,
                "cooldown_until":   status.CooldownUntil,
                "cooldown_reason":  status.CooldownReason,
                "tracked_prs":      trackedOut,
        }
}

// --- admission.IncidentMemory ---

// SimilarIncidents satisfies admission.IncidentMemory. Embeds the manifest
// JSON and asks the vector store for nearest neighbours above threshold.
func (w *wiring) SimilarIncidents(ctx context.Context, obj admission.Manifest, threshold float64) ([]string, error) {
        if w.vectorStore == nil {
                return nil, nil
        }
        metas, scores, err := w.vectorStore.Search(ctx, string(obj.JSON), 5)
        if err != nil {
                return nil, err
        }
        var out []string
        for i, m := range metas {
                if i >= len(scores) {
                        break
                }
                if float64(scores[i]) >= threshold {
                        out = append(out, m.IncidentID)
                }
        }
        return out, nil
}

// --- causal.DepGraphProvider ---

// depGraphProvider is the composite provider built in wire(). It may be nil
// (observe-only mode). When nil, GraphFor returns just the anomalous nodes.
var _ causal.DepGraphProvider = (*wiring)(nil)

// GraphFor satisfies causal.DepGraphProvider. Delegates to the composite
// depanalysis provider (Istio → Linkerd → NetworkPolicy → annotation → noop)
// and converts types from depanalysis.DepGraph to causal.Graph.
func (w *wiring) GraphFor(ctx context.Context, nodes []causal.Node) (*causal.Graph, error) {
        if w.depGraphProvider == nil {
                return &causal.Graph{Nodes: nodes}, nil
        }

        // Convert causal.Node → depanalysis.DepGraphNode
        daNodes := make([]depanalysis.DepGraphNode, len(nodes))
        for i, n := range nodes {
                daNodes[i] = depanalysis.DepGraphNode{Service: n.Service, Namespace: n.Namespace}
        }

        daGraph, err := w.depGraphProvider.GraphFor(ctx, daNodes)
        if err != nil {
                return nil, err
        }

        // Convert depanalysis.DepGraph → causal.Graph
        graph := &causal.Graph{}
        for _, n := range daGraph.Nodes {
                graph.Nodes = append(graph.Nodes, causal.Node{Service: n.Service, Namespace: n.Namespace})
        }
        for _, e := range daGraph.Edges {
                graph.Edges = append(graph.Edges, causal.Edge{
                        Source: causal.Node{Service: e.Source.Service, Namespace: e.Source.Namespace},
                        Target: causal.Node{Service: e.Target.Service, Namespace: e.Target.Namespace},
                })
        }
        return graph, nil
}

// --- chaos.K8sClient ---

// ListPods satisfies chaos.K8sClient.
func (w *wiring) ListPods(ctx context.Context, namespace string) ([]string, error) {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return nil, fmt.Errorf("chaos: k8s client not initialized")
        }
        pods, err := w.k8sClient.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
        if err != nil {
                return nil, err
        }
        out := make([]string, 0, len(pods.Items))
        for _, p := range pods.Items {
                out = append(out, p.Name)
        }
        return out, nil
}

// DeletePod satisfies chaos.K8sClient.
func (w *wiring) DeletePod(ctx context.Context, namespace, podName string) error {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return fmt.Errorf("chaos: k8s client not initialized")
        }
        return w.k8sClient.Clientset().CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

// GetConfigMap satisfies chaos.K8sClient.
func (w *wiring) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return nil, fmt.Errorf("chaos: k8s client not initialized")
        }
        cm, err := w.k8sClient.Clientset().CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
        if err != nil {
                return nil, err
        }
        return cm.Data, nil
}

// PatchConfigMap satisfies chaos.K8sClient.
func (w *wiring) PatchConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return fmt.Errorf("chaos: k8s client not initialized")
        }
        patch := map[string]any{"data": data}
        patchBytes, err := json.Marshal(patch)
        if err != nil {
                return err
        }
        _, err = w.k8sClient.Clientset().CoreV1().ConfigMaps(namespace).Patch(
                ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
        )
        return err
}

// RestartDeployment satisfies chaos.K8sClient.
func (w *wiring) RestartDeployment(ctx context.Context, namespace, name string) error {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return fmt.Errorf("chaos: k8s client not initialized")
        }
        patch := map[string]any{
                "spec": map[string]any{
                        "template": map[string]any{
                                "metadata": map[string]any{
                                        "annotations": map[string]any{
                                                "sentinel.io/chaos-restart-at": fmt.Sprintf("%d", time.Now().Unix()),
                                        },
                                },
                        },
                },
        }
        patchBytes, err := json.Marshal(patch)
        if err != nil {
                return err
        }
        _, err = w.k8sClient.Clientset().AppsV1().Deployments(namespace).Patch(
                ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
        )
        return err
}

// ListDeployments satisfies chaos.K8sClient.
func (w *wiring) ListDeployments(ctx context.Context, namespace string) ([]string, error) {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return nil, fmt.Errorf("chaos: k8s client not initialized")
        }
        deps, err := w.k8sClient.Clientset().AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
        if err != nil {
                return nil, err
        }
        out := make([]string, 0, len(deps.Items))
        for _, d := range deps.Items {
                out = append(out, d.Name)
        }
        return out, nil
}

// ListConfigMaps satisfies chaos.K8sClient.
func (w *wiring) ListConfigMaps(ctx context.Context, namespace string) ([]string, error) {
        if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
                return nil, fmt.Errorf("chaos: k8s client not initialized")
        }
        cms, err := w.k8sClient.Clientset().CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
        if err != nil {
                return nil, err
        }
        out := make([]string, 0, len(cms.Items))
        for _, c := range cms.Items {
                out = append(out, c.Name)
        }
        return out, nil
}

// --- alertmanager.SignalHandler ---

// HandleAlert satisfies alertmanager.SignalHandler. Looks up the GitHub
// repo via K8s annotations when the alert payload didn't carry one, then
// forwards the alert to the detection clusterer as a SignalLogError signal.
func (w *wiring) HandleAlert(service, namespace, reason, repo, description string, _ map[string]string) {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        if repo == "" && w.k8sClient != nil {
                annotations, _ := w.k8sClient.LookupWorkloadAnnotations(ctx, service, namespace)
                if annotations != nil {
                        repo = annotations["sentinel.io/repo"]
                }
        }

        chatopsLogInfo("alertmanager → clusterer",
                "service", service,
                "namespace", namespace,
                "reason", reason,
                "repo", repo,
        )

        w.clusterer.Send(ctx, detection.Signal{
                Type:      detection.SignalLogError,
                Service:   service,
                Namespace: namespace,
                Reason:    reason,
                Repo:      repo,
                Timestamp: time.Now(),
        })
}

// chatopsLogInfo is a thin wrapper that exists only so the alertmanager
// adapter above stays self-contained. Defined here rather than inlined so
// future adapters in this file have an obvious call site.
func chatopsLogInfo(msg string, args ...any) { logging.Info(msg, args...) }

// --- VectorStore adapters (cannot be methods on *wiring due to method-name collision) ---

// agentsVectorStore adapts *feedback.VectorStore to agents.VectorStore.
// Defined as a separate type because agents.VectorStore and
// prreview.VectorStore both require a method named Search with different
// return types — Go cannot express that on a single receiver.
type agentsVectorStore struct {
        vs *feedback.VectorStore
}

// Search satisfies agents.VectorStore.
func (a *agentsVectorStore) Search(ctx context.Context, text string, k int) ([]*agents.VectorMetadata, []float32, error) {
        metas, scores, err := a.vs.Search(ctx, text, k)
        if err != nil || len(metas) == 0 {
                return nil, nil, err
        }
        out := make([]*agents.VectorMetadata, len(metas))
        for i, m := range metas {
                out[i] = &agents.VectorMetadata{
                        IncidentID: m.IncidentID,
                        Service:    m.Service,
                        RootCause:  m.RootCause,
                        FixType:    m.FixType,
                        FixPattern: m.FixPattern,
                        Outcome:    m.Outcome,
                }
        }
        return out, scores, nil
}

// UsingHashFallback satisfies agents.VectorStore.
func (a *agentsVectorStore) UsingHashFallback() bool {
        return a.vs.UsingHashFallback()
}

// prreviewVectorStore adapts *feedback.VectorStore to prreview.VectorStore.
// See agentsVectorStore for why this cannot be a method on *wiring.
type prreviewVectorStore struct {
        vs *feedback.VectorStore
}

// Search satisfies prreview.VectorStore.
func (a *prreviewVectorStore) Search(ctx context.Context, text string, k int) ([]*prreview.VectorMetadata, []float32, error) {
        metas, scores, err := a.vs.Search(ctx, text, k)
        if err != nil || len(metas) == 0 {
                return nil, nil, err
        }
        out := make([]*prreview.VectorMetadata, len(metas))
        for i, m := range metas {
                out[i] = &prreview.VectorMetadata{
                        IncidentID: m.IncidentID,
                        Service:    m.Service,
                        RootCause:  m.RootCause,
                        FixType:    m.FixType,
                        FixPattern: m.FixPattern,
                        Outcome:    m.Outcome,
                }
        }
        return out, scores, nil
}

// UsingHashFallback satisfies prreview.VectorStore.
func (a *prreviewVectorStore) UsingHashFallback() bool {
        return a.vs.UsingHashFallback()
}

// Compile-time assertions that *wiring satisfies every interface a wired
// component expects. If one of these breaks, the corresponding method
// above needs to be updated.
var (
        _ chatops.Store              = (*wiring)(nil)
        _ chatops.AutoMergeClient    = (*wiring)(nil)
        _ chatops.PipelineRunner     = (*wiring)(nil)
        _ chatops.SandboxRunner      = (*wiring)(nil)
        _ automerge.Store            = (*wiring)(nil)
        _ automerge.OutcomeWatcher   = (*wiring)(nil)
        _ alertmanager.SignalHandler = (*wiring)(nil)
        _ admission.IncidentMemory   = (*wiring)(nil)
        _ causal.DepGraphProvider    = (*wiring)(nil)
        _ chaos.K8sClient            = (*wiring)(nil)
        _ agents.VectorStore         = (*agentsVectorStore)(nil)
        _ prreview.VectorStore       = (*prreviewVectorStore)(nil)
)
