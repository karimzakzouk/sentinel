// Package act is the controller's side of acting: it asks the actor for the
// actions namespaces' policies let Sentinel run on its own, as
// ActionRequests, and passes their outcomes back to the Brain. The
// controller holds no write permission on workloads.
package act

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/policy"
)

// Outcome is what became of a request: its final phase, or Pending while it
// waits for a person or a veto window.
type Outcome struct {
	IncidentID model.ID
	Request    string
	Action     string
	Phase      v1alpha1.ActionPhase
	Message    string
	ApprovedBy string
	// BudgetSpent marks a request the namespace's hourly budget turned
	// down.
	BudgetSpent bool
}

// Requests creates action requests and follows them.
type Requests struct {
	// Client reads the workloads to act on, to tell whose they are.
	Client     kubernetes.Interface
	Dynamic    dynamic.Interface
	Namespaces []string
	// TTL is how long a request may wait for the actor before it lapses.
	TTL       time.Duration
	Log       *slog.Logger
	OnOutcome func(context.Context, Outcome)

	mu        sync.Mutex
	policies  map[string]cache.Store
	delivered map[string]v1alpha1.ActionPhase
	// synced is set once the initial list is in; outcomes listed then were
	// passed on by an earlier run.
	synced bool
}

// Start runs informers on the namespaces' policies and requests. It returns
// once their caches have synced.
func (r *Requests) Start(ctx context.Context) error {
	r.policies, r.delivered = map[string]cache.Store{}, map[string]v1alpha1.ActionPhase{}
	var synced []cache.InformerSynced
	for _, ns := range r.Namespaces {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.Dynamic, 0, ns, nil)
		pol := f.ForResource(actor.PoliciesResource).Informer()
		req := f.ForResource(actor.RequestsResource).Informer()
		if _, err := req.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { r.observe(ctx, obj) },
			UpdateFunc: func(_, obj any) { r.observe(ctx, obj) },
		}); err != nil {
			return err
		}
		r.policies[ns] = pol.GetStore()
		synced = append(synced, pol.HasSynced, req.HasSynced)
		f.Start(ctx.Done())
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), synced...) {
		return fmt.Errorf("policy and action request caches did not sync in 30s")
	}
	r.mu.Lock()
	r.synced = true
	r.mu.Unlock()
	return nil
}

// Autonomy returns the level the namespace's policies allow an action.
func (r *Requests) Autonomy(namespace, action string) v1alpha1.AutonomyLevel {
	r.mu.Lock()
	store := r.policies[namespace]
	r.mu.Unlock()
	if store == nil {
		return v1alpha1.AutonomyObserve
	}
	var policies []v1alpha1.SentinelPolicy
	for _, obj := range store.List() {
		var p v1alpha1.SentinelPolicy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).Object, &p); err == nil {
			policies = append(policies, p)
		}
	}
	return policy.Autonomy(policies, action)
}

// Earned reports whether the namespace's policies make an action earn its
// level through its track record.
func (r *Requests) Earned(namespace, action string) bool {
	r.mu.Lock()
	store := r.policies[namespace]
	r.mu.Unlock()
	if store == nil {
		return false
	}
	var policies []v1alpha1.SentinelPolicy
	for _, obj := range store.List() {
		var p v1alpha1.SentinelPolicy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).Object, &p); err == nil {
			policies = append(policies, p)
		}
	}
	return policy.Earned(policies, action)
}

// Acting reports whether any watched namespace's policies let Sentinel run
// an action on its own.
func (r *Requests) Acting() bool {
	r.mu.Lock()
	namespaces := slices.Collect(maps.Keys(r.policies))
	r.mu.Unlock()
	for _, ns := range namespaces {
		for _, action := range []string{actor.ActionRolloutUndo, actor.ActionArgoCDRollback, actor.ActionFluxRollback, actor.ActionScale} {
			if level := r.Autonomy(ns, action); level == v1alpha1.AutonomyAutonomous || level == v1alpha1.AutonomyLabVerified {
				return true
			}
		}
	}
	return false
}

// RequestAction maps the Brain's actions to the actor's.
func RequestAction(brainAction string) (string, bool) {
	switch brainAction {
	case "rollout.undo":
		return actor.ActionRolloutUndo, true
	case "scale.restore":
		return actor.ActionScale, true
	}
	return "", false
}

// approvalTTL is how long a request that needs a person, or the Lab,
// waits for one.
const approvalTTL = time.Hour

