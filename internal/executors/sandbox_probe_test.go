package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ─── httpProbePaths ─────────────────────────────────────────────────────

func TestHTTPProbePaths(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:           "app",
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready"}}},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}}},
						},
					},
				},
			},
		},
	}
	got := httpProbePaths(dep)
	if len(got) != 2 || got[0] != "/ready" || got[1] != "/health" {
		t.Fatalf("httpProbePaths = %v, want [/ready /health]", got)
	}
}

func TestHTTPProbePathsDedupAndOrder(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:           "a",
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}}},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}}},
						},
						{
							Name:           "b",
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready"}}},
						},
					},
				},
			},
		},
	}
	got := httpProbePaths(dep)
	if len(got) != 2 || got[0] != "/health" || got[1] != "/ready" {
		t.Fatalf("httpProbePaths = %v, want [/health /ready] (deduped, order preserved)", got)
	}
}

func TestHTTPProbePathsNonHTTPIgnored(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:           "tcp",
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{}}},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"true"}}}},
						},
					},
				},
			},
		},
	}
	if got := httpProbePaths(dep); len(got) != 0 {
		t.Fatalf("httpProbePaths = %v, want empty for tcp/exec probes", got)
	}
}

func TestHTTPProbePathsEmptyPathDefaultsToRoot(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:           "app",
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: ""}}},
						},
					},
				},
			},
		},
	}
	got := httpProbePaths(dep)
	if len(got) != 1 || got[0] != "/" {
		t.Fatalf("httpProbePaths = %v, want [/] for empty path", got)
	}
}

func TestHTTPProbePathsNilDeployment(t *testing.T) {
	if got := httpProbePaths(nil); got != nil {
		t.Fatalf("httpProbePaths(nil) = %v, want nil", got)
	}
}

// ─── resolveProbePaths precedence ───────────────────────────────────────

func TestResolveProbePathsPrecedence(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)

	// No config, no spec → convention, any-of.
	paths, anyOf, source := s.resolveProbePaths(nil)
	if source != "convention" || !anyOf {
		t.Fatalf("no config/spec: got source=%q anyOf=%v, want convention/any-of", source, anyOf)
	}
	if len(paths) == 0 || paths[0] != "/health" {
		t.Fatalf("convention candidates = %v, want non-empty starting with /health", paths)
	}

	// Spec present → spec wins over convention, all-must-pass.
	paths, anyOf, source = s.resolveProbePaths([]string{"/health", "/ready"})
	if source != "spec" || anyOf || len(paths) != 2 {
		t.Fatalf("spec only: got source=%q anyOf=%v paths=%v, want spec/all-must-pass", source, anyOf, paths)
	}

	// Config present → config wins over spec.
	s.WithProbePaths([]string{"/healthz"})
	paths, anyOf, source = s.resolveProbePaths([]string{"/health", "/ready"})
	if source != "config" || anyOf || len(paths) != 1 || paths[0] != "/healthz" {
		t.Fatalf("config + spec: got source=%q anyOf=%v paths=%v, want config winning", source, anyOf, paths)
	}
}

func TestProbeModeStr(t *testing.T) {
	if probeModeStr(true) != "any-of" {
		t.Errorf(`probeModeStr(true) = %q, want "any-of"`, probeModeStr(true))
	}
	if probeModeStr(false) != "all-must-pass" {
		t.Errorf(`probeModeStr(false) = %q, want "all-must-pass"`, probeModeStr(false))
	}
}

// ─── buildProbeTargets ──────────────────────────────────────────────────

func TestBuildProbeTargets(t *testing.T) {
	ports := []corev1.ServicePort{
		{Port: 8080, Name: "http"},
		{Port: 9090, Name: "metrics"},
	}
	targets := buildProbeTargets("svc", "ns", ports, []string{"/health", "POST /ping"})
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets (2 ports × 2 paths), got %d", len(targets))
	}
	want0 := "http://svc.ns.svc.cluster.local:8080/health"
	if targets[0].url != want0 || targets[0].method != "GET" {
		t.Errorf("targets[0] = %+v, want url=%s method=GET", targets[0], want0)
	}
	if targets[3].method != "POST" || targets[3].path != "/ping" {
		t.Errorf("targets[3] = %+v, want POST /ping", targets[3])
	}
}

// ─── evaluateProbes (all-of / any-of semantics) ─────────────────────────

func testClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func TestEvaluateProbesAllOf(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer bad.Close()

	targets := []probeTarget{
		{method: "GET", url: ok.URL + "/health"},
		{method: "GET", url: ok.URL + "/ready"},
	}
	if !evaluateProbes(context.Background(), testClient(), targets, false) {
		t.Fatal("all-of with all healthy endpoints should pass")
	}

	targets = append(targets, probeTarget{method: "GET", url: bad.URL + "/nope"})
	if evaluateProbes(context.Background(), testClient(), targets, false) {
		t.Fatal("all-of with one 404 endpoint must fail")
	}
}

func TestEvaluateProbesAnyOf(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer bad.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ok.Close()

	// First candidates unhealthy, last one healthy → any-of passes.
	targets := []probeTarget{
		{method: "GET", url: bad.URL + "/healthz"},
		{method: "GET", url: bad.URL + "/readyz"},
		{method: "GET", url: ok.URL + "/health"},
	}
	if !evaluateProbes(context.Background(), testClient(), targets, true) {
		t.Fatal("any-of with one healthy candidate should pass")
	}

	// All unhealthy → fails.
	targets = []probeTarget{
		{method: "GET", url: bad.URL + "/healthz"},
		{method: "GET", url: bad.URL + "/ready"},
	}
	if evaluateProbes(context.Background(), testClient(), targets, true) {
		t.Fatal("any-of with no healthy candidate must fail")
	}
}

func TestEvaluateProbesFlakyEndpointDoesNotFalsePass(t *testing.T) {
	// Alternates 200/404 — never 2 consecutive successes.
	n := 0
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n%2 == 1 {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer flaky.Close()

	targets := []probeTarget{{method: "GET", url: flaky.URL + "/health"}}
	if evaluateProbes(context.Background(), testClient(), targets, true) {
		t.Fatal("flaky endpoint (no 2 consecutive 2xx) must not pass even under any-of")
	}
}

func TestEvaluateProbesConnectionRefused(t *testing.T) {
	// Nothing listens on this port — probe errors count as failures.
	targets := []probeTarget{{method: "GET", url: "http://127.0.0.1:1/health"}}
	if evaluateProbes(context.Background(), testClient(), targets, true) {
		t.Fatal("connection-refused target must fail even under any-of")
	}
}

// ─── convention set sanity ──────────────────────────────────────────────

func TestConventionProbePathsCoversCommonCases(t *testing.T) {
	// The eval fleet serves /health + /ready; kube-apiserver-style apps
	// serve /healthz + /readyz; both must be in the candidate set.
	set := map[string]bool{}
	for _, p := range conventionProbePaths {
		set[p] = true
	}
	for _, want := range []string{"/health", "/healthz", "/ready", "/readyz", "/livez", "/"} {
		if !set[want] {
			t.Errorf("convention candidate set missing %s", want)
		}
	}
}
