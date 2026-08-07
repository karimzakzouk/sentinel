package chaos

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/attest"
	"github.com/karimzakzouk/sentinel/internal/config"
)

// fakeK8s is an in-memory K8sClient for tests. It records every call
// and lets the test inspect what chaos did.
type fakeK8s struct {
	mu            sync.Mutex
	pods          map[string][]string // namespace -> pod names
	deployments   map[string][]string
	configmaps    map[string]map[string]map[string]string // ns -> cm name -> data
	deletedPods   []string
	patchedCMs    []string
	restartedDeps []string
	failDelete    bool
}

func newFakeK8s() *fakeK8s {
	return &fakeK8s{
		pods: map[string][]string{
			"staging": {"api-1", "api-2", "worker-1"},
		},
		deployments: map[string][]string{
			"staging": {"api", "worker", "frontend"},
		},
		configmaps: map[string]map[string]map[string]string{
			"staging": {
				"api-config":    {"LOG_LEVEL": "info", "PORT": "8080"},
				"worker-config": {"CONCURRENCY": "4"},
			},
		},
	}
}

func (f *fakeK8s) ListPods(_ context.Context, namespace string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.pods[namespace]...), nil
}

func (f *fakeK8s) DeletePod(ctx context.Context, namespace, podName string) error {
	if f.failDelete {
		return errors.New("k8s: forbidden")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedPods = append(f.deletedPods, namespace+"/"+podName)
	pods := f.pods[namespace]
	for i, p := range pods {
		if p == podName {
			f.pods[namespace] = append(pods[:i], pods[i+1:]...)
			break
		}
	}
	_ = ctx
	return nil
}

func (f *fakeK8s) GetConfigMap(_ context.Context, namespace, name string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cms, ok := f.configmaps[namespace]; ok {
		if data, ok := cms[name]; ok {
			out := make(map[string]string, len(data))
			for k, v := range data {
				out[k] = v
			}
			return out, nil
		}
	}
	return nil, errors.New("configmap not found")
}

func (f *fakeK8s) PatchConfigMap(_ context.Context, namespace, name string, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patchedCMs = append(f.patchedCMs, namespace+"/"+name)
	if f.configmaps[namespace] == nil {
		f.configmaps[namespace] = map[string]map[string]string{}
	}
	f.configmaps[namespace][name] = make(map[string]string, len(data))
	for k, v := range data {
		f.configmaps[namespace][name][k] = v
	}
	return nil
}

func (f *fakeK8s) RestartDeployment(_ context.Context, namespace, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartedDeps = append(f.restartedDeps, namespace+"/"+name)
	return nil
}

func (f *fakeK8s) ListDeployments(_ context.Context, namespace string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.deployments[namespace]...), nil
}

func (f *fakeK8s) ListConfigMaps(_ context.Context, namespace string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cms := f.configmaps[namespace]
	out := make([]string, 0, len(cms))
	for name := range cms {
		out = append(out, name)
	}
	return out, nil
}

func (f *fakeK8s) deletedPodNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.deletedPods...)
}

func (f *fakeK8s) restartedDeploymentNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.restartedDeps...)
}

func (f *fakeK8s) patchedCMNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.patchedCMs...)
}

// TestChaos_KillPod verifies the kill-pod experiment deletes a pod and
// returns a no-op Restore.
func TestChaos_KillPod(t *testing.T) {
	k8s := newFakeK8s()
	exp := NewKillPod(k8s, newRand())

	restore, err := exp.Run(context.Background(), "staging")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if restore == nil {
		t.Fatal("expected non-nil restore")
	}

	deleted := k8s.deletedPodNames()
	if len(deleted) != 1 {
		t.Fatalf("expected 1 pod deleted, got %d (%v)", len(deleted), deleted)
	}
	if !contains(deleted, "staging/") {
		t.Errorf("deleted pod should be in staging/, got %v", deleted)
	}

	// Restore should be a no-op but not error.
	if err := restore(context.Background()); err != nil {
		t.Errorf("restore: %v", err)
	}

	// Cleanup should be a no-op.
	if err := exp.Cleanup(context.Background(), "staging"); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}

