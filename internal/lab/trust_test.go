package lab

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestInjectTrustSetsCurlAndAWSBundles(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	injectTrust(spec, func(string, ...any) {}, stringer("Deployment/shop/front"))
	got := map[string]string{}
	for _, e := range spec.Containers[0].Env {
		got[e.Name] = e.Value
	}
	for _, name := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "AWS_CA_BUNDLE"} {
		if got[name] != labCAPath {
			t.Fatalf("%s=%q want %s; env %#v", name, got[name], labCAPath, got)
		}
	}
	if !strings.Contains(got["JAVA_TOOL_OPTIONS"], labTrustStorePath) {
		t.Fatalf("JAVA_TOOL_OPTIONS %q", got["JAVA_TOOL_OPTIONS"])
	}
}

func TestInjectTrustAppendsLiteralJavaToolOptions(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx1g"}},
	}}}
	injectTrust(spec, func(string, ...any) {}, stringer("x"))
	got := envVar(spec.Containers[0].Env, "JAVA_TOOL_OPTIONS").Value
	if !strings.Contains(got, "-Xmx1g") || !strings.Contains(got, labTrustStorePath) {
		t.Fatalf("JAVA_TOOL_OPTIONS %q", got)
	}
}

func TestInjectTrustLeavesJavaToolOptionsValueFrom(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{
			Name: "JAVA_TOOL_OPTIONS",
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "jvm"}, Key: "opts",
			}},
		}},
	}}}
	injectTrust(spec, func(string, ...any) {}, stringer("x"))
	e := envVar(spec.Containers[0].Env, "JAVA_TOOL_OPTIONS")
	if e.ValueFrom == nil || e.Value != "" {
		t.Fatalf("ValueFrom mutated: %+v", e)
	}
}

type stringer string

func (s stringer) String() string { return string(s) }

func TestInjectTrustNotesWhenAllTrustEnvKept(t *testing.T) {
	env := []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", ValueFrom: &corev1.EnvVarSource{
		ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "j"}, Key: "o"},
	}}}
	for _, name := range trustEnv {
		env = append(env, corev1.EnvVar{Name: name, Value: "/already"})
	}
	spec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: env}}}
	var notes []string
	injectTrust(spec, func(format string, args ...any) {
		notes = append(notes, fmt.Sprintf(format, args...))
	}, stringer("Deployment/shop/front"))
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "no trust override was applied") {
		t.Fatalf("notes:\n%s", joined)
	}
}
