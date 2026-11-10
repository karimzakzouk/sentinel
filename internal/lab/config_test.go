package lab

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// The configuration reverted is the failing copy with the previous
// revision's command, arguments and environment, variables the failing one
// added gone; its image and its limits stay the failing one's.
func TestConfigRevertTakesOnlyTheConfiguration(t *testing.T) {
	failing := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "web", Image: "web:2", Args: []string{"--no-such-flag"},
		Env:       []corev1.EnvVar{{Name: "PORT", Value: "eighty"}, {Name: "MODE", Value: "crash"}},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")}},
	}}}}
	previous := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "web", Image: "web:1", Env: []corev1.EnvVar{{Name: "PORT", Value: "8080"}},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
	}}}}
	got, ok := ConfigRevert(failing, previous)
	if !ok {
		t.Fatal("no configuration to revert")
	}
	c := got.Spec.Containers[0]
	if c.Image != "web:2" || c.Args != nil || len(c.Env) != 1 || c.Env[0].Value != "8080" || c.Resources.Limits.Memory().String() != "16Mi" {
		t.Fatalf("reverted: image %s, args %v, env %v, memory %s", c.Image, c.Args, c.Env, c.Resources.Limits.Memory())
	}
	if failing.Spec.Containers[0].Env[0].Value != "eighty" {
		t.Fatal("the failing copy changed")
	}
	if _, ok := ConfigRevert(failing, failing); ok {
		t.Fatal("a copy with the same configuration was a candidate")
	}
	renamed := previous.DeepCopy()
	renamed.Spec.Containers[0].Name = "server"
	if _, ok := ConfigRevert(failing, renamed); ok {
		t.Fatal("copies with different containers were matched")
	}
}

// A built fix replaces the failing image of its repository, pinned or not;
// an image of another repository has nothing to replace.
func TestImageCandidateReplacesTheFailingImage(t *testing.T) {
	failing := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "web", Image: "registry.test/shop/web@sha256:bad"}, {Name: "proxy", Image: "envoy:1"}}}}
	got, err := ImageCandidate(failing, "registry.test/shop/web:3f2a9c1")
	if err != nil || got.Spec.Containers[0].Image != "registry.test/shop/web:3f2a9c1" || got.Spec.Containers[1].Image != "envoy:1" {
		t.Fatalf("candidate %+v, %v", got, err)
	}
	if _, err := ImageCandidate(failing, "registry.test/other:1"); err == nil {
		t.Fatal("an image of another repository was raced")
	}
}

// A learned limit is raced as the failing copy with the limit in place.
func TestValuesCandidateSetsTheLearnedLimits(t *testing.T) {
	failing := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "echo", Image: "echo:2",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6Mi")}}}}}}
	pod, err := ValuesCandidate(failing, []v1alpha1.ValueOverride{{Container: "echo", Field: "limits.memory", Value: "15Mi"},
		{Container: "echo", Field: "requests.cpu", Value: "20m"}})
	if err != nil {
		t.Fatal(err)
	}
	r := pod.Spec.Containers[0].Resources
	if r.Limits.Memory().String() != "15Mi" || r.Requests.Cpu().String() != "20m" || failing.Spec.Containers[0].Resources.Limits.Memory().String() != "6Mi" {
		t.Fatalf("resources %+v", r)
	}
	if _, err := ValuesCandidate(failing, []v1alpha1.ValueOverride{{Container: "sidecar", Field: "limits.memory", Value: "1Mi"}}); err == nil {
		t.Fatal("a container the copy lacks was accepted")
	}
	if got := Overridden([]v1alpha1.ValueOverride{{Container: "echo", Field: "limits.memory", Value: "15Mi"}}); got != "echo's limits.memory at 15Mi" {
		t.Fatalf("%q", got)
	}
}