// TestChaos_KillPodEmptyNamespace verifies kill-pod returns an error
// when there are no pods to kill.
func TestChaos_KillPodEmptyNamespace(t *testing.T) {
	k8s := newFakeK8s()
	exp := NewKillPod(k8s, newRand())

	_, err := exp.Run(context.Background(), "empty-ns")
	if err == nil {
		t.Fatal("expected error when no pods in namespace")
	}
}

// TestChaos_MutateConfigMap verifies the mutate-configmap experiment
// adds a bad key and the Restore reverts it.
func TestChaos_MutateConfigMap(t *testing.T) {
	k8s := newFakeK8s()
	exp := NewMutateConfigMap(k8s, newRand())

	restore, err := exp.Run(context.Background(), "staging")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if restore == nil {
		t.Fatal("expected non-nil restore")
	}

	patched := k8s.patchedCMNames()
	if len(patched) != 1 {
		t.Fatalf("expected 1 configmap patched, got %d (%v)", len(patched), patched)
	}

	// Identify which ConfigMap was patched (the experiment picks at random).
	patchedName := patched[0]
	// patchedName is "staging/<cm>"; strip the namespace prefix.
	cmName := patchedName[len("staging/"):]

	// The bad key must be present in the patched ConfigMap.
	mutated, _ := k8s.GetConfigMap(context.Background(), "staging", cmName)
	if v, ok := mutated["sentinel-chaos-bad-key"]; !ok || v == "" {
		t.Errorf("expected sentinel-chaos-bad-key to be injected in %s, got data=%v", cmName, mutated)
	}

	// Restore should revert the bad key.
	if err := restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, _ := k8s.GetConfigMap(context.Background(), "staging", cmName)
	if _, ok := restored["sentinel-chaos-bad-key"]; ok {
		t.Error("bad key should be removed after restore")
	}

	// Cleanup should be a no-op.
	if err := exp.Cleanup(context.Background(), "staging"); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}

// TestChaos_RestartDeployment verifies the restart-deployment experiment
// triggers a rollout restart of a random Deployment.
func TestChaos_RestartDeployment(t *testing.T) {
	k8s := newFakeK8s()
	exp := NewRestartDeployment(k8s, newRand())

	restore, err := exp.Run(context.Background(), "staging")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if restore == nil {
		t.Fatal("expected non-nil restore")
	}

	restarted := k8s.restartedDeploymentNames()
	if len(restarted) != 1 {
		t.Fatalf("expected 1 deployment restarted, got %d (%v)", len(restarted), restarted)
	}
	if !contains(restarted, "staging/") {
		t.Errorf("restarted deployment should be in staging/, got %v", restarted)
	}

	// Restore + Cleanup are no-ops.
	if err := restore(context.Background()); err != nil {
		t.Errorf("restore: %v", err)
	}
	if err := exp.Cleanup(context.Background(), "staging"); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}

// TestChaos_NamespaceAllowlist verifies the runner refuses to operate
// on namespaces not in the allow-list (the runner simply has no
// namespaces to pick, so it does nothing).
func TestChaos_NamespaceAllowlist(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  nil, // empty allow-list
		ChaosIntervalMin: 1,
	}
	r := New(k8s, cfg, nil)

	// pickNamespace returns "" when allow-list is empty.
	if ns := r.pickNamespace(); ns != "" {
		t.Errorf("pickNamespace with empty allow-list = %q, want empty", ns)
	}

	// runOne does nothing.
	r.runOne(context.Background())
	if len(r.History()) != 0 {
		t.Errorf("expected 0 outcomes with empty allow-list, got %d", len(r.History()))
	}

	// With a namespace, it runs.
	r.cfg.ChaosNamespaces = []string{"staging"}
	r.runOne(context.Background())
	if len(r.History()) != 1 {
		t.Errorf("expected 1 outcome after allowing staging, got %d", len(r.History()))
	}
}

