package lab

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func TestHoneytokensKeepAShapeAndTheirMarker(t *testing.T) {
	cases := []struct {
		key    string
		needle string
	}{
		{"tls.key", "BEGIN PRIVATE KEY"},
		{"api-token", "eyJhbGciOiJub25lIn0."},
		{"sha256", "00000000"},
		{"password", "honey-"},
	}
	for _, tc := range cases {
		got := Honeytoken("front-secrets", tc.key)
		if !strings.Contains(got, tc.needle) {
			t.Errorf("%s: %q lacks shape %q", tc.key, got, tc.needle)
		}
		mark := SecretPlaceholder("front-secrets", tc.key)
		if HoneyMarker(got) != mark {
			t.Errorf("%s: marker %q, want %q in %q", tc.key, HoneyMarker(got), mark, got)
		}
	}
}

func TestAHoneytokenInALookupNamesItsSecret(t *testing.T) {
	ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "front"}
	w := World{Workloads: map[model.ObjectRef]Workload{
		ref: {Ref: ref, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{
				Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "front-secrets"}, Key: "token",
				}},
			}}}},
		}}},
	}}
	tokens := secretTokens(w)
	host := Honeytoken("front-secrets", "token") + ".svc"
	if got := tokenIn(host, tokens); got != "key token of secret front-secrets" {
		t.Fatalf("got %q", got)
	}
}

func TestASecretVolumeWithKeysBecomesAHoneytokenSecret(t *testing.T) {
	ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "front"}
	w := World{
		Namespace: "shop",
		Workloads: map[model.ObjectRef]Workload{ref: {
			Ref: ref, Replicas: 1,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "front"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:1",
					VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: "/tls"}}}},
					Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "front-tls", Items: []corev1.KeyToPath{
							{Key: "tls.key", Path: "tls.key"}}}}}},
				},
			},
		}},
		Digests: map[string]string{"app:1": "app@sha256:abc"},
	}
	plan, err := Synthesize(w, ref, "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Secrets) != 1 || plan.Secrets[0].Name != "front-tls" {
		t.Fatalf("secrets %+v", plan.Secrets)
	}
	if got := string(plan.Secrets[0].Data["tls.key"]); !strings.Contains(got, "BEGIN PRIVATE KEY") {
		t.Fatalf("honeytoken %q", got)
	}
	if plan.Pods[0].Spec.Volumes[0].Secret == nil {
		t.Fatal("volume should still mount the secret")
	}
}
