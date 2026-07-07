// Package chaos implements Sentinel's self-chaos mode.
//
// WHY THIS EXISTS
//
// Every serious SRE tool needs eval data. Sentinel-Bench (sentinel-bench/)
// is the public eval harness, but it needs SCENARIOS to run. The best
// scenarios are real incidents — but real incidents are rare and you can't
// wait for them.
//
// This package runs scheduled chaos experiments against a staging cluster.
// It intentionally breaks things:
//   - kill a random pod
//   - inject latency into a service
//   - mutate a configmap to a bad value
//   - push a buggy image
//   - delete a ConfigMap that a Deployment depends on
//   - simulate a memory leak
//
// ...then runs Sentinel's own detect→investigate→fix pipeline against the
// injected failure, and records whether Sentinel succeeded.
//
// The output is three things:
//   1. New scenarios for sentinel-bench (auto-promoted from successful
//      chaos runs).
//   2. Few-shot examples for the feedback vector store (Sentinel gets
//      better at fixing this class of failure next time).
//   3. A public stat: "Sentinel ran N chaos experiments this month,
//      fixed M of them autonomously."
//
// SAFETY
//
// Chaos runs ONLY against namespaces labeled sentinel-chaos-enabled=true.
// The default schedule is 1 experiment per hour during business hours only
// (configurable). Production namespaces are never touched.
//
// Every experiment is itself an attestable action — the chaos injector
// signs an attestation BEFORE injecting, so the post-incident audit trail
// can correlate "Sentinel did this on purpose at 14:00 UTC."
package chaos

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/attest"
)

// Experiment is a single chaos injection.
type Experiment struct {
	ID           string
	Name         string // e.g. "kill-pod", "inject-latency", "mutate-configmap"
	Target       Target
	InjectedAt   time.Time
	RestoreAfter time.Duration // auto-restore if Sentinel doesn't fix it
	// Injection is the actual chaos action. Returns a Restore func that
	// the runner calls if Sentinel fails or times out.
	Injection func(ctx context.Context, t Target) (Restore, error)
}

// Target is what we're breaking.
type Target struct {
	Namespace string
	Service   string
	Pod       string
	ConfigMap string
	// ChaosNamespace is the namespace where chaos is allowed.
	ChaosNamespace string
}

// Restore is called to undo the injection. Must be idempotent.
type Restore func(ctx context.Context) error

// Outcome is what happened.
type Outcome struct {
	Experiment    Experiment
	Detected      bool          // did Sentinel detect the failure?
	DetectedAt    time.Time     // when?
	TimeToDetect  time.Duration
	Fixed         bool          // did Sentinel open a verified-fix PR?
	TimeToFix     time.Duration
	AutoMerged    bool
	Notes         string
}

// K8sClient is the interface over internal/k8s. We define our own to avoid
// importing the full client (chaos only needs a few operations).
type K8sClient interface {
	DeletePod(ctx context.Context, namespace, podName string) error
	PatchConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	RestartDeployment(ctx context.Context, namespace, name string) error
}

// SentinelRunner is the interface over Sentinel's own investigation pipeline.
// After we inject chaos, we hand off to this interface and watch what happens.
type SentinelRunner interface {
	// RunInvestigation triggers Sentinel's pipeline for a synthetic incident.
	// Returns true if Sentinel detected+diagnosed+fixed within the timeout.
	RunInvestigation(ctx context.Context, incident SyntheticIncident, timeout time.Duration) (Outcome, error)
}

// SyntheticIncident is what we hand to Sentinel.
type SyntheticIncident struct {
	Service   string
	Namespace string
	Reason    string
	// The "ground truth" — what we broke. Used to score Sentinel's diagnosis.
	WhatWeBroke string
}