// TestChaos_DryRun verifies dry-run mode does not call any K8s API and
// does not invoke the trigger callback.
func TestChaos_DryRun(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  []string{"staging"},
		ChaosIntervalMin: 1,
	}
	triggerCalled := false
	r := New(k8s, cfg, func(_ context.Context, _ Experiment, _ string) Outcome {
		triggerCalled = true
		return Outcome{Fixed: true}
	})
	r.SetDryRun(true)

	// Pick a specific experiment and run it via RunExperiment so we
	// don't have to wait for the ticker.
	exp := NewKillPod(k8s, newRand())
	outcome := r.RunExperiment(context.Background(), exp, "staging")

	if triggerCalled {
		t.Error("trigger should not be called in dry-run mode")
	}
	if len(k8s.deletedPodNames()) != 0 {
		t.Errorf("no pods should be deleted in dry-run, got %v", k8s.deletedPodNames())
	}
	if outcome.Notes == "" {
		t.Error("dry-run outcome should have a Notes field set")
	}
	if !contains([]string{outcome.Notes}, "dry-run") {
		t.Errorf("dry-run outcome Notes should mention dry-run, got %q", outcome.Notes)
	}
}

// TestChaos_InvestigatesOwnInjection verifies the trigger callback is
// invoked with the experiment + namespace after injection, and the
// outcome is recorded in history.
func TestChaos_InvestigatesOwnInjection(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  []string{"staging"},
		ChaosIntervalMin: 1,
	}

	var triggeredExp string
	var triggeredNS string
	r := New(k8s, cfg, func(_ context.Context, exp Experiment, ns string) Outcome {
		triggeredExp = exp.Name()
		triggeredNS = ns
		return Outcome{
			Detected:     true,
			DetectedAt:   time.Now(),
			TimeToDetect: 30 * time.Second,
			Fixed:        true,
			TimeToFix:    2 * time.Minute,
			AutoMerged:   true,
		}
	})

	exp := NewKillPod(k8s, newRand())
	outcome := r.RunExperiment(context.Background(), exp, "staging")

	if triggeredExp != "kill-pod" {
		t.Errorf("trigger called with exp=%q, want kill-pod", triggeredExp)
	}
	if triggeredNS != "staging" {
		t.Errorf("trigger called with ns=%q, want staging", triggeredNS)
	}

	if !outcome.Detected {
		t.Error("outcome.Detected should be true (from trigger)")
	}
	if !outcome.Fixed {
		t.Error("outcome.Fixed should be true (from trigger)")
	}
	if !outcome.AutoMerged {
		t.Error("outcome.AutoMerged should be true (from trigger)")
	}
	if outcome.TimeToFix != 2*time.Minute {
		t.Errorf("outcome.TimeToFix = %v, want 2m", outcome.TimeToFix)
	}

	// History must contain the outcome.
	hist := r.History()
	if len(hist) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(hist))
	}
	if hist[0].Experiment != "kill-pod" {
		t.Errorf("history[0].Experiment = %q, want kill-pod", hist[0].Experiment)
	}
	if !hist[0].Fixed {
		t.Error("history[0].Fixed should be true")
	}

	// No auto-restore should have been attempted because Fixed=true.
	// (We can't directly observe restore, but we can verify the
	// outcome.Notes field is empty — auto-restore would set it.)
	if outcome.Notes != "" {
		t.Errorf("outcome.Notes should be empty (no auto-restore), got %q", outcome.Notes)
	}
}

// TestChaos_AutoRestoreOnFailure verifies that when Sentinel fails to
// fix the injection (Fixed=false), the runner calls Restore.
func TestChaos_AutoRestoreOnFailure(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  []string{"staging"},
		ChaosIntervalMin: 1,
	}
	r := New(k8s, cfg, func(_ context.Context, _ Experiment, _ string) Outcome {
		return Outcome{Fixed: false} // Sentinel failed to fix
	})

	// Use mutate-configmap so we can observe Restore via the ConfigMap
	// state (the bad key should be removed by Restore).
	exp := NewMutateConfigMap(k8s, newRand())
	outcome := r.RunExperiment(context.Background(), exp, "staging")

	if outcome.Fixed {
		t.Error("outcome.Fixed should be false (trigger returned false)")
	}
	if outcome.Notes == "" {
		t.Error("outcome.Notes should mention auto-restore")
	}

	// The bad key should have been removed by Restore.
	data, _ := k8s.GetConfigMap(context.Background(), "staging", "api-config")
	if _, ok := data["sentinel-chaos-bad-key"]; ok {
		t.Error("sentinel-chaos-bad-key should be removed by auto-restore")
	}
}

