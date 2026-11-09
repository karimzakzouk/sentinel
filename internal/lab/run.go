package lab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// ExperimentsResource is the Experiment custom resource.
var ExperimentsResource = v1alpha1.GroupVersion.WithResource("experiments")

// Phases of an Experiment.
const (
	PhasePending v1alpha1.ExperimentPhase = "Pending"
	PhaseSealing v1alpha1.ExperimentPhase = "Sealing"
	PhaseRunning v1alpha1.ExperimentPhase = "Running"
	PhasePassed  v1alpha1.ExperimentPhase = "Passed"
	PhaseFailed  v1alpha1.ExperimentPhase = "Failed"
	PhaseNotSure v1alpha1.ExperimentPhase = "NotSure"
	PhaseAborted v1alpha1.ExperimentPhase = "Aborted"
)

func done(p v1alpha1.ExperimentPhase) bool {
	return p == PhasePassed || p == PhaseFailed || p == PhaseNotSure || p == PhaseAborted
}

// Lab runs Experiments in its pool of namespaces, one at a time.
type Lab struct {
	Prover  *Prover
	Dynamic dynamic.Interface
	// Pool lists the Lab namespaces created at install.
	Pool []string
	// Enforced proves that a namespace's NetworkPolicy is enforced, with
	// live traffic; without it, the Lab never runs.
	Enforced func(ctx context.Context, ns string) (bool, string)
	Log      *slog.Logger

	mu      sync.Mutex
	proven  map[string]time.Time
	handled map[string]bool
}

// provenFor is how long a namespace's proof of enforcement is trusted.
const provenFor = 10 * time.Minute

// Handle runs one Experiment and returns its verdict and why.
func (l *Lab) Handle(ctx context.Context, exp *v1alpha1.Experiment) (v1alpha1.ExperimentPhase, string) {
	ns := exp.Spec.LabNamespace
	switch {
	case !slices.Contains(l.Pool, ns):
		return PhaseFailed, fmt.Sprintf("%s is not one of the Lab's namespaces", ns)
	case exp.Namespace != ns:
		return PhaseFailed, "an Experiment must live in the Lab namespace it uses"
	}
	if ok, why := l.enforced(ctx, ns); !ok {
		return PhaseFailed, "the Lab stays off: NetworkPolicy enforcement is not proven here (" + why + ")"
	}
	if err := l.Prover.Seal(ctx, ns); err != nil {
		return PhaseFailed, fmt.Sprintf("could not seal %s: %v", ns, err)
	}
	defer func() {
		_ = l.Prover.Client.NetworkingV1().NetworkPolicies(ns).Delete(context.WithoutCancel(ctx), "lab-internal", metav1.DeleteOptions{})
	}()
	results, err := l.Prover.Prove(ctx, ns)
	if err != nil {
		return PhaseNotSure, fmt.Sprintf("could not prove the seal: %v", err)
	}
	phase, msg, sealed := sealVerdict(results)
	if !sealed || exp.Spec.Purpose == "prove" {
		return phase, msg
	}
	return PhaseNotSure, "the Lab is sealed, but reproducing incidents comes with phase 3's next slice"
}

// sealVerdict reads the matrix: the seal holds when every forbidden
// destination was refused and every control answered.
func sealVerdict(results []Result) (v1alpha1.ExperimentPhase, string, bool) {
	var inside, outside, silent []string
	refused := 0
	for _, r := range results {
		switch {
		case r.Allowed && !r.Reached:
			silent = append(silent, r.Name)
		case !r.Allowed && r.Reached && r.Outside:
			outside = append(outside, r.Name)
		case !r.Allowed && r.Reached:
			inside = append(inside, r.Name)
		case !r.Allowed:
			refused++
		}
	}
	switch {
	case len(inside) > 0:
		msg := "the seal leaks to " + strings.Join(inside, ", ")
		if len(outside) > 0 {
			msg += ", and outside the cluster to " + strings.Join(outside, ", ")
		}
		return PhaseFailed, msg, false
	case len(outside) > 0:
		return PhaseFailed, "the seal leaks outside the cluster to " + strings.Join(outside, ", ") +
			": the network plugin lets pods reach them despite the policy, so block them at the nodes before using the Lab here", false
	case len(silent) > 0:
		return PhaseNotSure, "the positive controls did not answer (" + strings.Join(silent, ", ") + "), so a refusal means nothing", false
	}
	return PhasePassed, fmt.Sprintf("sealed: %d forbidden destinations refused and the controls answered", refused), true
}

