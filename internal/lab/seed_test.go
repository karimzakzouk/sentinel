package lab

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
)

func TestSeedFidelityNotesSeededEngines(t *testing.T) {
	plan := &Plan{
		Empty: []model.ObjectRef{
			{Kind: "StatefulSet", Name: "redis"},
			{Kind: "StatefulSet", Name: "db"},
			{Kind: "StatefulSet", Name: "mq"},
		},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.redis"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "redis:7", Ports: []corev1.ContainerPort{{ContainerPort: 6379}}}}},
		}, {
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.db"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "postgres:16"}}},
		}, {
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.mq"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "rabbitmq:3-management"}}},
		}},
	}
	notes := SeedFidelity(plan, []capture.BodySample{{Method: "POST", Body: `{"x":1}`}})
	if len(notes) != 3 {
		t.Fatalf("notes %v", notes)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"sentinel:lab:seed", "sentinel_lab_seed", "sentinel.lab.seed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "not run yet") {
		t.Fatalf("seeded engines still deferred:\n%s", joined)
	}
}

func TestSeedRedisPodUsesBodiesAsArguments(t *testing.T) {
	pod := seedRedisPod("ns", "lab-seed-redis", "redis:7", "redis", 6379, []string{`{"a":1}`, "b"})
	args := pod.Spec.Containers[0].Command
	if args[len(args)-1] != "b" || pod.Spec.Containers[0].Image != "redis:7" {
		t.Fatalf("args %v image %s", args, pod.Spec.Containers[0].Image)
	}
}

func TestEmptyDatastoreDetectsEngineAndDefaultPort(t *testing.T) {
	cases := []struct {
		name, image, engine string
		port                int32
		declared            int32
	}{
		{"redis", "redis:7", "redis", 6379, 0},
		{"valkey", "valkey/valkey:8", "valkey", 6379, 0},
		{"postgres", "postgres:16", "postgres", 5432, 0},
		{"postgresql", "bitnami/postgresql:16", "postgresql", 5432, 0},
		{"mysql", "mysql:8", "mysql", 3306, 0},
		{"mariadb", "mariadb:11", "mariadb", 3306, 0},
		{"mongo", "mongo:7", "mongo", 27017, 0},
		{"mongodb", "mongodb:7", "mongodb", 27017, 0},
		{"kafka", "bitnami/kafka:3.7", "kafka", 9092, 0},
		{"rabbitmq", "rabbitmq:3", "rabbitmq", 5672, 0},
		{"postgres-custom-port", "postgres:16", "postgres", 15432, 15432},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ports []corev1.ContainerPort
			if tc.declared > 0 {
				ports = []corev1.ContainerPort{{ContainerPort: tc.declared}}
			}
			plan := &Plan{
				Empty: []model.ObjectRef{{Kind: "StatefulSet", Name: "store"}},
				Pods: []*corev1.Pod{{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.store"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: tc.image, Ports: ports}}},
				}},
			}
			image, host, port, engine, ok := emptyDatastore(plan, plan.Empty[0])
			if !ok || engine != tc.engine || port != tc.port || image != tc.image || host != "store" {
				t.Fatalf("got image=%s host=%s port=%d engine=%s ok=%v", image, host, port, engine, ok)
			}
			if seedFamily(engine) == "" {
				t.Fatalf("engine %s should be seedable", engine)
			}
		})
	}
}

func TestSeedPodChoosesScriptPerEngine(t *testing.T) {
	bodies := []string{`{"a":1}`}
	cases := []struct {
		engine string
		want   []string
	}{
		{"redis", []string{"redis-cli", "sentinel:lab:seed:"}},
		{"valkey", []string{"redis-cli", "sentinel:lab:seed:"}},
		{"postgres", []string{"psql", "sentinel_lab_seed"}},
		{"postgresql", []string{"psql", "sentinel_lab_seed"}},
		{"mysql", []string{"mysql", "sentinel_lab_seed"}},
		{"mariadb", []string{"mysql", "sentinel_lab_seed"}},
		{"mongo", []string{"mongosh", "sentinel_lab_seed"}},
		{"mongodb", []string{"mongosh", "sentinel_lab_seed"}},
		{"kafka", []string{"kafka-console-producer", "sentinel-lab-seed"}},
		{"rabbitmq", []string{"rabbitmqadmin", "sentinel.lab.seed"}},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			script := seedScript(tc.engine)
			if script == "" {
				t.Fatal("no script")
			}
			for _, want := range tc.want {
				if !strings.Contains(script, want) {
					t.Fatalf("script for %s lacks %q:\n%s", tc.engine, want, script)
				}
			}
			pod := seedPod("ns", "lab-seed-"+tc.engine, tc.engine+":1", tc.engine, defaultSeedPort(tc.engine), tc.engine, bodies)
			cmd := pod.Spec.Containers[0].Command
			if len(cmd) < 5 || cmd[2] != script || cmd[len(cmd)-1] != bodies[0] {
				t.Fatalf("command %v", cmd)
			}
			if pod.Spec.Containers[0].Image != tc.engine+":1" {
				t.Fatalf("image %s", pod.Spec.Containers[0].Image)
			}
		})
	}
}

func TestSeedPortsIncludeRabbitManagement(t *testing.T) {
	got := seedPorts("rabbitmq", 5672)
	if len(got) != 2 || got[0] != 5672 || got[1] != 15672 {
		t.Fatalf("ports %v", got)
	}
	got = seedPorts("redis", 6379)
	if len(got) != 1 || got[0] != 6379 {
		t.Fatalf("redis ports %v", got)
	}
}
