package executors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestBuildRestartPatch verifies the JSON merge patch shape for Restart.
func TestBuildRestartPatch(t *testing.T) {
	got := buildRestartPatch("2026-01-02T15:04:05Z")
	want := `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-01-02T15:04:05Z"}}}}}`
	if got != want {
		t.Errorf("buildRestartPatch = %q, want %q", got, want)
	}
	// Must be valid JSON.
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Errorf("buildRestartPatch produced invalid JSON: %v", err)
	}
}

// TestBuildScalePatch verifies the JSON merge patch shape for Scale.
func TestBuildScalePatch(t *testing.T) {
	got := buildScalePatch(7)
	want := `{"spec":{"replicas":7}}`
	if got != want {
		t.Errorf("buildScalePatch(7) = %q, want %q", got, want)
	}
	got0 := buildScalePatch(0)
	want0 := `{"spec":{"replicas":0}}`
	if got0 != want0 {
		t.Errorf("buildScalePatch(0) = %q, want %q", got0, want0)
	}
}

// TestParseRevision covers the revision-annotation parser.
func TestParseRevision(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1", 1},
		{"42", 42},
		{"", 0},
		{"not-a-number", 0},
		{"0", 0},
		{"-1", -1}, // valid int64, treated as oldest
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := parseRevision(c.in); got != c.want {
				t.Errorf("parseRevision(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestMustJSON verifies mustJSON marshals cleanly.
func TestMustJSON(t *testing.T) {
	got := mustJSON(map[string]string{"a": "b"})
	if !strings.Contains(got, `"a":"b"`) {
		t.Errorf("mustJSON = %q, expected \"a\":\"b\" present", got)
	}
}

// TestRestartPatchesAnnotation uses a fake clientset to verify Restart
// actually patches the pod template annotation.
func TestRestartPatchesAnnotation(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments",
			Namespace: "prod",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "payments"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payments"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep)
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixRestart,
		TargetService:   "payments",
		TargetNamespace: "prod",
	}
	if err := e.Restart(context.Background(), fp); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	got, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "payments", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Restart: %v", err)
	}
	ts, ok := got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]
	if !ok || ts == "" {
		t.Errorf("Restart should set kubectl.kubernetes.io/restartedAt annotation, got: %v", got.Spec.Template.Annotations)
	}
}

// TestScaleUpdatesReplicas uses a fake clientset to verify Scale patches
// the replica count.
func TestScaleUpdatesReplicas(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep)
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixScale,
		TargetService:   "api",
		TargetNamespace: "prod",
	}
	if err := e.Scale(context.Background(), fp, 5); err != nil {
		t.Fatalf("Scale failed: %v", err)
	}

	got, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Scale: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 5 {
		t.Errorf("Scale should set replicas to 5, got: %v", got.Spec.Replicas)
	}
}

// TestRestartFallsBackToLabel verifies Restart falls back to the
// app.kubernetes.io/name label selector when the named lookup fails.
func TestRestartFallsBackToLabel(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-v1",
			Namespace: "prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "api"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep)
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixRestart,
		TargetService:   "api", // not the Deployment name "api-v1"
		TargetNamespace: "prod",
	}
	if err := e.Restart(context.Background(), fp); err != nil {
		t.Fatalf("Restart should fall back to label selector: %v", err)
	}

	got, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api-v1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Restart: %v", err)
	}
	if _, ok := got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; !ok {
		t.Errorf("Restart should set restartedAt annotation via label fallback")
	}
}

// TestRestartMissingDeployment verifies Restart returns an error when the
// deployment doesn't exist (and no label match either).
func TestRestartMissingDeployment(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixRestart,
		TargetService:   "ghost",
		TargetNamespace: "prod",
	}
	err := e.Restart(context.Background(), fp)
	if err == nil {
		t.Fatal("Restart on missing deployment should return error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestRollbackPicksPreviousRevision verifies Rollback patches the
// deployment template to match the immediately-previous revision
// (highest revision that is not the current one).
func TestRollbackPicksPreviousRevision(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api",
			Namespace: "prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "3",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "current:v3"}}},
			},
		},
	}
	// revision 2 = the previous (highest non-current).
	rs2 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-2",
			Namespace: "prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "2",
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "previous:v2"}}},
			},
		},
	}
	// revision 1 = older than the previous.
	rs1 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-1",
			Namespace: "prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "1",
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "ancient:v1"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep, rs1, rs2)
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixRollback,
		TargetService:   "api",
		TargetNamespace: "prod",
	}
	if err := e.Rollback(context.Background(), fp); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	got, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Rollback: %v", err)
	}
	if len(got.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("deployment has no containers after rollback")
	}
	img := got.Spec.Template.Spec.Containers[0].Image
	if img != "previous:v2" {
		t.Errorf("Rollback should pick revision 2 (immediately-previous), got image %q, want previous:v2", img)
	}
}