func (l *Lab) enforced(ctx context.Context, ns string) (bool, string) {
	if l.Enforced == nil {
		return false, "no enforcement check is configured"
	}
	l.mu.Lock()
	at, ok := l.proven[ns]
	l.mu.Unlock()
	if ok && time.Since(at) < provenFor {
		return true, ""
	}
	enforced, why := l.Enforced(ctx, ns)
	if enforced {
		l.mu.Lock()
		if l.proven == nil {
			l.proven = map[string]time.Time{}
		}
		l.proven[ns] = time.Now()
		l.mu.Unlock()
	}
	return enforced, why
}

// Run watches Experiments in the pool and runs each new one, until ctx ends.
func (l *Lab) Run(ctx context.Context) error {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	stores := map[string]cache.Store{}
	var synced []cache.InformerSynced
	for _, ns := range l.Pool {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(l.Dynamic, 0, ns, nil)
		inf := f.ForResource(ExperimentsResource).Informer()
		enqueue := func(obj any) {
			if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
				queue.Add(key)
			}
		}
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: enqueue, UpdateFunc: func(_, obj any) { enqueue(obj) }}); err != nil {
			return err
		}
		stores[ns] = inf.GetStore()
		synced = append(synced, inf.HasSynced)
		f.Start(ctx.Done())
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return fmt.Errorf("experiment caches did not sync")
	}
	l.Log.Info("lab started", "pool", l.Pool)
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for {
		key, quit := queue.Get()
		if quit {
			return nil
		}
		if err := l.process(ctx, stores, key); err != nil {
			l.Log.Error("could not process an experiment", "experiment", key, "error", err)
			queue.AddRateLimited(key)
		} else {
			queue.Forget(key)
		}
		queue.Done(key)
	}
}

func (l *Lab) process(ctx context.Context, stores map[string]cache.Store, key string) error {
	ns, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil || stores[ns] == nil {
		return nil
	}
	obj, exists, err := stores[ns].GetByKey(key)
	if err != nil || !exists {
		return err
	}
	var exp v1alpha1.Experiment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).Object, &exp); err != nil {
		return err
	}
	l.mu.Lock()
	handled := l.handled[string(exp.UID)]
	l.mu.Unlock()
	switch {
	case done(exp.Status.Phase) || handled:
		return nil
	case exp.Status.Phase == PhaseSealing || exp.Status.Phase == PhaseRunning:
		return l.setStatus(ctx, &exp, PhaseAborted, "the Lab stopped while the experiment ran")
	}
	if err := l.setStatus(ctx, &exp, PhaseSealing, "sealing "+exp.Spec.LabNamespace); err != nil {
		return err
	}
	l.mu.Lock()
	if l.handled == nil {
		l.handled = map[string]bool{}
	}
	l.handled[string(exp.UID)] = true
	l.mu.Unlock()
	timeout := exp.Spec.Timeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	phase, msg := l.Handle(run, &exp)
	if run.Err() != nil && ctx.Err() == nil {
		phase, msg = PhaseAborted, "the experiment ran out of time: "+msg
	}
	l.Log.Info("experiment done", "experiment", key, "purpose", exp.Spec.Purpose, "phase", phase, "message", msg)
	return l.setStatus(ctx, &exp, phase, msg)
}

func (l *Lab) setStatus(ctx context.Context, exp *v1alpha1.Experiment, phase v1alpha1.ExperimentPhase, msg string) error {
	exp.Status.Phase, exp.Status.Message, exp.Status.ObservedGeneration = phase, msg, exp.Generation
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(exp)
	if err != nil {
		return err
	}
	updated, err := l.Dynamic.Resource(ExperimentsResource).Namespace(exp.Namespace).UpdateStatus(ctx,
		&unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{FieldManager: "sentinel-lab"})
	if err != nil {
		return err
	}
	exp.ResourceVersion = updated.GetResourceVersion()
	return nil
}
