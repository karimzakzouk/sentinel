package chaos

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/attest"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// K8sClient is the Kubernetes API surface the chaos runner needs.
// *k8s.Client satisfies this via K8sAdapter; tests provide a fake.
type K8sClient interface {
	// ListPods returns the names of all pods in namespace.
	ListPods(ctx context.Context, namespace string) ([]string, error)
	// DeletePod deletes one pod by name.
	DeletePod(ctx context.Context, namespace, podName string) error
	// GetConfigMap returns the data of one ConfigMap.
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	// PatchConfigMap replaces the data of one ConfigMap.
	PatchConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	// RestartDeployment triggers a rollout restart of one Deployment.
	RestartDeployment(ctx context.Context, namespace, name string) error
	// ListDeployments returns the names of all Deployments in namespace.
	ListDeployments(ctx context.Context, namespace string) ([]string, error)
	// ListConfigMaps returns the names of all ConfigMaps in namespace.
	ListConfigMaps(ctx context.Context, namespace string) ([]string, error)
}

// Restore is called to undo an injection. Must be idempotent.
type Restore func(ctx context.Context) error

// Experiment is a single chaos injection. Implementations must be safe
// to call concurrently with the runner's ticker (the runner itself
// serializes calls — only one experiment runs at a time).
type Experiment interface {
	// Name returns a short, kebab-case identifier (e.g. "kill-pod").
	Name() string
	// Run injects the failure into namespace and returns a Restore
	// func. If the experiment does not need a restore (e.g. kill-pod,
	// which the Deployment controller auto-heals), return a no-op.
	Run(ctx context.Context, namespace string) (Restore, error)
	// Cleanup is called after Run + the trigger callback completes,
	// regardless of outcome. Use it to remove any leftover state that
	// Restore did not handle (rare). Must be idempotent.
	Cleanup(ctx context.Context, namespace string) error
}

// Outcome is what happened during one chaos run.
type Outcome struct {
	Experiment   string
	Namespace    string
	InjectedAt   time.Time
	Detected     bool
	DetectedAt   time.Time
	TimeToDetect time.Duration
	Fixed        bool
	TimeToFix    time.Duration
	AutoMerged   bool
	Notes        string
}

// TriggerCallback is invoked after an experiment runs. The callback
// typically starts Sentinel's investigation pipeline against the
// synthetic incident described by exp + namespace, then returns the
// outcome. If nil, the runner skips the investigation and auto-restores.
type TriggerCallback func(ctx context.Context, exp Experiment, namespace string) Outcome

// Runner schedules and executes chaos experiments.
type Runner struct {
	mu          sync.Mutex
	k8s         K8sClient
	cfg         *config.Config
	trigger     TriggerCallback
	attestor    *attest.Signer
	experiments []Experiment
	history     []Outcome
	stopCh      chan struct{}
	rng         *rand.Rand
	dryRun      bool
	onComplete  func(Outcome)
}

// New returns a Runner with the three built-in experiments registered.
// cfg may be nil — in that case the runner has no allowed namespaces
// and Start() will spin without running anything (callers can still
// invoke experiments directly via RunExperiment for tests).
func New(k8s K8sClient, cfg *config.Config, trigger TriggerCallback) *Runner {
	r := &Runner{
		k8s:     k8s,
		cfg:     cfg,
		trigger: trigger,
		stopCh:  make(chan struct{}),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	r.experiments = r.defaultExperiments()
	return r
}

// SetAttestor wires an Ed25519 signer. When set, the runner signs an
// attestation before each injection and after each outcome. If not
// called, no attestation is signed (the audit trail simply has fewer
// entries). Safe to call with nil to disable.
func (r *Runner) SetAttestor(s *attest.Signer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attestor = s
}

// SetDryRun toggles dry-run mode. In dry-run, the runner logs what it
// would do but does NOT call any K8s API and does NOT invoke the
// trigger callback. Useful for validating config without breaking
// staging.
func (r *Runner) SetDryRun(b bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dryRun = b
}

// OnComplete registers a callback invoked after every experiment with
// the final Outcome. Use it to persist eval data to disk or write a
// few-shot example to the feedback store.
func (r *Runner) OnComplete(fn func(Outcome)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onComplete = fn
}

// AddExperiment registers a custom experiment. Additive only — there
// is no RemoveExperiment (operators who want a different set should
// construct their own Runner and not call the default constructor).
func (r *Runner) AddExperiment(e Experiment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.experiments = append(r.experiments, e)
}

// Experiments returns the registered experiments (for inspection / tests).
func (r *Runner) Experiments() []Experiment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Experiment, len(r.experiments))
	copy(out, r.experiments)
	return out
}

