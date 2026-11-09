package topology

import (
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func deployment(name string) model.ObjectRef {
	return model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: name}
}

func template(env map[string]string, args ...string) corev1.PodTemplateSpec {
	c := corev1.Container{Name: "server", Args: args}
	for k, v := range env {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, Value: v})
	}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
}

var services = map[string]ServiceTarget{
	"redis-cart":            {Name: "redis-cart", Backing: []model.ObjectRef{deployment("redis-cart")}},
	"cartservice":           {Name: "cartservice", Backing: []model.ObjectRef{deployment("cartservice")}},
	"productcatalogservice": {Name: "productcatalogservice", Backing: []model.ObjectRef{deployment("productcatalogservice")}},
	"frontend":              {Name: "frontend", Backing: []model.ObjectRef{deployment("frontend")}},
	"orphan":                {Name: "orphan"},
}

func targets(edges []Edge) string {
	var out []string
	for _, e := range edges {
		out = append(out, e.To.Name+"/"+e.Service)
	}
	return fmt.Sprint(out)
}

func TestConfigEdgesFindServicesInEveryForm(t *testing.T) {
	for value, want := range map[string]string{
		"redis-cart:6379":                            "[redis-cart/redis-cart]",
		"redis://user:pw@redis-cart:6379/0":          "[redis-cart/redis-cart]",
		"http://cartservice:7070/v1":                 "[cartservice/cartservice]",
		"cartservice.shop.svc.cluster.local:7070":    "[cartservice/cartservice]",
		"cartservice.shop:7070":                      "[cartservice/cartservice]",
		"cartservice.other-namespace:7070":           "[]",
		"redis-cart:6379,productcatalogservice:3550": "[redis-cart/redis-cart productcatalogservice/productcatalogservice]",
		"orphan:80":          "[orphan/orphan]",
		"api.github.com:443": "[]",
		"true":               "[]",
	} {
		edges := ConfigEdges(deployment("checkout"), template(map[string]string{"ADDR": value}), services)
		if got := targets(edges); got != want {
			t.Errorf("%q: edges to %s, want %s", value, got, want)
		}
	}
	args := ConfigEdges(deployment("checkout"), template(nil, "--cart=cartservice:7070", "frontend:80"), services)
	if got := targets(args); got != "[cartservice/cartservice frontend/frontend]" {
		t.Errorf("edges from arguments: %s", got)
	}
}

func TestAWorkloadDoesNotDependOnItself(t *testing.T) {
	if edges := ConfigEdges(deployment("frontend"), template(map[string]string{"SELF": "frontend:80"}), services); len(edges) != 0 {
		t.Fatalf("edges = %+v", edges)
	}
}

// Online Boutique's shape: the frontend calls the cart service, which calls Redis.
func TestDependentsAndDependencies(t *testing.T) {
	var edges []Edge
	edges = append(edges, ConfigEdges(deployment("loadgenerator"), template(map[string]string{"FRONTEND_ADDR": "frontend:80"}), services)...)
	edges = append(edges, ConfigEdges(deployment("frontend"), template(map[string]string{
		"CART_SERVICE_ADDR": "cartservice:7070", "PRODUCT_CATALOG_SERVICE_ADDR": "productcatalogservice:3550"}), services)...)
	edges = append(edges, ConfigEdges(deployment("cartservice"), template(map[string]string{"REDIS_ADDR": "redis-cart:6379"}), services)...)
	m := NewMap(edges)
	names := func(refs []model.ObjectRef) string {
		var out []string
		for _, r := range refs {
			out = append(out, r.Name)
		}
		return fmt.Sprint(out)
	}
	if got := names(m.Dependents(deployment("redis-cart"))); got != "[cartservice frontend loadgenerator]" {
		t.Errorf("dependents of redis-cart = %s", got)
	}
	if got := names(m.Dependencies(deployment("frontend"))); got != "[cartservice productcatalogservice redis-cart]" {
		t.Errorf("dependencies of frontend = %s", got)
	}
}

func TestExternalHostsAreTheAddressesOutsideTheNamespace(t *testing.T) {
	tpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Env: []corev1.EnvVar{
			{Name: "BACKEND", Value: "backend:8080"},
			{Name: "BACKEND_FQDN", Value: "http://backend.shop.svc.cluster.local:8080/api"},
			{Name: "PAYMENTS", Value: "https://api.stripe.com/v1"},
			{Name: "DB", Value: "postgres://app:secret@db.other.svc:5432/app"},
			{Name: "CACHE", Value: "redis-cart:6379"},
			{Name: "PEERS", Value: "10.0.0.7:9000,10.0.0.8:9000"},
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "VERSION", Value: "1.2.3"},
			{Name: "CONFIG", Value: "/etc/app/config.yaml"},
			{Name: "TIMEOUT", Value: "host:abc"},
		},
		Args: []string{"--upstream=metrics.example.com:443"},
	}}}}
	got := ExternalHosts(tpl, "shop", map[string]bool{"backend": true})
	want := []string{"10.0.0.7", "10.0.0.8", "api.stripe.com", "db.other.svc", "metrics.example.com", "redis-cart"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
