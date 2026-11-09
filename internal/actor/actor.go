// Package actor runs the action requests the controller makes. It checks
// each request again, independently of the controller (the target, the
// namespace's policy, the budget and the kill switch), computes what to write
// from the cluster rather than from the request, and writes it as the actor's
// own identity, which the admission fence bounds.
package actor

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/policy"
)

var (
	// RequestsResource and PoliciesResource are the custom resources the
	// actor reads.
	RequestsResource = v1alpha1.GroupVersion.WithResource("actionrequests")
	PoliciesResource = v1alpha1.GroupVersion.WithResource("sentinelpolicies")
)

// Action types the actor has contracts for, and their parameters.
const (
	// ActionRolloutUndo restores the revision before the one named by
	// ParamFromTemplateHash, which must still be the Deployment's newest.
	ActionRolloutUndo = "rollout.undo"
	// ActionScale sets ParamReplicas on a workload whose replicas are still
	// ParamFromReplicas.
	ActionScale = "scale"

	ParamFromTemplateHash = "fromTemplateHash"
	ParamReplicas         = "replicas"
	ParamFromReplicas     = "fromReplicas"
)

// FieldManager names the actor's writes in managedFields.
const FieldManager = "sentinel-actor"

const revisionAnnotation = "deployment.kubernetes.io/revision"

// Actor runs action requests.
type Actor struct {
	Client  kubernetes.Interface
	Dynamic dynamic.Interface
	// Namespaces are those Sentinel may act in.
	Namespaces []string
	// Disabled is the kill switch: while it reports true, every request is
	// rejected.
	Disabled func() bool
	// PerHour bounds the actions run per namespace per hour.
	PerHour int
	Log     *slog.Logger
	Now     func() time.Time

	mu     sync.Mutex
	recent map[string][]time.Time
	// handled holds the requests this process has started.
	handled map[types.UID]bool
}

func (a *Actor) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}

// Handle runs one request and returns its final phase and why.
func (a *Actor) Handle(ctx context.Context, req *v1alpha1.ActionRequest) (v1alpha1.ActionPhase, string) {
	now := a.now()
	t := req.Spec.Target
	switch {
	case !req.Spec.ExpiresAt.IsZero() && now.After(req.Spec.ExpiresAt.Time):
		return v1alpha1.ActionExpired, "the request lapsed before it ran"
	case a.Disabled != nil && a.Disabled():
		return v1alpha1.ActionRejected, "the kill switch is on"
	case t.Namespace != req.Namespace || !slices.Contains(a.Namespaces, t.Namespace):
		return v1alpha1.ActionRejected, fmt.Sprintf("Sentinel may not act in namespace %q", t.Namespace)
	case t.APIVersion != "apps/v1" || t.Kind != "Deployment" && t.Kind != "StatefulSet":
		return v1alpha1.ActionRejected, fmt.Sprintf("no contract covers a %s/%s", t.APIVersion, t.Kind)
	}
	level, err := a.autonomy(ctx, req.Namespace, req.Spec.Action)
	if err != nil {
		return v1alpha1.ActionFailed, fmt.Sprintf("could not read the namespace's policy: %v", err)
	}
	if level != v1alpha1.AutonomyAutonomous {
		return v1alpha1.ActionRejected, fmt.Sprintf("the namespace's policy holds %s at %s", req.Spec.Action, level)
	}
	var run func(context.Context, *v1alpha1.ActionRequest, bool) (string, error)
	switch req.Spec.Action {
	case ActionRolloutUndo:
		run = a.rolloutUndo
	case ActionScale:
		run = a.scale
	default:
		return v1alpha1.ActionRejected, fmt.Sprintf("no contract for %s yet", req.Spec.Action)
	}
	// The dry run checks the contract's preconditions and the fence before
	// any budget is spent.
	if _, err := run(ctx, req, true); err != nil {
		return v1alpha1.ActionFailed, err.Error()
	}
	if req.Spec.DryRunOnly {
		return v1alpha1.ActionDryRunPassed, "the dry run passed"
	}
	if !a.spend(req.Namespace, now) {
		return v1alpha1.ActionRejected, fmt.Sprintf("the namespace's budget of %d actions an hour is spent", a.PerHour)
	}
	msg, err := run(ctx, req, false)
	if err != nil {
		return v1alpha1.ActionFailed, err.Error()
	}
	return v1alpha1.ActionSucceeded, msg
}

// spend takes one action from the namespace's hourly budget.
func (a *Actor) spend(namespace string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recent == nil {
		a.recent = map[string][]time.Time{}
	}
	var kept []time.Time
	for _, t := range a.recent[namespace] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if len(kept) >= a.PerHour {
		a.recent[namespace] = kept
		return false
	}
	a.recent[namespace] = append(kept, now)
	return true
}

// autonomy reads the namespace's policies afresh, so a policy lowered a
// moment ago holds.
func (a *Actor) autonomy(ctx context.Context, namespace, action string) (v1alpha1.AutonomyLevel, error) {
	list, err := a.Dynamic.Resource(PoliciesResource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	policies := make([]v1alpha1.SentinelPolicy, len(list.Items))
	for i, item := range list.Items {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &policies[i]); err != nil {
			return "", err
		}
	}
	return policy.Autonomy(policies, action), nil
}

