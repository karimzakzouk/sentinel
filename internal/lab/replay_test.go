package lab

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// A read not answered at all fails a run; a server error does too, unless
// the world's datastores are empty.
func TestACopyMustAnswerEveryReplayedRead(t *testing.T) {
	codes, err := replayCodes("1 503\n0 200\n2 404\n", 3)
	if err != nil || !slices.Equal(codes, []int{200, 503, 404}) {
		t.Fatalf("codes %v, %v", codes, err)
	}
	if answered(codes, false) || !answered(codes, true) {
		t.Fatal("a server error is held against a copy only where datastores are not empty")
	}
	if answered([]int{200, 0}, true) {
		t.Fatal("a read not answered passed")
	}
	if _, err := replayCodes("0 200\n", 2); err == nil {
		t.Fatal("a missing line was not an error")
	}
}

// Captured paths reach the replay as arguments, never as script.
func TestReplayedPathsAreArgumentsNotScript(t *testing.T) {
	pod := replayPod("lab", "web-cand-0-replay", "10.0.0.7", []v1alpha1.ReplayRequest{
		{Path: "/cart/12345", Port: 8080}, {Path: "/x';reboot;'", Port: 8080}})
	cmd := pod.Spec.Containers[0].Command
	if strings.Contains(cmd[2], "/cart") || strings.Contains(cmd[2], "reboot") {
		t.Fatalf("a path is in the script: %s", cmd[2])
	}
	if !slices.Equal(cmd[3:], []string{"replay", "http://10.0.0.7:8080/cart/12345", "http://10.0.0.7:8080/x';reboot;'"}) {
		t.Fatalf("arguments %q", cmd[3:])
	}
	if pod.Labels[Label] != "true" || len(pod.Labels) != 1 || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("the replay pod could join a copied Service or carry a token: %+v", pod.ObjectMeta)
	}
}

// A run that stays ready but does not answer its traffic, like the
// deletion control, fails; one that answers it works.
func TestARunWorksOnlyIfItsCopyAnswersItsTraffic(t *testing.T) {
	hold := holdFor
	holdFor = 0
	t.Cleanup(func() { holdFor = hold })
	client := fake.NewClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	readyPod := func(name, ip string) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "lab"}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: ip, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		if _, err := client.CoreV1().Pods("lab").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	readyPod("web-cand-0", "10.0.0.7")
	readyPod("web-del-0", "10.0.0.8")
	replies := map[string]string{"web-cand-0-replay": "0 200\n1 404\n", "web-del-0-replay": "0 0\n1 0\n"}
	go func() {
		for ctx.Err() == nil {
			for name, msg := range replies {
				pod, err := client.CoreV1().Pods("lab").Get(ctx, name, metav1.GetOptions{})
				if err != nil || pod.Status.Phase == corev1.PodSucceeded {
					continue
				}
				pod.Status.Phase = corev1.PodSucceeded
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "probe",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: msg}}}}
				_, _ = client.CoreV1().Pods("lab").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	runs := []*racePod{{variant: "candidate", name: "web-cand-0", created: time.Now()}, {variant: "deletion control", name: "web-del-0", created: time.Now()}}
	traffic := Traffic{Requests: []v1alpha1.ReplayRequest{{Path: "/", Port: 8080}, {Path: "/cart/1", Port: 8080}}}
	if err := (&Prover{Client: client}).watchRuns(ctx, "lab", runs, traffic); err != nil {
		t.Fatal(err)
	}
	if !*runs[0].ok || *runs[1].ok {
		t.Fatalf("candidate ok=%v, deletion control ok=%v", *runs[0].ok, *runs[1].ok)
	}
	if runs[0].replay != "web-cand-0-replay" {
		t.Fatalf("replay pod %q", runs[0].replay)
	}
}

// A read the agent saw no port for goes to the copy's first declared one.
func TestReadsWithoutAPortGoToTheCopysFirstPort(t *testing.T) {
	reads := []v1alpha1.ReplayRequest{{Path: "/a"}, {Path: "/b", Port: 9090}}
	copied := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Ports: []corev1.ContainerPort{{ContainerPort: 53, Protocol: corev1.ProtocolUDP}, {ContainerPort: 8080}}}}}}
	if got := withPorts(reads, copied); len(got) != 2 || got[0].Port != 8080 || got[1].Port != 9090 {
		t.Fatalf("ports %+v", got)
	}
	if got := withPorts(reads, &corev1.Pod{}); len(got) != 1 || got[0].Path != "/b" {
		t.Fatalf("without a declared port %+v", got)
	}
}