// History returns a snapshot of past experiment outcomes.
func (r *Runner) History() []Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Outcome, len(r.history))
	copy(out, r.history)
	return out
}

// Start runs the chaos schedule. Blocks until ctx is canceled or Stop
// is called. The interval is cfg.ChaosIntervalMin minutes (default 60
// if unset or zero). If no namespaces are configured or no experiments
// are registered, Start returns immediately.
func (r *Runner) Start(ctx context.Context) {
	interval := r.interval()
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	if !r.hasWork() {
		logging.Warn("chaos: no allowed namespaces or no experiments registered; Start() will spin",
			"interval", interval)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logging.Info("chaos: runner started",
		"interval", interval,
		"experiments", len(r.experiments),
		"allowed_ns", r.allowedNamespaces(),
		"dry_run", r.dryRun,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.runOne(ctx)
		}
	}
}

// Stop signals the runner to exit. Safe to call multiple times —
// subsequent calls are no-ops.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
}

// RunExperiment runs one specific experiment against namespace. It is
// the synchronous, single-shot entry point used by tests and by the
// scheduled loop. Returns the Outcome.
func (r *Runner) RunExperiment(ctx context.Context, exp Experiment, namespace string) Outcome {
	outcome := Outcome{
		Experiment: exp.Name(),
		Namespace:  namespace,
		InjectedAt: time.Now().UTC(),
	}

	r.mu.Lock()
	dryRun := r.dryRun
	attestor := r.attestor
	r.mu.Unlock()

	if dryRun {
		logging.Info("chaos: dry-run, skipping injection",
			"experiment", exp.Name(), "namespace", namespace)
		outcome.Notes = "dry-run: no injection performed"
		return outcome
	}

	// Attest the injection BEFORE doing it — auditors need to know this
	// was intentional.
	if attestor != nil {
		_ = attestor.Sign(ctx, attest.Attestation{
			Action: "chaos.inject",
			Actor:  "sentinel",
			Reason: fmt.Sprintf("intentional chaos experiment: %s against %s", exp.Name(), namespace),
			Evidence: []attest.Evidence{
				{Kind: "chaos", Payload: map[string]any{
					"experiment": exp.Name(),
					"namespace":  namespace,
				}},
			},
		})
	}

	logging.Info("chaos: injecting", "experiment", exp.Name(), "namespace", namespace)

	restore, err := exp.Run(ctx, namespace)
	if err != nil {
		logging.Error("chaos: injection failed", "experiment", exp.Name(), "err", err)
		outcome.Notes = fmt.Sprintf("injection failed: %v", err)
		// Still try cleanup.
		_ = exp.Cleanup(ctx, namespace)
		r.recordOutcome(outcome)
		return outcome
	}

	// Hand off to Sentinel via the trigger callback.
	if r.trigger != nil {
		triggered := r.trigger(ctx, exp, namespace)
		// Preserve the original InjectedAt (the trigger callback
		// doesn't know when we injected).
		triggered.InjectedAt = outcome.InjectedAt
		outcome = triggered
		outcome.Experiment = exp.Name()
		outcome.Namespace = namespace
	}

	// Auto-restore if Sentinel didn't fix it (don't leave staging broken).
	if !outcome.Fixed && restore != nil {
		logging.Warn("chaos: Sentinel failed to fix, auto-restoring", "experiment", exp.Name())
		if err := restore(ctx); err != nil {
			logging.Error("chaos: RESTORE FAILED — staging may be broken", "err", err)
			if outcome.Notes == "" {
				outcome.Notes = "auto-restore FAILED: " + err.Error()
			} else {
				outcome.Notes += "; auto-restore FAILED: " + err.Error()
			}
		} else {
			if outcome.Notes == "" {
				outcome.Notes = "auto-restored after Sentinel failed to fix within timeout"
			}
		}
	}

	// Always run cleanup.
	_ = exp.Cleanup(ctx, namespace)

	// Attest the outcome.
	if attestor != nil {
		_ = attestor.Sign(ctx, attest.Attestation{
			Action:  "chaos.outcome",
			Actor:   "sentinel",
			Outcome: outcome.Fixed,
			Reason: fmt.Sprintf("chaos experiment %s: detected=%v fixed=%v ttd=%s ttf=%s",
				exp.Name(), outcome.Detected, outcome.Fixed, outcome.TimeToDetect, outcome.TimeToFix),
			Evidence: []attest.Evidence{
				{Kind: "chaos-outcome", Payload: map[string]any{
					"detected":       outcome.Detected,
					"fixed":          outcome.Fixed,
					"time_to_detect": outcome.TimeToDetect.String(),
					"time_to_fix":    outcome.TimeToFix.String(),
				}},
			},
		})
	}

	r.recordOutcome(outcome)

	logging.Info("chaos: experiment complete",
		"experiment", exp.Name(),
		"namespace", namespace,
		"detected", outcome.Detected,
		"fixed", outcome.Fixed,
		"auto_merged", outcome.AutoMerged,
		"ttd", outcome.TimeToDetect,
		"ttf", outcome.TimeToFix,
	)
	return outcome
}

