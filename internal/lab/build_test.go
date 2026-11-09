package lab

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The gate comes before a copy's own init containers, and a gate that gave
// up marks the pod as unsealed, not as a failing run.
func TestTheSealGateComesFirstAndItsFailureIsTold(t *testing.T) {
	gate := SealGate("10.96.0.53", "10.96.0.1:443")
	if !strings.Contains(gate.Command[2], "/agnhost connect 10.96.0.53:53") || !strings.Contains(gate.Command[2], "! /agnhost connect 10.96.0.1:443") {
		t.Fatalf("gate script = %s", gate.Command[2])
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "migrate"}}, Containers: []corev1.Container{{Name: "app"}}}}
	got := gated(pod, gate)
	if len(got.Spec.InitContainers) != 2 || got.Spec.InitContainers[0].Name != GateName || len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("init containers = %+v; want the gate first, and the original untouched", got.Spec.InitContainers)
	}
	got.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: GateName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}}
	if !gateFailed(got) {
		t.Fatal("a gate that gave up was not noticed")
	}
	got.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 0
	if gateFailed(got) {
		t.Fatal("a gate that passed was taken for a failure")
	}
}