func writeOptions(dryRun bool) metav1.UpdateOptions {
	o := metav1.UpdateOptions{FieldManager: FieldManager}
	if dryRun {
		o.DryRun = []string{metav1.DryRunAll}
	}
	return o
}

// rolloutUndo restores the revision before the one the request names, which
// must still be the Deployment's newest. The template comes from the
// Deployment's own ReplicaSet, never from the request.
func (a *Actor) rolloutUndo(ctx context.Context, req *v1alpha1.ActionRequest, dryRun bool) (string, error) {
	t := req.Spec.Target
	if t.Kind != "Deployment" {
		return "", fmt.Errorf("%s applies to Deployments only", ActionRolloutUndo)
	}
	dep, err := a.Client.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	rss, err := a.Client.AppsV1().ReplicaSets(t.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	tmpl, from, to, err := previousTemplate(dep, rss.Items, req.Spec.Params[ParamFromTemplateHash])
	if err != nil {
		return "", err
	}
	updated := dep.DeepCopy()
	updated.Spec.Template = *tmpl
	if _, err := a.Client.AppsV1().Deployments(t.Namespace).Update(ctx, updated, writeOptions(dryRun)); err != nil {
		return "", err
	}
	return fmt.Sprintf("rolled %s back from revision %d to revision %d", t.Name, from, to), nil
}

// previousTemplate finds the Deployment's newest revision, checks that it is
// the one the request names, and returns the template of the revision
// before it, as the Deployment controller will match it to that ReplicaSet.
func previousTemplate(dep *appsv1.Deployment, rss []appsv1.ReplicaSet, fromHash string) (*corev1.PodTemplateSpec, int, int, error) {
	if fromHash == "" {
		return nil, 0, 0, fmt.Errorf("the request names no revision to undo (%s)", ParamFromTemplateHash)
	}
	var current, previous *appsv1.ReplicaSet
	revision := func(rs *appsv1.ReplicaSet) int {
		n, _ := strconv.Atoi(rs.Annotations[revisionAnnotation])
		return n
	}
	for i := range rss {
		rs := &rss[i]
		if owner := metav1.GetControllerOf(rs); owner == nil || owner.UID != dep.UID || revision(rs) == 0 {
			continue
		}
		if current == nil || revision(rs) > revision(current) {
			current = rs
		}
	}
	if current == nil {
		return nil, 0, 0, fmt.Errorf("%s has no ReplicaSets with revisions", dep.Name)
	}
	if hash := current.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; hash != fromHash {
		return nil, 0, 0, fmt.Errorf("%s changed since the decision: its newest revision is %d (%s), not %s",
			dep.Name, revision(current), hash, fromHash)
	}
	for i := range rss {
		rs := &rss[i]
		if owner := metav1.GetControllerOf(rs); owner == nil || owner.UID != dep.UID || revision(rs) >= revision(current) || revision(rs) == 0 {
			continue
		}
		if previous == nil || revision(rs) > revision(previous) {
			previous = rs
		}
	}
	if previous == nil {
		return nil, 0, 0, fmt.Errorf("%s has no revision before %d to roll back to", dep.Name, revision(current))
	}
	tmpl := previous.Spec.Template.DeepCopy()
	delete(tmpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return tmpl, revision(current), revision(previous), nil
}

// scale sets a workload's replicas, if they are still what the request
// expected.
func (a *Actor) scale(ctx context.Context, req *v1alpha1.ActionRequest, dryRun bool) (string, error) {
	t := req.Spec.Target
	want, err := strconv.ParseInt(req.Spec.Params[ParamReplicas], 10, 32)
	if err != nil || want < 0 {
		return "", fmt.Errorf("the request's %s %q is not a replica count", ParamReplicas, req.Spec.Params[ParamReplicas])
	}
	from, err := strconv.ParseInt(req.Spec.Params[ParamFromReplicas], 10, 32)
	if err != nil {
		return "", fmt.Errorf("the request's %s %q is not a replica count", ParamFromReplicas, req.Spec.Params[ParamFromReplicas])
	}
	get, update := a.Client.AppsV1().Deployments(t.Namespace).GetScale, a.Client.AppsV1().Deployments(t.Namespace).UpdateScale
	if t.Kind == "StatefulSet" {
		get, update = a.Client.AppsV1().StatefulSets(t.Namespace).GetScale, a.Client.AppsV1().StatefulSets(t.Namespace).UpdateScale
	}
	sc, err := get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if int64(sc.Spec.Replicas) != from {
		return "", fmt.Errorf("%s changed since the decision: %d replicas, not %d", t.Name, sc.Spec.Replicas, from)
	}
	next := &autoscalingv1.Scale{ObjectMeta: sc.ObjectMeta, Spec: autoscalingv1.ScaleSpec{Replicas: int32(want)}}
	if _, err := update(ctx, t.Name, next, writeOptions(dryRun)); err != nil {
		return "", err
	}
	return fmt.Sprintf("scaled %s from %d to %d replicas", t.Name, from, want), nil
}