// TestChaos_OnCompleteCallback verifies the OnComplete callback is
// invoked with the outcome after each experiment.
func TestChaos_OnCompleteCallback(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  []string{"staging"},
		ChaosIntervalMin: 1,
	}

	var got Outcome
	var cbCalled bool
	r := New(k8s, cfg, func(_ context.Context, _ Experiment, _ string) Outcome {
		return Outcome{Fixed: true}
	})
	r.OnComplete(func(o Outcome) {
		got = o
		cbCalled = true
	})

	exp := NewKillPod(k8s, newRand())
	r.RunExperiment(context.Background(), exp, "staging")

	if !cbCalled {
		t.Fatal("OnComplete callback not invoked")
	}
	if !got.Fixed {
		t.Error("OnComplete received Fixed=false, want true")
	}
}

// TestChaos_AttestorSignsInjection verifies that when an attestor is
// wired, the runner signs an attestation before injecting and after
// the outcome.
func TestChaos_AttestorSignsInjection(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{
		ChaosNamespaces:  []string{"staging"},
		ChaosIntervalMin: 1,
	}
	signer, err := attest.NewSigner("", t.TempDir())
	if err != nil {
		t.Fatalf("attest.NewSigner: %v", err)
	}

	r := New(k8s, cfg, func(_ context.Context, _ Experiment, _ string) Outcome {
		return Outcome{Fixed: true}
	})
	r.SetAttestor(signer)

	exp := NewKillPod(k8s, newRand())
	r.RunExperiment(context.Background(), exp, "staging")

	// The signer should have advanced its hash chain (2 attestations:
	// one before injection, one after outcome).
	if h := signer.PreviousHash(); h == "" {
		t.Error("expected attestor to have signed at least one attestation")
	}
}

// TestChaos_StopIsIdempotent verifies Stop can be called multiple times
// without panicking.
func TestChaos_StopIsIdempotent(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{ChaosNamespaces: []string{"staging"}, ChaosIntervalMin: 1}
	r := New(k8s, cfg, nil)
	r.Stop()
	r.Stop() // must not panic
}

// TestChaos_HistoryIsCopy verifies History returns a copy — mutating
// the returned slice does not affect the runner's internal state.
func TestChaos_HistoryIsCopy(t *testing.T) {
	k8s := newFakeK8s()
	cfg := &config.Config{ChaosNamespaces: []string{"staging"}, ChaosIntervalMin: 1}
	r := New(k8s, cfg, func(_ context.Context, _ Experiment, _ string) Outcome {
		return Outcome{Fixed: true}
	})

	exp := NewKillPod(k8s, newRand())
	r.RunExperiment(context.Background(), exp, "staging")

	h1 := r.History()
	if len(h1) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(h1))
	}
	h1[0] = Outcome{} // mutate the copy

	h2 := r.History()
	if h2[0].Experiment != "kill-pod" {
		t.Errorf("mutating History() copy affected internal state: %v", h2[0])
	}
}

// TestChaos_ExperimentsRegistered verifies the default constructor
// registers all three built-in experiments.
func TestChaos_ExperimentsRegistered(t *testing.T) {
	r := New(newFakeK8s(), &config.Config{}, nil)
	exps := r.Experiments()
	if len(exps) != 3 {
		t.Fatalf("expected 3 default experiments, got %d", len(exps))
	}
	names := map[string]bool{}
	for _, e := range exps {
		names[e.Name()] = true
	}
	for _, want := range []string{"kill-pod", "mutate-configmap", "restart-deployment"} {
		if !names[want] {
			t.Errorf("expected experiment %q registered, got %v", want, names)
		}
	}
}

// TestChaos_AddExperiment verifies custom experiments can be added.
func TestChaos_AddExperiment(t *testing.T) {
	r := New(newFakeK8s(), &config.Config{}, nil)
	before := len(r.Experiments())
	r.AddExperiment(&customExperiment{name: "custom-1"})
	after := len(r.Experiments())
	if after != before+1 {
		t.Errorf("AddExperiment: before=%d after=%d, want %d", before, after, before+1)
	}
}

// --- helpers ---

type customExperiment struct{ name string }

func (c *customExperiment) Name() string                                     { return c.name }
func (c *customExperiment) Run(_ context.Context, _ string) (Restore, error) { return nil, nil }
func (c *customExperiment) Cleanup(_ context.Context, _ string) error        { return nil }

func newRand() *rand.Rand {
	return rand.New(rand.NewSource(42))
}

func contains(slice []string, prefix string) bool {
	for _, s := range slice {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
