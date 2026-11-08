package watcher

import (
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// ReadyEndpoints counts a Service's ready endpoints across its slices. An
// endpoint whose readiness is unknown counts as ready, as kube-proxy treats it.
func ReadyEndpoints(slices []*discoveryv1.EndpointSlice) int {
	ready := 0
	for _, s := range slices {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				ready++
			}
		}
	}
	return ready
}

// ServiceFacts returns the facts for a Service with no ready endpoints: one
// on every workload its selector matches, or on the Service itself when none
// does. Callers pass only Services that have had ready endpoints while
// Sentinel watched, so one that is empty by design never alarms.
func ServiceFacts(svc *corev1.Service, backing []model.ObjectRef, now time.Time) []Fact {
	detail := fmt.Sprintf("Service %s has no ready endpoints", svc.Name)
	if len(backing) == 0 {
		backing = []model.ObjectRef{{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name}}
	}
	facts := make([]Fact, 0, len(backing))
	for _, ref := range backing {
		facts = append(facts, Fact{Kind: FactNoEndpoints, Workload: ref, At: now, Detail: detail})
	}
	return facts
}

// selects reports a Service whose selector routes to pods with these labels.
func selects(svc *corev1.Service, podLabels map[string]string) bool {
	if len(svc.Spec.Selector) == 0 {
		return false
	}
	return labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(podLabels))
}

// BackingWorkloads returns the Deployments and StatefulSets whose pods a
// Service's selector matches.
func BackingWorkloads(svc *corev1.Service, deps []*appsv1.Deployment, sts []*appsv1.StatefulSet) []model.ObjectRef {
	var out []model.ObjectRef
	for _, d := range deps {
		if selects(svc, d.Spec.Template.Labels) {
			out = append(out, deploymentRef(d))
		}
	}
	for _, s := range sts {
		if selects(svc, s.Spec.Template.Labels) {
			out = append(out, statefulSetRef(s))
		}
	}
	return out
}
