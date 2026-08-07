package safeactions

import (
	"context"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// makeDeployment builds a minimal deployment object for tests.
func makeDeployment(name, namespace string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img"}}},
			},
		},
	}
}

// enabledCfg returns a Config that's enabled for the given namespaces.
func enabledCfg(namespaces ...string) Config {
	return Config{
		Enabled:           true,
		AllowedNamespaces: namespaces,
		MaxActionsPerHour: 3,
		MaxScaleDeltaPct:  50,
	}
}

// TestIsAllowed verifies the namespace allowlist + enabled gate.
func TestIsAllowed(t *testing.T) {
	c := New(nil, enabledCfg("sim", "staging"))
	if !c.IsAllowed("sim") {
		t.Error("sim should be allowed")
	}
	if !c.IsAllowed("staging") {
		t.Error("staging should be allowed")
	}
	if c.IsAllowed("prod") {
		t.Error("prod should NOT be allowed")
	}

	disabled := New(nil, Config{Enabled: false, AllowedNamespaces: []string{"sim"}})
	if disabled.IsAllowed("sim") {
		t.Error("disabled controller should reject everything")
	}
}

// TestRestartDisabledRejects verifies that a disabled controller rejects
// the action without touching the API server.
func TestRestartDisabledRejects(t *testing.T) {
	c := New(fake.NewSimpleClientset(), Config{Enabled: false})
	r := c.Restart(context.Background(), "sim", "api")
	if r.Success {
		t.Error("disabled controller should not succeed")
	}
	if r.Detail != "namespace not in safe-actions allow-list" {
		t.Errorf("unexpected detail: %q", r.Detail)
	}
}

// TestRestartPatchesAnnotation verifies Restart actually patches the pod
// template annotation on the backing Deployment.
func TestRestartPatchesAnnotation(t *testing.T) {
	dep := makeDeployment("api", "sim", 3)
	c := New(fake.NewSimpleClientset(dep), enabledCfg("sim"))

	r := c.Restart(context.Background(), "sim", "api")
	if !r.Success {
		t.Fatalf("Restart failed: %s", r.Detail)
	}

	got, err := c.clientset.AppsV1().Deployments("sim").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Restart: %v", err)
	}
	ts, ok := got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]
	if !ok || ts == "" {
		t.Errorf("Restart should set restartedAt annotation, got: %v", got.Spec.Template.Annotations)
	}
}

// TestRestartNotFoundByExactName verifies the label-selector fallback.
// The deployment is named differently from the service, but carries the
// matching app=<service> label.
func TestRestartNotFoundByExactName(t *testing.T) {
	dep := makeDeployment("api-v1", "sim", 3)
	dep.Labels = map[string]string{"app": "api"}
	dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	dep.Spec.Template.Labels = map[string]string{"app": "api"}
	c := New(fake.NewSimpleClientset(dep), enabledCfg("sim"))

	r := c.Restart(context.Background(), "sim", "api")
	if !r.Success {
		t.Errorf("Restart should fall back to label selector, got: %s", r.Detail)
	}
}

// TestSafeActions_RateLimit is the v1 regression test: after MaxActionsPerHour
// successful actions, subsequent calls are rejected with "rate limit exceeded"
// and do not call into the K8s API.
func TestSafeActions_RateLimit(t *testing.T) {
	dep := makeDeployment("api", "sim", 3)
	cs := fake.NewSimpleClientset(dep)
	c := New(cs, Config{
		Enabled:           true,
		AllowedNamespaces: []string{"sim"},
		MaxActionsPerHour: 2,
	})

	for i := 1; i <= 2; i++ {
		r := c.Restart(context.Background(), "sim", "api")
		if !r.Success {
			t.Fatalf("call %d should succeed, got: %s", i, r.Detail)
		}
	}
	r := c.Restart(context.Background(), "sim", "api")
	if r.Success {
		t.Error("third call should be rate-limited, but succeeded")
	}
	if r.Detail != "rate limit exceeded" {
		t.Errorf("detail = %q, want 'rate limit exceeded'", r.Detail)
	}

	// After waiting past the 1h window, the limiter should accept again.
	c.mu.Lock()
	for i := range c.actionLog {
		c.actionLog[i] = time.Now().Add(-2 * time.Hour)
	}
	c.mu.Unlock()

	r2 := c.Restart(context.Background(), "sim", "api")
	if !r2.Success {
		t.Errorf("call after window expiry should succeed, got: %s", r2.Detail)
	}
}

// TestScaleBlastRadiusExceeded verifies that a percent change beyond
// MaxScaleDeltaPct is rejected.
func TestScaleBlastRadiusExceeded(t *testing.T) {
	dep := makeDeployment("api", "sim", 10)
	c := New(fake.NewSimpleClientset(dep), Config{
		Enabled:           true,
		AllowedNamespaces: []string{"sim"},
		MaxActionsPerHour: 10,
		MaxScaleDeltaPct:  50,
	})

	// 10 → 20 is +100%, exceeds 50%.
	r := c.Scale(context.Background(), "sim", "api", 20)
	if r.Success {
		t.Error("scale +100% should be rejected by blast-radius gate")
	}
	if r.Detail == "" {
		t.Error("rejection should carry a detail")
	}
}

