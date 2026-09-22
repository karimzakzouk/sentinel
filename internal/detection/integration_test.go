package detection

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/kubernetes/fake"
)

// These tests exercise the full detection path — pod event → repo
// resolution (via owner chain) → status classification → cooldown →
// incident trigger — against the fake clientset. This is the closest thing
// to a cluster e2e that runs without kube-apiserver: no watcher plumbing is
// faked, only the watch delivery itself (handlePod is exactly what the
// watch loop invokes per event).

type capturedIncident struct {
	Service   string
	Namespace string
	Reason    string
	PodLogs   string
}

// newTestDetector builds a Detector over a fake clientset preloaded with
// objects, plus a channel that captures handler invocations.
func newTestDetector(t *testing.T, objects ...runtime.Object) (*Detector, chan capturedIncident) {
	t.Helper()
	clientset := fake.NewSimpleClientset(objects...)
	incidents := make(chan capturedIncident, 4)
	handler := func(service, namespace, reason, podLogs string) {
		incidents <- capturedIncident{Service: service, Namespace: namespace, Reason: reason, PodLogs: podLogs}
	}
	d := NewWithConfig(clientset, handler, "", time.Hour, 100, nil)
	return d, incidents
}

// crashLoopPod builds a pod in CrashLoopBackOff owned by rs, which is owned
// by the annotated deployment.
func crashLoopPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID("pod-uid-" + name),
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": "api"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "api-rs", APIVersion: "apps/v1"},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "api"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "api",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off 5m0s restarting failed container",
						},
					},
				},
			},
		},
	}
}

func annotatedDeployment(namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api",
			Namespace: namespace,
			Annotations: map[string]string{
				"sentinel.io/repo": "example-org/example-service",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
	}
}

func replicaSet(namespace string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-rs",
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"},
			},
		},
	}
}

// TestDetectionCrashLoopBackOffViaDeploymentAnnotation is the headline
// scenario: a pod crashes, the repo is discovered by walking the owner
// chain to the Deployment's sentinel.io/repo annotation, and the incident
// handler fires with the right service, namespace, and reason.
func TestDetectionCrashLoopBackOffViaDeploymentAnnotation(t *testing.T) {
	d, incidents := newTestDetector(t,
		annotatedDeployment("sim"),
		replicaSet("sim"),
		crashLoopPod("sim", "api-abc123"),
	)

	d.handlePod(context.Background(), crashLoopPod("sim", "api-abc123"))

	select {
	case inc := <-incidents:
		if inc.Service != "api" {
			t.Errorf("service = %q, want %q", inc.Service, "api")
		}
		if inc.Namespace != "sim" {
			t.Errorf("namespace = %q, want %q", inc.Namespace, "sim")
		}
		if inc.Reason == "" || inc.Reason[:len("CrashLoopBackOff")] != "CrashLoopBackOff" {
			t.Errorf("reason = %q, want CrashLoopBackOff prefix", inc.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incident handler did not fire for CrashLoopBackOff pod")
	}
}

// TestDetectionPodLevelAnnotation tests the direct pod-annotation path —
// no owner-chain walk needed.
func TestDetectionPodLevelAnnotation(t *testing.T) {
	pod := crashLoopPod("sim", "worker-xyz")
	pod.Annotations = map[string]string{"sentinel.io/repo": "example-org/worker"}
	pod.Labels = map[string]string{"app": "worker"}
	pod.OwnerReferences = nil

	d, incidents := newTestDetector(t, pod)
	d.handlePod(context.Background(), pod)

	select {
	case inc := <-incidents:
		if inc.Service != "worker" {
			t.Errorf("service = %q, want worker", inc.Service)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incident handler did not fire for pod-annotated crash")
	}
}

// TestDetectionUnannotatedPodIgnored verifies the negative case: pods
// without a discoverable repo never trigger.
func TestDetectionUnannotatedPodIgnored(t *testing.T) {
	pod := crashLoopPod("default", "stray-abc123")
	pod.OwnerReferences = nil // no annotated owner either

	d, incidents := newTestDetector(t, pod)
	d.handlePod(context.Background(), pod)

	select {
	case inc := <-incidents:
		t.Fatalf("unexpected incident for unannotated pod: %+v", inc)
	case <-time.After(300 * time.Millisecond):
		// pass — no trigger expected
	}
}

// TestDetectionCooldownSuppressesDuplicateTriggers verifies the same crash
// fingerprint does not fire twice within the cooldown window.
func TestDetectionCooldownSuppressesDuplicateTriggers(t *testing.T) {
	d, incidents := newTestDetector(t,
		annotatedDeployment("sim"),
		replicaSet("sim"),
		crashLoopPod("sim", "api-abc123"),
	)
	d.cooldown = time.Hour // long cooldown for the test

	ctx := context.Background()
	pod := crashLoopPod("sim", "api-abc123")
	d.handlePod(ctx, pod)
	d.handlePod(ctx, pod) // duplicate — must be suppressed

	select {
	case <-incidents:
		// first trigger delivered
	case <-time.After(2 * time.Second):
		t.Fatal("first trigger did not fire")
	}
	select {
	case inc := <-incidents:
		t.Fatalf("cooldown failed — duplicate incident fired: %+v", inc)
	case <-time.After(300 * time.Millisecond):
		// pass
	}
}

// TestDetectionOOMKilled covers the OOMKilled classification path.
func TestDetectionOOMKilled(t *testing.T) {
	pod := crashLoopPod("sim", "api-oom")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "api",
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:   "OOMKilled",
					ExitCode: 137,
				},
			},
		},
	}

	d, incidents := newTestDetector(t,
		annotatedDeployment("sim"),
		replicaSet("sim"),
		pod,
	)
	d.handlePod(context.Background(), pod)

	select {
	case inc := <-incidents:
		if inc.Service != "api" || inc.Namespace != "sim" {
			t.Errorf("got %+v, want service=api namespace=sim", inc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incident handler did not fire for OOMKilled pod")
	}
}