// TestRollbackNoPrevious verifies Rollback errors when there's no
// previous ReplicaSet to roll back to.
func TestRollbackNoPrevious(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api",
			Namespace:   "prod",
			Labels:      map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "v1"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep)
	e := NewK8s(clientset)

	fp := &schemas.FixProposal{
		FixType:         schemas.FixRollback,
		TargetService:   "api",
		TargetNamespace: "prod",
	}
	err := e.Rollback(context.Background(), fp)
	if err == nil {
		t.Fatal("Rollback with only one RS should return error")
	}
	if !strings.Contains(err.Error(), "no previous") && !strings.Contains(err.Error(), "found 1") {
		t.Errorf("error should mention no-previous, got: %v", err)
	}
}

// TestCopyConfigMapsAndSecrets uses a fake clientset to verify the
// sandbox ConfigMap/Secret copier collects errors and treats 409 as success.
func TestCopyConfigMapsAndSecrets(t *testing.T) {
	srcNS := "prod"
	dstNS := "sentinel-sandbox-abc"
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: srcNS},
		Data:       map[string]string{"KEY": "val"},
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: srcNS},
		Data:       map[string][]byte{"TOKEN": []byte("secret")},
		Type:       corev1.SecretTypeOpaque,
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: srcNS},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app",
						EnvFrom: []corev1.EnvFromSource{
							{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
							{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"}}},
						},
					}},
				},
			},
		},
	}
	clientset := fake.NewSimpleClientset(cm, sec)
	if err := CopyConfigMapsAndSecrets(context.Background(), clientset, deploy, srcNS, dstNS); err != nil {
		t.Fatalf("CopyConfigMapsAndSecrets failed: %v", err)
	}

	gotCM, err := clientset.CoreV1().ConfigMaps(dstNS).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get CM: %v", err)
	}
	if gotCM.Data["KEY"] != "val" {
		t.Errorf("CM data not copied: %v", gotCM.Data)
	}
	gotSec, err := clientset.CoreV1().Secrets(dstNS).Get(context.Background(), "app-secret", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get Secret: %v", err)
	}
	if string(gotSec.Data["TOKEN"]) != "secret" {
		t.Errorf("Secret data not copied: %v", gotSec.Data)
	}
}

// TestCopyConfigMapsAndSecrets_MissingRef verifies the copier aggregates
// errors instead of swallowing them.
func TestCopyConfigMapsAndSecrets_MissingRef(t *testing.T) {
	srcNS := "prod"
	dstNS := "sentinel-sandbox"
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: srcNS},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app",
						EnvFrom: []corev1.EnvFromSource{
							{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm"}}},
						},
					}},
				},
			},
		},
	}
	clientset := fake.NewSimpleClientset()
	err := CopyConfigMapsAndSecrets(context.Background(), clientset, deploy, srcNS, dstNS)
	if err == nil {
		t.Fatal("expected aggregated error for missing ConfigMap")
	}
	if !strings.Contains(err.Error(), "missing-cm") {
		t.Errorf("error should mention missing-cm, got: %v", err)
	}
}

// TestRollbackByName verifies the chatops helper constructs a synthetic
// FixProposal and delegates to Rollback.
func TestRollbackByName(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api",
			Namespace:   "prod",
			Labels:      map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "v2"}}},
			},
		},
	}
	// Current revision RS (revision=2, matches deployment's current).
	rs2 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api-2",
			Namespace:   "prod",
			Labels:      map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "v2"}}},
			},
		},
	}
	// Previous revision RS (revision=1).
	rs1 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api-1",
			Namespace:   "prod",
			Labels:      map[string]string{"app.kubernetes.io/name": "api"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "v1"}}},
			},
		},
	}
	clientset := fake.NewSimpleClientset(dep, rs1, rs2)
	e := NewK8s(clientset)
	if err := e.RollbackByName(context.Background(), "prod", "api"); err != nil {
		t.Fatalf("RollbackByName failed: %v", err)
	}
	got, _ := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "v1" {
		t.Errorf("RollbackByName should roll back to v1, got %q",
			got.Spec.Template.Spec.Containers[0].Image)
	}
}