// TestScaleWithinBlastRadius verifies an in-bounds scale call patches
// replicas.
func TestScaleWithinBlastRadius(t *testing.T) {
	dep := makeDeployment("api", "sim", 10)
	c := New(fake.NewSimpleClientset(dep), Config{
		Enabled:           true,
		AllowedNamespaces: []string{"sim"},
		MaxActionsPerHour: 10,
		MaxScaleDeltaPct:  50,
	})

	// 10 → 12 is +20%, within 50%.
	r := c.Scale(context.Background(), "sim", "api", 12)
	if !r.Success {
		t.Errorf("Scale should succeed, got: %s", r.Detail)
	}
	got, _ := c.clientset.AppsV1().Deployments("sim").Get(context.Background(), "api", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 12 {
		t.Errorf("replicas = %v, want 12", got.Spec.Replicas)
	}
}

// TestRollbackNoPreviousRevision verifies the error path when no prior
// ReplicaSet exists.
func TestRollbackNoPreviousRevision(t *testing.T) {
	dep := makeDeployment("api", "sim", 3)
	dep.Annotations = map[string]string{"deployment.kubernetes.io/revision": "1"}
	c := New(fake.NewSimpleClientset(dep), enabledCfg("sim"))

	r := c.Rollback(context.Background(), "sim", "api")
	if r.Success {
		t.Error("Rollback should fail when no previous revision exists")
	}
	if r.Detail != "no previous revision to rollback to" {
		t.Errorf("detail = %q, want 'no previous revision to rollback to'", r.Detail)
	}
}

// TestRollbackPatchesTemplate verifies a rollback copies the prior
// ReplicaSet's pod template onto the deployment.
func TestRollbackPatchesTemplate(t *testing.T) {
	dep := makeDeployment("api", "sim", 3)
	dep.Annotations = map[string]string{"deployment.kubernetes.io/revision": "2"}

	// Previous ReplicaSet with revision 1 and a distinct image.
	prevRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-old",
			Namespace: "sim",
			Labels:    map[string]string{"app": "api"},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "1",
			},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "old-image"}}},
			},
		},
	}
	c := New(fake.NewSimpleClientset(dep, prevRS), enabledCfg("sim"))

	r := c.Rollback(context.Background(), "sim", "api")
	if !r.Success {
		t.Fatalf("Rollback should succeed, got: %s", r.Detail)
	}

	got, _ := c.clientset.AppsV1().Deployments("sim").Get(context.Background(), "api", metav1.GetOptions{})
	if len(got.Spec.Template.Spec.Containers) == 0 || got.Spec.Template.Spec.Containers[0].Image != "old-image" {
		t.Errorf("deployment template was not patched to previous RS, got: %+v", got.Spec.Template.Spec)
	}
}

// TestParseAction verifies the parser for the action-string wire format.
func TestParseAction(t *testing.T) {
	cases := []struct {
		in        string
		wantType  ActionType
		wantReps  int32
		wantError bool
	}{
		{"restart", ActionRestart, 0, false},
		{"rollback", ActionRollback, 0, false},
		{"scale 5", ActionScale, 5, false},
		{"scale", "", 0, true},
		{"scale abc", "", 0, true},
		{"explode", "", 0, true},
		{"", "", 0, true},
		{"  RESTART  ", ActionRestart, 0, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotType, gotReps, err := ParseAction(c.in)
			if (err != nil) != c.wantError {
				t.Errorf("ParseAction(%q) err = %v, wantError = %v", c.in, err, c.wantError)
			}
			if gotType != c.wantType {
				t.Errorf("ParseAction(%q) type = %q, want %q", c.in, gotType, c.wantType)
			}
			if gotReps != c.wantReps {
				t.Errorf("ParseAction(%q) reps = %d, want %d", c.in, gotReps, c.wantReps)
			}
		})
	}
}

// TestAvailableActions verifies the help-text helper.
func TestAvailableActions(t *testing.T) {
	got := AvailableActions()
	if len(got) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(got))
	}
}

// TestRateLimitedConcurrent verifies the rate limiter is safe under
// concurrent use — the action log never exceeds MaxActionsPerHour. Without
// the mutex, 200 concurrent calls would all read len < 50 and append,
// blowing the cap to ~200.
func TestRateLimitedConcurrent(t *testing.T) {
	c := New(nil, Config{Enabled: true, MaxActionsPerHour: 50})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.rateLimited()
		}()
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actionLog) > 50 {
		t.Errorf("actionLog grew to %d under concurrent access, expected <= 50 (mutex not held?)",
			len(c.actionLog))
	}
	if len(c.actionLog) == 0 {
		t.Errorf("actionLog should not be empty after 200 attempts")
	}
}