// runOne is the scheduled-loop entry point. Picks a random experiment
// and a random allowed namespace, then delegates to RunExperiment.
func (r *Runner) runOne(ctx context.Context) {
	r.mu.Lock()
	if len(r.experiments) == 0 {
		r.mu.Unlock()
		return
	}
	exp := r.experiments[r.rng.Intn(len(r.experiments))]
	r.mu.Unlock()

	ns := r.pickNamespace()
	if ns == "" {
		return
	}

	r.RunExperiment(ctx, exp, ns)
}

// recordOutcome appends to history and invokes onComplete.
func (r *Runner) recordOutcome(o Outcome) {
	r.mu.Lock()
	r.history = append(r.history, o)
	cb := r.onComplete
	r.mu.Unlock()

	if cb != nil {
		cb(o)
	}
}

// interval returns the schedule interval from config.
func (r *Runner) interval() time.Duration {
	if r.cfg == nil || r.cfg.ChaosIntervalMin <= 0 {
		return 60 * time.Minute
	}
	return time.Duration(r.cfg.ChaosIntervalMin) * time.Minute
}

// allowedNamespaces returns a copy of the configured namespace allow-list.
func (r *Runner) allowedNamespaces() []string {
	if r.cfg == nil {
		return nil
	}
	out := make([]string, len(r.cfg.ChaosNamespaces))
	copy(out, r.cfg.ChaosNamespaces)
	return out
}

// pickNamespace returns a random allowed namespace, or "" if none.
func (r *Runner) pickNamespace() string {
	ns := r.allowedNamespaces()
	if len(ns) == 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return ns[r.rng.Intn(len(ns))]
}

// hasWork reports whether the runner has both experiments and namespaces.
func (r *Runner) hasWork() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.experiments) > 0 && len(r.allowedNamespaces()) > 0
}

// defaultExperiments returns the built-in chaos set.
func (r *Runner) defaultExperiments() []Experiment {
	return []Experiment{
		NewKillPod(r.k8s, r.rng),
		NewMutateConfigMap(r.k8s, r.rng),
		NewRestartDeployment(r.k8s, r.rng),
	}
}

// --- built-in experiments ---

// KillPod deletes a random pod in the namespace. The Deployment
// controller recreates it; Restore is a no-op.
type KillPod struct {
	k8s K8sClient
	rng *rand.Rand
}

// NewKillPod returns a KillPod experiment.
func NewKillPod(k8s K8sClient, rng *rand.Rand) *KillPod {
	return &KillPod{k8s: k8s, rng: rng}
}

// Name returns "kill-pod".
func (e *KillPod) Name() string { return "kill-pod" }

