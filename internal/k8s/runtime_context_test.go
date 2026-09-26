package k8s

import (
	"context"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/schemas"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// TestFetchPodRuntimeContext verifies the v1.7.4 runtime ground-truth
// fetch: env var NAMES (never values), configmap/secret refs, resources.
// This is the guard against the run-#11 user-api class of harmful fixes —
// the LLM must see that the deployment sets USER_DATABASE_URL, not guess
// a canonical DATABASE_URL.
func TestFetchPodRuntimeContext(t *testing.T) {
	cpu := resource.MustParse("250m")
	mem := resource.MustParse("128Mi")
	cpuLim := resource.MustParse("1")
	memLim := resource.MustParse("256Mi")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-api-86cc7c8bf7-7bqvf",
			Namespace: "marketly",
			Labels:    map[string]string{"app.kubernetes.io/name": "user-api"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "user-api",
					Image:   "ghcr.io/marketly-org/user-api:abc123",
					Command: []string{"node", "dist/server.js"},
					Env: []corev1.EnvVar{
						{Name: "USER_DATABASE_URL", Value: "postgresql://secret-creds"},
						{Name: "JWT_SECRET", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "marketly-secrets"}, Key: "jwt-secret"},
						}},
						{Name: "SMTP_HOST", ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "smtp"},
						}},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: mem},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: cpuLim, corev1.ResourceMemory: memLim},
					},
				},
			},
		},
	}

	clientset := k8sfake.NewSimpleClientset(pod)
	c := &Client{clientset: clientset}

	rc, err := c.FetchPodRuntimeContext(context.Background(), "user-api", "marketly")
	if err != nil {
		t.Fatalf("FetchPodRuntimeContext: %v", err)
	}
	if rc == nil {
		t.Fatal("expected runtime context, got nil")
	}
	if rc.PodName != "user-api-86cc7c8bf7-7bqvf" {
		t.Errorf("pod name = %q", rc.PodName)
	}

	// Names, sorted; values must NEVER appear.
	wantEnv := []string{"JWT_SECRET", "SMTP_HOST", "USER_DATABASE_URL"}
	if len(rc.EnvVarNames) != len(wantEnv) {
		t.Fatalf("env names = %v, want %v", rc.EnvVarNames, wantEnv)
	}
	for i, w := range wantEnv {
		if rc.EnvVarNames[i] != w {
			t.Errorf("env names[%d] = %q, want %q", i, rc.EnvVarNames[i], w)
		}
	}
	if contains(rc.EnvVarNames, "postgresql://secret-creds") || containsAny(rc.SecretRefs, "secret-creds") {
		t.Error("env var VALUE leaked into runtime context")
	}

	// Refs.
	if !contains(rc.ConfigMapRefs, "app-config") {
		t.Errorf("configmap refs = %v, want app-config", rc.ConfigMapRefs)
	}
	if !contains(rc.SecretRefs, "marketly-secrets") {
		t.Errorf("secret refs = %v, want marketly-secrets", rc.SecretRefs)
	}

	// Resources.
	if rc.MemoryLimit != "256Mi" || rc.CPULimit != "1" {
		t.Errorf("limits = cpu:%s mem:%s, want cpu:1 mem:256Mi", rc.CPULimit, rc.MemoryLimit)
	}
	if rc.MemoryRequest != "128Mi" {
		t.Errorf("memory request = %q, want 128Mi", rc.MemoryRequest)
	}

	if rc.Image == "" || rc.ContainerName != "user-api" {
		t.Errorf("image/container = %q/%q", rc.Image, rc.ContainerName)
	}
}

// TestFetchPodRuntimeContextNoPod asserts the best-effort contract: no
// matching pod returns (nil, nil), never an error.
func TestFetchPodRuntimeContextNoPod(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	c := &Client{clientset: clientset}
	rc, err := c.FetchPodRuntimeContext(context.Background(), "ghost-service", "marketly")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rc != nil {
		t.Fatalf("expected nil context, got %+v", rc)
	}
}

