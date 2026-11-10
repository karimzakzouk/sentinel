package capture

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestInjectMITMMountsCAAndSetsProxyWhenUnset(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	InjectMITM(spec, "http://proxy:8888")
	if len(spec.Volumes) != 1 || spec.Volumes[0].Name != TrustVolume {
		t.Fatalf("volumes %+v", spec.Volumes)
	}
	c := spec.Containers[0]
	if len(c.VolumeMounts) != 2 {
		t.Fatalf("mounts %+v", c.VolumeMounts)
	}
	got := map[string]string{}
	for _, e := range c.Env {
		got[e.Name] = e.Value
	}
	for _, name := range append(trustEnv, "HTTPS_PROXY", "HTTP_PROXY", "JAVA_TOOL_OPTIONS") {
		if got[name] == "" {
			t.Fatalf("missing %s in %#v", name, got)
		}
	}
	if got["HTTPS_PROXY"] != "http://proxy:8888" {
		t.Fatalf("proxy %q", got["HTTPS_PROXY"])
	}
	if !strings.Contains(got["JAVA_TOOL_OPTIONS"], TrustStorePath) {
		t.Fatalf("jvm opts %q", got["JAVA_TOOL_OPTIONS"])
	}
}

func TestInjectMITMIsIdempotentAndKeepsExistingProxy(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "HTTPS_PROXY", Value: "http://other:1"}, {Name: "SSL_CERT_FILE", Value: "/mine"}},
	}}}
	InjectMITM(spec, "")
	InjectMITM(spec, "")
	if len(spec.Volumes) != 1 {
		t.Fatalf("volumes %d", len(spec.Volumes))
	}
	got := map[string]string{}
	for _, e := range spec.Containers[0].Env {
		got[e.Name] = e.Value
	}
	if got["HTTPS_PROXY"] != "http://other:1" || got["SSL_CERT_FILE"] != "/mine" {
		t.Fatalf("kept values %#v", got)
	}
}

func TestInjectMITMAppendsJavaToolOptions(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx1g"}},
	}}}
	InjectMITM(spec, "")
	got := spec.Containers[0].Env[envIndex(spec.Containers[0].Env, "JAVA_TOOL_OPTIONS")].Value
	if !strings.Contains(got, "-Xmx1g") || !strings.Contains(got, TrustStorePath) {
		t.Fatalf("java opts %q", got)
	}
}
