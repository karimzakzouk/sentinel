package lab

import (
	"slices"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFromBodiesTurnsCapturesIntoExperimentWrites(t *testing.T) {
	got := FromBodies([]capture.BodySample{
		{Method: "POST", Path: "/cart", Body: `{"id":1}`, Status: 201},
		{Method: "POST", Path: "/cart", Body: `{"id":2}`, Status: 201},
		{Method: "PUT", Path: "/item", Status: 204},
	}, 5)
	if len(got) != 2 || got[0].Method != "POST" || got[0].Status != 201 || got[1].Method != "PUT" {
		t.Fatalf("writes %+v", got)
	}
	if bad := FromBodies([]capture.BodySample{{Method: "GET", Path: "/x"}}, 5); len(bad) != 0 {
		t.Fatalf("GET kept: %+v", bad)
	}
}

func TestWritesAnsweredComparesStatuses(t *testing.T) {
	writes := []v1alpha1.WriteReplay{{Status: 200}, {Status: 500}}
	if !writesAnswered([]int{200, 500}, writes, false) {
		t.Fatal("matching statuses should pass")
	}
	if writesAnswered([]int{200, 404}, writes, false) {
		t.Fatal("a mismatch should fail")
	}
	if !writesAnswered([]int{200, 503}, writes, true) {
		t.Fatal("both server errors should pass with empty datastores")
	}
	if notes := WriteCompareNotes([]int{200, 404}, []v1alpha1.WriteReplay{{Method: "POST", Path: "/a", Status: 200},
		{Method: "PUT", Path: "/b", Status: 500}}); len(notes) != 1 || !strings.Contains(notes[0], "PUT /b") {
		t.Fatalf("notes %v", notes)
	}
}

func TestWriteReplayedPathsAndBodiesAreArgumentsNotScript(t *testing.T) {
	pod := writeReplayPod("lab", "web-cand-0-write-replay", "10.0.0.7", []v1alpha1.WriteReplay{
		{Method: "POST", Path: "/cart", Port: 8080, Body: `{"x":1}`},
		{Method: "PUT", Path: "/x';reboot;'", Port: 8080, Body: "evil"},
	})
	cmd := pod.Spec.Containers[0].Command
	if strings.Contains(cmd[2], "/cart") || strings.Contains(cmd[2], "reboot") {
		t.Fatalf("path or body is in the script: %s", cmd[2])
	}
	want := []string{"writereplay", "POST", "http://10.0.0.7:8080/cart", `{"x":1}`, "PUT", "http://10.0.0.7:8080/x';reboot;'", "evil"}
	if !slices.Equal(cmd[3:], want) {
		t.Fatalf("arguments %q", cmd[3:])
	}
}

func TestWriteReplayCodesAndPorts(t *testing.T) {
	codes, err := writeReplayCodes("0 201\n1 500\n", 2)
	if err != nil || !slices.Equal(codes, []int{201, 500}) {
		t.Fatalf("codes %v, %v", codes, err)
	}
	w := []v1alpha1.WriteReplay{{Path: "/a"}, {Path: "/b", Port: 9090}}
	copied := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Ports: []corev1.ContainerPort{{ContainerPort: 8080}}},
	}}}
	got := withWritePorts(w, copied)
	if len(got) != 2 || got[0].Port != 8080 || got[1].Port != 9090 {
		t.Fatalf("ports %+v", got)
	}
	plan := &Plan{
		Empty: []model.ObjectRef{{Kind: "StatefulSet", Name: "redis"}},
		Pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CopyOf: "StatefulSet.redis"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "redis:7"}}}}},
	}
	if got := SeedFidelity(plan, []capture.BodySample{{Method: "POST", Path: "/x", Body: "{}"}}); len(got) == 0 || !strings.Contains(got[0], "sentinel:lab:seed") {
		t.Fatalf("seed fidelity %v", got)
	}
}
