package watcher

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func crashingPod(state corev1.ContainerState, last corev1.ContainerState) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "uid-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "web", State: state, LastTerminationState: last, RestartCount: 3},
		}},
	}
}

func TestACrashedInstanceIsFetchedOnce(t *testing.T) {
	crashed := &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", ContainerID: "containerd://abc"}
	fact := []Fact{{Kind: FactCrashLoop, Workload: web, Container: "web"}}
	waitingToRestart := LogRequests(crashingPod(corev1.ContainerState{Terminated: crashed}, corev1.ContainerState{}), fact)
	inBackOff := LogRequests(crashingPod(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		corev1.ContainerState{Terminated: crashed}), fact)
	if len(waitingToRestart) != 1 || waitingToRestart[0].Previous {
		t.Fatalf("an exited container's logs are its current ones: %+v", waitingToRestart)
	}
	if len(inBackOff) != 1 || !inBackOff[0].Previous {
		t.Fatalf("after the restart they are the previous ones: %+v", inBackOff)
	}
	if waitingToRestart[0].Key != inBackOff[0].Key {
		t.Fatalf("one crashed instance, two keys: %q and %q", waitingToRestart[0].Key, inBackOff[0].Key)
	}
	running := crashingPod(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, corev1.ContainerState{})
	if r := LogRequests(running, fact); len(r) != 0 {
		t.Fatalf("no crashed instance, yet %+v", r)
	}
}

func TestANotReadyPodFetchesItsUnreadyContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "uid-1"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(t0)}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "web", Ready: false}, {Name: "sidecar", Ready: true},
			},
		},
	}
	reqs := LogRequests(pod, []Fact{{Kind: FactNotReady, Workload: web}})
	if len(reqs) != 1 || reqs[0].Container != "web" || reqs[0].Previous || reqs[0].Reason != "not-ready" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestLogCollectorDedupesAndKeepsItsBudget(t *testing.T) {
	var mu sync.Mutex
	var got []LogEvidence
	c := NewLogCollector(fake.NewClientset(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(_ context.Context, ev LogEvidence) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	c.Limiter = rate.NewLimiter(rate.Every(time.Hour), 2)
	ctx := context.Background()
	req := func(key string) LogRequest {
		return LogRequest{Workload: web, Namespace: "shop", Pod: "web-1", Container: "web", Reason: "crash", Key: key}
	}
	if !c.Request(ctx, req("a")) {
		t.Fatal("the first request was refused")
	}
	c.Wait()
	if c.Request(ctx, req("a")) {
		t.Fatal("the same key was fetched twice")
	}
	if !c.Request(ctx, req("b")) || c.Request(ctx, req("c")) {
		t.Fatal("the budget of two fetches was not kept")
	}
	c.Wait()
	if c.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", c.Dropped())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Summary.Lines == 0 || got[0].Summary.SHA256 == "" || got[0].Workload != web {
		t.Fatalf("evidence = %+v", got)
	}
}
