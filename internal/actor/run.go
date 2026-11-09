package actor

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// Run watches action requests in the actor's namespaces and runs each new
// one, one at a time, until ctx ends. A request is marked Executing before it
// runs, so a restart never runs it twice: one found Executing is failed, and
// its precondition would fail anyway, as the workload has moved on.
func (a *Actor) Run(ctx context.Context) error {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	stores := map[string]cache.Store{}
	var synced []cache.InformerSynced
	for _, ns := range a.Namespaces {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(a.Dynamic, 0, ns, nil)
		inf := f.ForResource(RequestsResource).Informer()
		enqueue := func(obj any) {
			if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
				queue.Add(key)
			}
		}
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    enqueue,
			UpdateFunc: func(_, obj any) { enqueue(obj) },
		}); err != nil {
			return err
		}
		stores[ns] = inf.GetStore()
		synced = append(synced, inf.HasSynced)
		f.Start(ctx.Done())
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return fmt.Errorf("action request caches did not sync")
	}
	a.Log.Info("actor started", "namespaces", a.Namespaces, "perHour", a.PerHour)
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for {
		key, quit := queue.Get()
		if quit {
			return nil
		}
		retry, err := a.process(ctx, stores, key)
		switch {
		case err != nil:
			a.Log.Error("could not process an action request", "request", key, "error", err)
			queue.AddRateLimited(key)
		case retry > 0:
			queue.Forget(key)
			queue.AddAfter(key, retry)
		default:
			queue.Forget(key)
		}
		queue.Done(key)
	}
}

// process handles one request and returns how long until it should be
// looked at again, when it waits for a person or a veto window.
func (a *Actor) process(ctx context.Context, stores map[string]cache.Store, key string) (time.Duration, error) {
	ns, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return 0, nil
	}
	obj, exists, err := stores[ns].GetByKey(key)
	if err != nil || !exists {
		return 0, err
	}
	u := obj.(*unstructured.Unstructured).DeepCopy()
	var req v1alpha1.ActionRequest
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &req); err != nil {
		return 0, err
	}
	a.mu.Lock()
	handled := a.handled[req.UID]
	a.mu.Unlock()
	switch {
	case req.Status.Phase.Done() || handled:
		// The cache can still hold the Executing status this process wrote.
		return 0, nil
	case req.Status.Phase == v1alpha1.ActionExecuting:
		return 0, a.setStatus(ctx, &req, v1alpha1.ActionFailed, "the actor stopped while it ran; check the workload before asking again")
	}
	ok, phase, msg, retry, policies := a.admit(ctx, &req)
	if !ok {
		if phase != v1alpha1.ActionPending {
			a.Log.Info("action request done", "request", key, "action", req.Spec.Action, "target", req.Spec.Target.Name,
				"incident", req.Spec.IncidentID, "phase", phase, "message", msg)
			return 0, a.setStatus(ctx, &req, phase, msg)
		}
		if req.Status.Phase != phase || req.Status.Message != msg {
			a.Log.Info("action request waits", "request", key, "action", req.Spec.Action, "target", req.Spec.Target.Name, "message", msg)
			if err := a.setStatus(ctx, &req, phase, msg); err != nil {
				return 0, err
			}
		}
		return retry, nil
	}
	if err := a.setStatus(ctx, &req, v1alpha1.ActionExecuting, "running"); err != nil {
		return 0, err
	}
	a.mu.Lock()
	if a.handled == nil {
		a.handled = map[types.UID]bool{}
	}
	a.handled[req.UID] = true
	a.mu.Unlock()
	phase, msg = a.execute(ctx, &req, policies)
	a.Log.Info("action request done", "request", key, "action", req.Spec.Action, "target", req.Spec.Target.Name,
		"incident", req.Spec.IncidentID, "phase", phase, "approvedBy", req.Status.ApprovedBy, "message", msg)
	return 0, a.setStatus(ctx, &req, phase, msg)
}

// setStatus writes the request's status and keeps req current.
func (a *Actor) setStatus(ctx context.Context, req *v1alpha1.ActionRequest, phase v1alpha1.ActionPhase, msg string) error {
	req.Status.Phase, req.Status.Message, req.Status.ObservedGeneration = phase, msg, req.Generation
	status := metav1.ConditionFalse
	if phase == v1alpha1.ActionSucceeded || phase == v1alpha1.ActionDryRunPassed {
		status = metav1.ConditionTrue
	}
	if phase.Done() {
		setCondition(&req.Status.Conditions, metav1.Condition{Type: "Done", Status: status, Reason: string(phase),
			Message: msg, ObservedGeneration: req.Generation, LastTransitionTime: metav1.NewTime(a.now())})
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(req)
	if err != nil {
		return err
	}
	updated, err := a.Dynamic.Resource(RequestsResource).Namespace(req.Namespace).UpdateStatus(ctx,
		&unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{FieldManager: FieldManager})
	if err != nil {
		return err
	}
	req.ResourceVersion = updated.GetResourceVersion()
	return nil
}

func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	for i := range *conds {
		if (*conds)[i].Type == c.Type {
			(*conds)[i] = c
			return
		}
	}
	*conds = append(*conds, c)
}