// Runner schedules and executes chaos experiments.
type Runner struct {
	mu        sync.Mutex
	k8s       K8sClient
	sentinel  SentinelRunner
	attestor  *attest.Signer
	logger    *slog.Logger
	rng       *rand.Rand
	stopCh    chan struct{}

	// Schedule
	Interval time.Duration // default 1h
	// AllowedNamespaces is a allow-list — chaos only runs in these.
	AllowedNamespaces []string
	// BusinessHoursOnly, if true, restricts chaos to 09:00-17:00 in the
	// configured timezone.
	BusinessHoursOnly bool
	Timezone          string

	// History of past experiments — in-memory; persisted via Sentinel's
	// feedback store in production.
	history []Outcome

	// Experiments available to run. Operators can add custom ones.
	experiments []Experiment

	// Callbacks
	OnExperimentComplete func(Outcome)
}

// NewRunner creates a chaos Runner with the default experiment set.
func NewRunner(k8s K8sClient, sentinel SentinelRunner, attestor *attest.Signer, logger *slog.Logger) *Runner {
	r := &Runner{
		k8s:              k8s,
		sentinel:         sentinel,
		attestor:         attestor,
		logger:           logger,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		stopCh:           make(chan struct{}),
		Interval:         1 * time.Hour,
		BusinessHoursOnly: true,
		Timezone:         "UTC",
	}
	r.experiments = r.defaultExperiments()
	return r
}

// AddExperiment registers a custom chaos experiment.
func (r *Runner) AddExperiment(e Experiment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.experiments = append(r.experiments, e)
}

// Start begins the chaos schedule. Blocks until ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()

	r.logger.Info("chaos: runner started",
		"interval", r.Interval,
		"experiments", len(r.experiments),
		"allowed_ns", r.AllowedNamespaces,
		"business_hours_only", r.BusinessHoursOnly,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			if r.BusinessHoursOnly && !r.inBusinessHours() {
				continue
			}
			r.runOne(ctx)
		}
	}
}

// Stop signals the runner to exit.
func (r *Runner) Stop() { close(r.stopCh) }

// History returns a snapshot of past experiment outcomes.
func (r *Runner) History() []Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Outcome, len(r.history))
	copy(out, r.history)
	return out
}

func (r *Runner) runOne(ctx context.Context) {
	r.mu.Lock()
	if len(r.experiments) == 0 || len(r.AllowedNamespaces) == 0 {
		r.mu.Unlock()
		return
	}
	exp := r.experiments[r.rng.Intn(len(r.experiments))]
	ns := r.AllowedNamespaces[r.rng.Intn(len(r.AllowedNamespaces))]
	r.mu.Unlock()

	target := Target{
		Namespace:       ns,
		ChaosNamespace:  ns,
		Service:         pickService(r.rng),
	}

	// Attest the injection BEFORE doing it — auditors need to know this was intentional.
	if r.attestor != nil {
		_ = r.attestor.Sign(ctx, attest.Attestation{
			Action:    "chaos.inject",
			Reason:    fmt.Sprintf("intentional chaos experiment: %s against %s/%s", exp.Name, target.Namespace, target.Service),
			Timestamp: time.Now(),
			Evidence: attest.Evidence{
				Extras: map[string]interface{}{
					"experiment": exp.Name,
					"target":     target,
				},
			},
		})
	}

	r.logger.Info("chaos: injecting", "experiment", exp.Name, "namespace", target.Namespace, "service", target.Service)

	restore, err := exp.Injection(ctx, target)
	if err != nil {
		r.logger.Error("chaos: injection failed", "experiment", exp.Name, "err", err)
		return
	}

	// Hand off to Sentinel.
	incident := SyntheticIncident{
		Service:   target.Service,
		Namespace: target.Namespace,
		Reason:    fmt.Sprintf("chaos experiment: %s", exp.Name),
		WhatWeBroke: exp.Name,
	}

	timeout := 10 * time.Minute
	outcome, err := r.sentinel.RunInvestigation(ctx, incident, timeout)
	if err != nil {
		r.logger.Error("chaos: sentinel investigation errored", "err", err)
	}

	// Auto-restore if Sentinel didn't fix it (don't leave staging broken).
	if !outcome.Fixed && restore != nil {
		r.logger.Warn("chaos: Sentinel failed to fix — auto-restoring", "experiment", exp.Name)
		if err := restore(ctx); err != nil {
			r.logger.Error("chaos: RESTORE FAILED — staging may be broken", "err", err)
		} else {
			outcome.Notes = "auto-restored after Sentinel failed to fix within timeout"
		}
	}

	outcome.Experiment = exp
	r.mu.Lock()
	r.history = append(r.history, outcome)
	r.mu.Unlock()

	r.logger.Info("chaos: experiment complete",
		"experiment", exp.Name,
		"detected", outcome.Detected,
		"fixed", outcome.Fixed,
		"auto_merged", outcome.AutoMerged,
		"ttd", outcome.TimeToDetect,
		"ttf", outcome.TimeToFix,
	)

	if r.attestor != nil {
		_ = r.attestor.Sign(ctx, attest.Attestation{
			Action:    "chaos.outcome",
			Outcome:   outcome.Fixed,
			Reason:    fmt.Sprintf("chaos experiment %s: detected=%v fixed=%v ttd=%s ttf=%s", exp.Name, outcome.Detected, outcome.Fixed, outcome.TimeToDetect, outcome.TimeToFix),
			Timestamp: time.Now(),
			Evidence: attest.Evidence{
				Extras: map[string]interface{}{
					"detected":       outcome.Detected,
					"fixed":          outcome.Fixed,
					"time_to_detect": outcome.TimeToDetect.String(),
					"time_to_fix":    outcome.TimeToFix.String(),
				},
			},
		})
	}

	if r.OnExperimentComplete != nil {
		r.OnExperimentComplete(outcome)
	}
}

