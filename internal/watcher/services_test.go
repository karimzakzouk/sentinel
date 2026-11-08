package watcher

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func TestReadyEndpoints(t *testing.T) {
	slices := []*discoveryv1.EndpointSlice{
		{Endpoints: []discoveryv1.Endpoint{
			{Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
			{Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(false)}},
		}},
		{Endpoints: []discoveryv1.Endpoint{{}}}, // readiness unknown counts as ready
		{},                                      // a Service scaled to zero keeps an empty slice
	}
	if n := ReadyEndpoints(slices); n != 2 {
		t.Fatalf("ready = %d, want 2", n)
	}
}

func TestANoEndpointsFactLandsOnTheWorkloadsBehindTheService(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "redis-cart", Namespace: "shop"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "redis-cart"}}}
	template := func(app string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app, "tier": "data"}}}
	}
	deps := []*appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "redis-cart", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: template("redis-cart")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cartservice", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: template("cartservice")}},
	}
	backing := BackingWorkloads(svc, deps, nil)
	redis := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "redis-cart"}
	if len(backing) != 1 || backing[0] != redis {
		t.Fatalf("backing = %+v", backing)
	}
	facts := ServiceFacts(svc, backing, t0)
	if len(facts) != 1 || facts[0].Kind != FactNoEndpoints || facts[0].Workload != redis {
		t.Fatalf("facts = %+v", facts)
	}
	// With nothing behind it, the Service itself carries the fact.
	orphan := ServiceFacts(svc, nil, t0)
	if len(orphan) != 1 || orphan[0].Workload.Kind != "Service" || orphan[0].Workload.Name != "redis-cart" {
		t.Fatalf("orphan facts = %+v", orphan)
	}
}

func TestAServiceWithoutASelectorBacksNothing(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "shop"}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}}}}}
	if b := BackingWorkloads(svc, []*appsv1.Deployment{dep}, nil); len(b) != 0 {
		t.Fatalf("backing = %+v", b)
	}
}
