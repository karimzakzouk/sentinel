package lab

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
)

func TestSeedFidelityNotesRedisAndDeferredEngines(t *testing.T) {
	plan := &Plan{
		Empty: []model.ObjectRef{{Kind: "StatefulSet", Name: "redis"}, {Kind: "StatefulSet", Name: "db"}},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.redis"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "redis:7", Ports: []corev1.ContainerPort{{ContainerPort: 6379}}}}},
		}, {
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.db"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "postgres:16"}}},
		}},
	}
	notes := SeedFidelity(plan, []capture.BodySample{{Method: "POST", Body: `{"x":1}`}})
	if len(notes) < 2 {
		t.Fatalf("notes %v", notes)
	}
	if !strings.Contains(notes[0], "postgres") || !strings.Contains(notes[1], "sentinel:lab:seed") {
		t.Fatalf("notes %v", notes)
	}
}

func TestSeedRedisPodUsesBodiesAsArguments(t *testing.T) {
	pod := seedRedisPod("ns", "lab-seed-redis", "redis:7", "redis", 6379, []string{`{"a":1}`, "b"})
	args := pod.Spec.Containers[0].Command
	if args[len(args)-1] != "b" || pod.Spec.Containers[0].Image != "redis:7" {
		t.Fatalf("args %v image %s", args, pod.Spec.Containers[0].Image)
	}
}