// Parameters the Brain passes about an action, which the request carries
// in annotations, not in its parameters: the Lab Experiment verifying it,
// and whether its track record has earned its level, with a sentence for
// whoever must approve it when not.
const (
	ParamExperiment = "experiment"
	ParamEarned     = "earned"
	ParamTrack      = "track"
)

// Request asks the actor for an action if the target namespace's policy
// lets Sentinel run it on its own, after a veto window, or once a person
// approves; it reports whether it asked. Asking twice for one incident and
// action is asking once.
func (r *Requests) Request(ctx context.Context, a model.Action, reason string) (bool, error) {
	action, ok := RequestAction(a.Type)
	if !ok {
		return false, nil
	}
	if action == actor.ActionRolloutUndo && r.Client != nil && a.Target.Kind == "Deployment" {
		dep, err := r.Client.AppsV1().Deployments(a.Target.Namespace).Get(ctx, a.Target.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		action = actor.RollbackAction(dep.ObjectMeta)
	}
	ttl := r.TTL
	level := r.Autonomy(a.Target.Namespace, action)
	switch level {
	case v1alpha1.AutonomyAutonomous:
	case v1alpha1.AutonomyActUnlessVetoed:
		ttl += actor.VetoWindow
	case v1alpha1.AutonomyApprove, v1alpha1.AutonomyLabVerified:
		ttl = approvalTTL
	default:
		return false, nil
	}
	params := maps.Clone(a.Params)
	annotations := map[string]string{}
	if exp := params[ParamExperiment]; exp != "" {
		annotations[actor.AnnotationExperiment] = exp
	}
	if r.Earned(a.Target.Namespace, action) && policy.Above(level, v1alpha1.AutonomyApprove) && params[ParamEarned] != "true" {
		annotations[actor.AnnotationNeedsApproval] = cmp.Or(params[ParamTrack], "its track record has not earned "+string(level)+" yet")
		ttl = approvalTTL
	}
	for _, p := range []string{ParamExperiment, ParamEarned, ParamTrack} {
		delete(params, p)
	}
	req := &v1alpha1.ActionRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ActionRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: requestName(a.IncidentID, action), Namespace: a.Target.Namespace, Annotations: annotations},
		Spec: v1alpha1.ActionRequestSpec{IncidentID: string(a.IncidentID), Action: action, Params: params, Reason: clip(reason, 2048),
			ExpiresAt: metav1.NewTime(time.Now().Add(ttl)),
			Target:    v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: a.Target.Kind, Namespace: a.Target.Namespace, Name: a.Target.Name}},
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(req)
	if err != nil {
		return false, err
	}
	_, err = r.Dynamic.Resource(actor.RequestsResource).Namespace(req.Namespace).Create(ctx, &unstructured.Unstructured{Object: obj},
		metav1.CreateOptions{FieldManager: "sentinel"})
	if apierrors.IsAlreadyExists(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	r.Log.Info("asked the actor for an action", "incident", a.IncidentID, "action", action, "target", a.Target.String())
	return true, nil
}

// observe passes on a request's outcome once it is known.
func (r *Requests) observe(ctx context.Context, obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || r.OnOutcome == nil {
		return
	}
	var req v1alpha1.ActionRequest
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &req); err != nil ||
		!req.Status.Phase.Done() && req.Status.Phase != v1alpha1.ActionPending {
		return
	}
	key := req.Namespace + "/" + req.Name
	r.mu.Lock()
	seen := r.delivered[key] == req.Status.Phase || !r.synced
	r.delivered[key] = req.Status.Phase
	r.mu.Unlock()
	if seen {
		return
	}
	r.OnOutcome(ctx, Outcome{IncidentID: model.ID(req.Spec.IncidentID), Request: key, Action: req.Spec.Action,
		Phase: req.Status.Phase, Message: req.Status.Message, ApprovedBy: req.Status.ApprovedBy,
		BudgetSpent: req.Status.Phase == v1alpha1.ActionRejected && policy.BudgetSpent(req.Status.Message)})
}

// requestName is the same for every request of one action on one incident.
func requestName(incident model.ID, action string) string {
	id := strings.ReplaceAll(string(incident), "-", "")
	return fmt.Sprintf("sentinel-%s-%s", id[max(0, len(id)-12):], strings.ReplaceAll(action, ".", "-"))
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
