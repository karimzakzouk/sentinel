package fixer

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A memory limit lowered to what the container used while healthy brings
// its heap settings down with it, each to its runtime's share of the new
// limit; a setting that fits stays, and one taken from elsewhere is not
// touched.
func TestHeapSettingsFollowANewMemoryLimit(t *testing.T) {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{
		{Name: "JAVA_TOOL_OPTIONS", Value: "-XX:+UseG1GC -Xmx2g -Dfoo=bar"},
		{Name: "NODE_OPTIONS", Value: "--max-old-space-size=4096 --enable-source-maps"},
		{Name: "GOMEMLIMIT", Value: "100MiB"},
		{Name: "JAVA_OPTS", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "opts"}}},
	}}}}
	got := map[string]any{}
	for _, c := range runtimeFlags(spec, "app", "512Mi") {
		got[c.Field] = c.Good
	}
	want := map[string]any{
		"env:JAVA_TOOL_OPTIONS": "-XX:+UseG1GC -Xmx384m -Dfoo=bar",
		"env:NODE_OPTIONS":      "--max-old-space-size=384 --enable-source-maps",
	}
	if len(got) != len(want) || got["env:JAVA_TOOL_OPTIONS"] != want["env:JAVA_TOOL_OPTIONS"] || got["env:NODE_OPTIONS"] != want["env:NODE_OPTIONS"] {
		t.Fatalf("changes %v\nwant    %v", got, want)
	}
	if changes := runtimeFlags(spec, "app", "1Gi"); len(changes) != 2 {
		t.Fatalf("at 1Gi: %v", changes)
	}
	spec.Containers[0].Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "1GiB"}}
	if c := runtimeFlags(spec, "app", "512Mi"); len(c) != 1 || c[0].Good != "460MiB" {
		t.Fatalf("GOMEMLIMIT: %v", c)
	}
}
