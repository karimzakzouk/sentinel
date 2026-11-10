package lab

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// The deletion control must run as non-root so restricted Lab namespaces
// admit the probe image, which otherwise defaults to root and never starts.
func TestDeletionControlRunsAsNonRoot(t *testing.T) {
	failing := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "web",
			Image: "shop:bad",
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/healthz"},
			}},
			SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr.To(true)},
		}},
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true)},
	}}
	got := DeletionControl(failing)
	if got.Spec.Containers[0].Image != Image || len(got.Spec.Containers[0].Args) != 1 || got.Spec.Containers[0].Args[0] != "pause" {
		t.Fatalf("container %+v", got.Spec.Containers[0])
	}
	if got.Spec.Containers[0].ReadinessProbe == nil || got.Spec.Containers[0].ReadinessProbe.HTTPGet == nil {
		t.Fatal("kept the copy's probe")
	}
	if u := got.Spec.SecurityContext.RunAsUser; u == nil || *u != 65532 || !ptr.Deref(got.Spec.SecurityContext.RunAsNonRoot, false) {
		t.Fatalf("pod security %+v", got.Spec.SecurityContext)
	}
	sc := got.Spec.Containers[0].SecurityContext
	if u := sc.RunAsUser; u == nil || *u != 65532 || !ptr.Deref(sc.RunAsNonRoot, false) {
		t.Fatalf("container security %+v", sc)
	}
}