// Run lists pods in namespace, deletes one at random, and returns a
// no-op Restore (the Deployment controller recreates the pod).
func (e *KillPod) Run(ctx context.Context, namespace string) (Restore, error) {
	pods, err := e.k8s.ListPods(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods in %s", namespace)
	}
	victim := pods[e.rng.Intn(len(pods))]
	if err := e.k8s.DeletePod(ctx, namespace, victim); err != nil {
		return nil, fmt.Errorf("delete pod %s: %w", victim, err)
	}
	logging.Info("chaos: kill-pod deleted pod", "namespace", namespace, "pod", victim)
	return func(_ context.Context) error { return nil }, nil
}

// Cleanup is a no-op for kill-pod.
func (e *KillPod) Cleanup(_ context.Context, _ string) error { return nil }

// MutateConfigMap adds a sentinel-chaos-bad-key entry to a random
// ConfigMap in the namespace. Restore patches the ConfigMap back to
// its original data.
type MutateConfigMap struct {
	k8s K8sClient
	rng *rand.Rand
}

// NewMutateConfigMap returns a MutateConfigMap experiment.
func NewMutateConfigMap(k8s K8sClient, rng *rand.Rand) *MutateConfigMap {
	return &MutateConfigMap{k8s: k8s, rng: rng}
}

// Name returns "mutate-configmap".
func (e *MutateConfigMap) Name() string { return "mutate-configmap" }

// Run picks a random ConfigMap, snapshots its data, injects a bad key,
// patches the ConfigMap, and returns a Restore that re-patches the
// original data.
func (e *MutateConfigMap) Run(ctx context.Context, namespace string) (Restore, error) {
	cms, err := e.k8s.ListConfigMaps(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list configmaps: %w", err)
	}
	if len(cms) == 0 {
		return nil, fmt.Errorf("no configmaps in %s", namespace)
	}
	target := cms[e.rng.Intn(len(cms))]

	original, err := e.k8s.GetConfigMap(ctx, namespace, target)
	if err != nil {
		return nil, fmt.Errorf("get configmap %s: %w", target, err)
	}

	bad := make(map[string]string, len(original)+1)
	for k, v := range original {
		bad[k] = v
	}
	bad["sentinel-chaos-bad-key"] = "INVALID_VALUE_X"

	if err := e.k8s.PatchConfigMap(ctx, namespace, target, bad); err != nil {
		return nil, fmt.Errorf("patch configmap %s: %w", target, err)
	}
	logging.Info("chaos: mutate-configmap patched", "namespace", namespace, "configmap", target)

	restore := func(ctx context.Context) error {
		if err := e.k8s.PatchConfigMap(ctx, namespace, target, original); err != nil {
			return fmt.Errorf("restore configmap %s: %w", target, err)
		}
		return nil
	}
	return restore, nil
}

// Cleanup is a no-op for mutate-configmap (Restore handles it).
func (e *MutateConfigMap) Cleanup(_ context.Context, _ string) error { return nil }

// RestartDeployment triggers a rollout restart of a random Deployment
// in the namespace. Restore is a no-op (the Deployment controller
// handles rolling out the restart).
type RestartDeployment struct {
	k8s K8sClient
	rng *rand.Rand
}

// NewRestartDeployment returns a RestartDeployment experiment.
func NewRestartDeployment(k8s K8sClient, rng *rand.Rand) *RestartDeployment {
	return &RestartDeployment{k8s: k8s, rng: rng}
}

// Name returns "restart-deployment".
func (e *RestartDeployment) Name() string { return "restart-deployment" }

// Run picks a random Deployment and triggers a rollout restart.
func (e *RestartDeployment) Run(ctx context.Context, namespace string) (Restore, error) {
	deps, err := e.k8s.ListDeployments(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	if len(deps) == 0 {
		return nil, fmt.Errorf("no deployments in %s", namespace)
	}
	target := deps[e.rng.Intn(len(deps))]
	if err := e.k8s.RestartDeployment(ctx, namespace, target); err != nil {
		return nil, fmt.Errorf("restart deployment %s: %w", target, err)
	}
	logging.Info("chaos: restart-deployment triggered", "namespace", namespace, "deployment", target)
	return func(_ context.Context) error { return nil }, nil
}

// Cleanup is a no-op for restart-deployment.
func (e *RestartDeployment) Cleanup(_ context.Context, _ string) error { return nil }
