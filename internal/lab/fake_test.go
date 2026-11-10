package lab

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/capture"
)

// An outside API host becomes a Lab Service and stub pod; captured statuses
// shape what it answers.
func TestAnExternalAPIHostGetsAFakeServiceAndPod(t *testing.T) {
	w := shop()
	w.Calls = []capture.Call{
		{Host: "api.stripe.com", Method: "GET", Path: "/v1/balance", Port: 443, Status: 200},
		{Host: "api.stripe.com", Method: "POST", Path: "/v1/charges", Port: 443, Status: 402},
	}
	plan, err := Synthesize(w, ref("Deployment", "front"), "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	var svc *corev1.Service
	for _, s := range plan.Services {
		if s.Annotations[FakeHost] == "api.stripe.com" {
			svc = s
		}
	}
	if svc == nil || svc.Name != "fake-api-stripe-com" || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 443 {
		t.Fatalf("fake Service %+v", svc)
	}
	var pod *corev1.Pod
	for _, p := range plan.Pods {
		if p.Name == "fake-api-stripe-com" {
			pod = p
		}
	}
	if pod == nil || pod.Labels["app"] != "fake-api-stripe-com" || pod.Spec.Containers[0].Image != Image {
		t.Fatalf("fake pod %+v", pod)
	}
	var cm *corev1.ConfigMap
	for _, c := range plan.ConfigMaps {
		if c.Name == "fake-api-stripe-com" {
			cm = c
		}
	}
	if cm == nil || !strings.Contains(cm.Data["respond.sh"], "GET") || !strings.Contains(cm.Data["respond.sh"], "/v1/charges") {
		t.Fatalf("fake ConfigMap %+v", cm)
	}
	want := "Deployment/shop/front's api.stripe.com is a fake of the outside service from captured traffic; unmatched calls answer 404"
	if !slices.Contains(plan.Fidelity, want) {
		t.Fatalf("fidelity lacks %q:\n%s", want, strings.Join(plan.Fidelity, "\n"))
	}
}

// Stub status matching: method+path and route templates; unmatched is 0.
func TestStubStatusMatchesMethodAndPathOrRoute(t *testing.T) {
	calls := []capture.Call{
		{Host: "api.example", Method: "GET", Path: "/items/{id}", Route: "/items/{id}", Port: 80, Status: 200},
		{Host: "api.example", Method: "POST", Path: "/v1/charges", Port: 80, Status: 502},
		{Host: "other", Method: "GET", Path: "/v1/charges", Port: 80, Status: 200},
		{Host: "api.example", Method: "gRPC", Path: "/pkg.Svc/Method", Port: 7000, Status: 200},
	}
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/items/42", 200},
		{"GET", "/items/42/extra", 0},
		{"POST", "/v1/charges", 502},
		{"GET", "/v1/charges", 0},
		{"GET", "/missing", 0},
		{"gRPC", "/pkg.Svc/Method", 0},
	}
	for _, tc := range cases {
		if got := stubStatus("api.example", calls, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
	if fakeName("api.stripe.com") != "fake-api-stripe-com" {
		t.Fatalf("name %q", fakeName("api.stripe.com"))
	}
}

// The respond script the stub mounts answers recorded statuses over HTTP.
func TestTheFakeRespondScriptAnswersRecordedStatuses(t *testing.T) {
	routes := []fakeRoute{
		{Method: "GET", Path: "/v1/rates", Status: 200},
		{Method: "POST", Path: "/v1/charges", Status: 502},
		{Method: "GET", Path: "/items/{id}", Status: 201},
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "respond.sh")
	if err := os.WriteFile(script, []byte(respondScript(routes)), 0o755); err != nil {
		t.Fatal(err)
	}
	ask := func(method, path string) int {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Stdin = strings.NewReader(method + " " + path + " HTTP/1.1\r\nHost: x\r\n\r\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s: %v (%s)", method, path, err, out)
		}
		line := strings.SplitN(string(out), "\r\n", 2)[0]
		var proto string
		var code int
		if _, err := fmt.Sscanf(line, "%s %d", &proto, &code); err != nil {
			t.Fatalf("status line %q: %v", line, err)
		}
		return code
	}
	if got := ask(http.MethodGet, "/v1/rates"); got != 200 {
		t.Fatalf("GET /v1/rates -> %d", got)
	}
	if got := ask(http.MethodPost, "/v1/charges"); got != 502 {
		t.Fatalf("POST /v1/charges -> %d", got)
	}
	if got := ask(http.MethodGet, "/items/7"); got != 201 {
		t.Fatalf("GET /items/7 -> %d", got)
	}
	if got := ask(http.MethodGet, "/nope"); got != 404 {
		t.Fatalf("unmatched -> %d", got)
	}
}