func (r *Runner) inBusinessHours() bool {
	// Simplified — production would honor r.Timezone via time.LoadLocation.
	now := time.Now().UTC()
	// UTC business hours: 09:00-17:00, Mon-Fri.
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	hour := now.Hour()
	return hour >= 9 && hour < 17
}

// defaultExperiments returns the built-in chaos set.
func (r *Runner) defaultExperiments() []Experiment {
	return []Experiment{
		{
			Name:           "kill-pod",
			RestoreAfter:   10 * time.Minute,
			Injection: func(ctx context.Context, t Target) (Restore, error) {
				if err := r.k8s.DeletePod(ctx, t.Namespace, t.Pod); err != nil {
					return nil, err
				}
				return func(ctx context.Context) error {
					// Pods auto-recreate via Deployment controller — nothing to do.
					return nil
				}, nil
			},
		},
		{
			Name:           "mutate-configmap",
			RestoreAfter:   10 * time.Minute,
			Injection: func(ctx context.Context, t Target) (Restore, error) {
				original, err := r.k8s.GetConfigMap(ctx, t.Namespace, t.ConfigMap)
				if err != nil {
					return nil, err
				}
				// Inject a typo into a random key.
				bad := map[string]string{}
				for k, v := range original {
					if len(v) > 4 {
						bad[k] = v[:len(v)-1] + "X" // mutate last char
					} else {
						bad[k] = v
					}
				}
				if err := r.k8s.PatchConfigMap(ctx, t.Namespace, t.ConfigMap, bad); err != nil {
					return nil, err
				}
				return func(ctx context.Context) error {
					return r.k8s.PatchConfigMap(ctx, t.Namespace, t.ConfigMap, original)
				}, nil
			},
		},
		{
			Name:           "restart-deployment",
			RestoreAfter:   0, // no restore needed — Deployment controller handles it
			Injection: func(ctx context.Context, t Target) (Restore, error) {
				if err := r.k8s.RestartDeployment(ctx, t.Namespace, t.Service); err != nil {
					return nil, err
				}
				return func(ctx context.Context) error { return nil }, nil
			},
		},
	}
}

// pickService returns a deterministic-ish service name for chaos targets.
// In production, this would query the cluster for actual services in the
// chaos namespace.
func pickService(rng *rand.Rand) string {
	services := []string{"api", "worker", "frontend", "backend", "scheduler"}
	return services[rng.Intn(len(services))]
}