// TestFetchPodRuntimeContextSidecarSkip asserts the sidecar filter picks
// the app container, not istio-proxy.
func TestFetchPodRuntimeContextSidecarSkip(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-pod", Namespace: "marketly",
			Labels: map[string]string{"app": "myapp"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "istio-proxy", Image: "istio"},
				{Name: "myapp", Image: "myapp:1", Env: []corev1.EnvVar{{Name: "APP_MODE", Value: "prod"}}},
			},
		},
	}
	clientset := k8sfake.NewSimpleClientset(pod)
	c := &Client{clientset: clientset}
	rc, err := c.FetchPodRuntimeContext(context.Background(), "myapp", "marketly")
	if err != nil || rc == nil {
		t.Fatalf("rc=%v err=%v", rc, err)
	}
	if rc.ContainerName != "myapp" {
		t.Errorf("container = %q, want myapp (sidecar must be skipped)", rc.ContainerName)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsAny(s []string, v ...string) bool {
	for _, x := range s {
		for _, y := range v {
			if x == y {
				return true
			}
		}
	}
	return false
}

// Compile-time assertion that the schema type stays wire-compatible.
var _ = schemas.PodRuntimeContext{}

// TestFetchPodRuntimeContextCrashVitals (v1.7.16) verifies the restart /
// termination signals are read from the worst-crashing pod's container
// status: restart count, OOMKilled last termination with exit 137, and
// the CrashLoopBackOff waiting reason. This is the evidence that was
// invisible to the investigator in run #29 (user-api OOMKilled 5x while
// the LLM kept proposing config-validation patches).
func TestFetchPodRuntimeContextCrashVitals(t *testing.T) {
	healthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-api-86cc7c8bf7-aaa",
			Namespace: "marketly",
			Labels:    map[string]string{"app.kubernetes.io/name": "user-api"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "user-api", Image: "user-api:1"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "user-api", Ready: true, RestartCount: 0},
			},
		},
	}
	// The crasher: listed SECOND so the worst-pod selection is what finds it.
	crasher := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-api-86cc7c8bf7-bbb",
			Namespace: "marketly",
			Labels:    map[string]string{"app.kubernetes.io/name": "user-api"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "user-api", Image: "user-api:1"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "user-api",
					RestartCount: 5,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				},
			},
		},
	}

	clientset := k8sfake.NewSimpleClientset(healthy, crasher)
	c := &Client{clientset: clientset}

	rc, err := c.FetchPodRuntimeContext(context.Background(), "user-api", "marketly")
	if err != nil {
		t.Fatalf("FetchPodRuntimeContext: %v", err)
	}
	if rc == nil {
		t.Fatal("expected runtime context, got nil")
	}
	if rc.PodName != "user-api-86cc7c8bf7-bbb" {
		t.Errorf("pod name = %q, want the crash-looping pod user-api-86cc7c8bf7-bbb", rc.PodName)
	}
	if rc.RestartCount != 5 {
		t.Errorf("restart count = %d, want 5", rc.RestartCount)
	}
	if rc.LastTerminationReason != "OOMKilled" {
		t.Errorf("last termination reason = %q, want OOMKilled", rc.LastTerminationReason)
	}
	if rc.LastTerminationExitCode != 137 {
		t.Errorf("last termination exit code = %d, want 137", rc.LastTerminationExitCode)
	}
	if !rc.OOMKilled {
		t.Error("OOMKilled flag not set from OOMKilled termination reason")
	}
	if rc.WaitingReason != "CrashLoopBackOff" {
		t.Errorf("waiting reason = %q, want CrashLoopBackOff", rc.WaitingReason)
	}
}

// TestFetchPodRuntimeContextExit137SetsOOM (v1.7.16) — some runtimes
// report the OOM kill only as exit code 137 without a reason string.
func TestFetchPodRuntimeContextExit137SetsOOM(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-7d9c8bf7-ccc",
			Namespace: "marketly",
			Labels:    map[string]string{"app.kubernetes.io/name": "worker"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "worker", Image: "worker:1"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "worker",
					RestartCount: 2,
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 137},
					},
				},
			},
		},
	}
	clientset := k8sfake.NewSimpleClientset(pod)
	c := &Client{clientset: clientset}

	rc, err := c.FetchPodRuntimeContext(context.Background(), "worker", "marketly")
	if err != nil {
		t.Fatalf("FetchPodRuntimeContext: %v", err)
	}
	if !rc.OOMKilled {
		t.Errorf("exit 137 without reason must still set OOMKilled, got %+v", rc)
	}
}
