// Package fixes is the controller's side of the Fixer: it asks for the
// change at the source that makes a rollback hold, as a Fix in the Fixer's
// namespace, and passes each step the Fixer reports back to the Brain.
package fixes

import (
	"context"
	"fmt"
	"log/slog"
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
	"github.com/karimzakzouk/sentinel/internal/brain"
	"github.com/karimzakzouk/sentinel/internal/model"
)

// FixesResource is the Fix custom resource.
var FixesResource = v1alpha1.GroupVersion.WithResource("fixes")

// Update is a step a fix reached.
type Update struct {
	IncidentID model.ID
	Name       string
	Status     v1alpha1.FixStatus
}

// Fixes creates Fixes and follows them.
type Fixes struct {
	Dynamic dynamic.Interface
	// Namespace is the Fixer's, where Fixes live.
	Namespace string
	Log       *slog.Logger
	OnUpdate  func(context.Context, Update)

	mu        sync.Mutex
	delivered map[string]string
}

// Start runs an informer on the Fixer's Fixes and returns once its cache
// has synced. Every phase is passed on once per run of the controller,
// those reached while it was down included; the Brain ignores repeats.
func (f *Fixes) Start(ctx context.Context) error {
	f.delivered = map[string]string{}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(f.Dynamic, 0, f.Namespace, nil)
	inf := factory.ForResource(FixesResource).Informer()
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { f.observe(ctx, obj) },
		UpdateFunc: func(_, obj any) { f.observe(ctx, obj) },
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), inf.HasSynced) {
		return fmt.Errorf("fixes in %s did not sync in 30s", f.Namespace)
	}
	return nil
}

// Fix asks the Fixer to make the rollback of a workload hold at its
// source. Asking twice for one incident is asking once. It returns the
// Fix's name.
func (f *Fixes) Fix(ctx context.Context, r brain.FixRequest) (string, error) {
	incident, target, fromRevision := r.Incident, r.Target, r.FromRevision
	name := Name(incident)
	fix := &v1alpha1.Fix{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Fix"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.Namespace},
		Spec: v1alpha1.FixSpec{IncidentID: string(incident), FromRevision: fromRevision, FaultClass: r.FaultClass, MemoryLimits: r.MemoryLimits, Merge: r.Merge,
			Workload: v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: target.Kind, Namespace: target.Namespace, Name: target.Name}},
	}
	if c := r.Crash; c != nil {
		fix.Spec.Crash = &v1alpha1.Crash{Language: c.Language, Frames: c.Frames[:min(len(c.Frames), 20)]}
		for _, f := range c.Stack[:min(len(c.Stack), 20)] {
			fix.Spec.Crash.Stack = append(fix.Spec.Crash.Stack, v1alpha1.CrashFrame{Function: f.Function, File: f.File, Line: int32(min(f.Line, 1<<30))}) //nolint:gosec // bounded just above
		}
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fix)
	if err != nil {
		return "", err
	}
	_, err = f.Dynamic.Resource(FixesResource).Namespace(f.Namespace).Create(ctx, &unstructured.Unstructured{Object: obj},
		metav1.CreateOptions{FieldManager: "sentinel"})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	f.Log.Info("asked the Fixer for a fix at the source", "incident", incident, "target", target.String(), "from", fromRevision)
	return f.Namespace + "/" + name, nil
}

func (f *Fixes) observe(ctx context.Context, obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || f.OnUpdate == nil {
		return
	}
	var fix v1alpha1.Fix
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &fix); err != nil || fix.Status.Phase == "" {
		return
	}
	key := fix.Namespace + "/" + fix.Name
	// A step is the phase, and how the fix to the code stands.
	step := string(fix.Status.Phase)
	if c := fix.Status.Code; c != nil {
		step += "|" + c.PullRequest + "|" + c.Image + "|" + c.Verdict + "|" + c.Message
	}
	f.mu.Lock()
	seen := f.delivered[key] == step
	f.delivered[key] = step
	f.mu.Unlock()
	if seen {
		return
	}
	f.OnUpdate(ctx, Update{IncidentID: model.ID(fix.Spec.IncidentID), Name: key, Status: fix.Status})
}

// Name is the same for every Fix of one incident.
func Name(incident model.ID) string {
	id := strings.ReplaceAll(string(incident), "-", "")
	return "sentinel-" + id[max(0, len(id)-12):] + "-fix"
}
