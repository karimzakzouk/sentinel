// Package verify is the controller's side of the Lab: it asks the Lab
// whether a proposed rollback fixes an incident, as an Experiment in a free
// namespace of the Lab's pool, and passes the verdict back to the Brain.
package verify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/model"
)

// ExperimentsResource is the Experiment custom resource.
var ExperimentsResource = v1alpha1.GroupVersion.WithResource("experiments")

// Verdict is how the Lab judged an incident's proposed fix.
type Verdict struct {
	IncidentID model.ID
	Experiment string
	Phase      v1alpha1.ExperimentPhase
	Message    string
	Runs       int32
	Fidelity   []string
}

// Lab creates Experiments and follows them.
type Lab struct {
	Dynamic dynamic.Interface
	// Pool lists the Lab's namespaces.
	Pool []string
	// Watched are the namespaces the Lab may copy from.
	Watched   []string
	Log       *slog.Logger
	OnVerdict func(context.Context, Verdict)
	// Replay, when traffic capture is on, returns the reads production
	// answered that each run sends its copy.
	Replay func(model.ObjectRef) []v1alpha1.ReplayRequest

	mu        sync.Mutex
	stores    map[string]cache.Store
	delivered map[string]bool
	synced    bool
}

// Start runs informers on the pool's Experiments. It returns once their
// caches have synced.
func (l *Lab) Start(ctx context.Context) error {
	l.stores, l.delivered = map[string]cache.Store{}, map[string]bool{}
	var synced []cache.InformerSynced
	for _, ns := range l.Pool {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(l.Dynamic, 0, ns, nil)
		inf := f.ForResource(ExperimentsResource).Informer()
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { l.observe(ctx, obj) },
			UpdateFunc: func(_, obj any) { l.observe(ctx, obj) },
		}); err != nil {
			return err
		}
		l.stores[ns] = inf.GetStore()
		synced = append(synced, inf.HasSynced)
		f.Start(ctx.Done())
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), synced...) {
		return fmt.Errorf("experiment caches did not sync in 30s")
	}
	l.mu.Lock()
	l.synced = true
	l.mu.Unlock()
	return nil
}

// Verify asks the Lab whether the revision before revision, the failing
// one, fixes the incident on target, and reports whether it asked; it does
// not when every Lab namespace is busy, so the Brain asks again. Asking
// twice for one incident is asking once.
func (l *Lab) Verify(ctx context.Context, incident model.ID, target model.ObjectRef, revision string) (string, bool, error) {
	return l.ask(ctx, ExperimentName(incident), incident, target, revision, "")
}

// VerifyImage asks the Lab whether image, a fix to the failing revision's
// code that its source's pipeline built, fixes the incident: the failing
// revision raced with that image in place of its own.
func (l *Lab) VerifyImage(ctx context.Context, incident model.ID, target model.ObjectRef, revision, image string) (string, bool, error) {
	return l.ask(ctx, CodeExperimentName(incident), incident, target, revision, image)
}

func (l *Lab) ask(ctx context.Context, name string, incident model.ID, target model.ObjectRef, revision, image string) (string, bool, error) {
	if !slices.Contains(l.Watched, target.Namespace) {
		return "", false, nil
	}
	ns, busy := l.place(name)
	switch {
	case ns != "" && !busy:
		return ns + "/" + name, true, nil
	case ns == "":
		return "", false, nil
	}
	exp := &v1alpha1.Experiment{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Experiment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.ExperimentSpec{IncidentID: string(incident), Purpose: "verify-fix", LabNamespace: ns, Revision: revision, Candidate: image,
			Workload: &v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: target.Kind, Namespace: target.Namespace, Name: target.Name}},
	}
	if l.Replay != nil {
		exp.Spec.Replay = l.Replay(target)
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(exp)
	if err != nil {
		return "", false, err
	}
	_, err = l.Dynamic.Resource(ExperimentsResource).Namespace(ns).Create(ctx, &unstructured.Unstructured{Object: obj},
		metav1.CreateOptions{FieldManager: "sentinel"})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", false, err
	}
	what := "a rollback"
	if image != "" {
		what = "a fix to the code, built as " + image
	}
	l.Log.Info("asked the Lab to verify "+what, "incident", incident, "target", target.String(), "revision", revision, "lab", ns)
	return ns + "/" + name, true, nil
}

// place finds where the incident's Experiment is, with busy false, or a
// Lab namespace where none runs, with busy true; "" when every namespace
// is busy.
func (l *Lab) place(name string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	free := ""
	for _, ns := range l.Pool {
		store := l.stores[ns]
		if store == nil {
			continue
		}
		running := false
		for _, obj := range store.List() {
			u := obj.(*unstructured.Unstructured)
			if u.GetName() == name {
				return ns, false
			}
			phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
			running = running || !done(v1alpha1.ExperimentPhase(phase))
		}
		if !running && free == "" {
			free = ns
		}
	}
	return free, true
}

func done(p v1alpha1.ExperimentPhase) bool {
	return p == "Passed" || p == "Failed" || p == "NotSure" || p == "Aborted"
}

// observe passes on an Experiment's verdict once it is in.
func (l *Lab) observe(ctx context.Context, obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || l.OnVerdict == nil {
		return
	}
	var exp v1alpha1.Experiment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &exp); err != nil || !done(exp.Status.Phase) {
		return
	}
	key := exp.Namespace + "/" + exp.Name
	l.mu.Lock()
	seen := l.delivered[key] || !l.synced
	l.delivered[key] = true
	l.mu.Unlock()
	if seen {
		return
	}
	l.OnVerdict(ctx, Verdict{IncidentID: model.ID(exp.Spec.IncidentID), Experiment: key, Phase: exp.Status.Phase,
		Message: exp.Status.Message, Runs: exp.Status.Runs, Fidelity: exp.Status.Fidelity})
}

// ExperimentName is the same for every Experiment of one incident.
func ExperimentName(incident model.ID) string {
	id := strings.ReplaceAll(string(incident), "-", "")
	return "sentinel-" + id[max(0, len(id)-12):] + "-verify"
}

// CodeExperimentName is the one Experiment per incident that races a fix
// to the failing image's code.
func CodeExperimentName(incident model.ID) string {
	return strings.TrimSuffix(ExperimentName(incident), "-verify") + "-code"
}
