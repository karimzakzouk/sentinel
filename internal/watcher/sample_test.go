package watcher

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

func TestSampleFactsCountErrorRecords(t *testing.T) {
	ev := LogEvidence{Workload: web, Pod: "web-1", Container: "web", Revision: "abc", Reason: ReasonSample, CollectedAt: t0,
		Summary: logs.Summary{Lines: 60, Templates: []logs.TemplateCount{
			{Template: "GET <*> 200", Count: 50},
			{Template: "redis timeout after <*>", Count: 3, Error: true},
			{Template: "Unhandled exception", Count: 2, Error: true},
		}}}
	facts := SampleFacts(ev)
	if len(facts) != 5 {
		t.Fatalf("facts = %d, want one per error record (5)", len(facts))
	}
	for _, f := range facts {
		if f.Kind != FactLogErrors || f.Workload != web || f.Revision != "abc" || f.At != t0 || f.Detail == "" {
			t.Fatalf("fact = %+v", f)
		}
	}
}

func TestSamplesWaitForBudgetAndLeaveAReserve(t *testing.T) {
	var mu sync.Mutex
	served := 0
	c := NewLogCollector(fake.NewClientset(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, LogEvidence) {
		mu.Lock()
		served++
		mu.Unlock()
	})
	c.Limiter = rate.NewLimiter(rate.Every(time.Hour), 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sample := func(workload, key string) LogRequest {
		ref := web
		ref.Name = workload
		return LogRequest{Workload: ref, Namespace: "shop", Pod: workload + "-1", Container: "web", Reason: ReasonSample,
			Key: key, LowPriority: true}
	}
	for i := range 12 {
		name := fmt.Sprintf("w%d", i)
		if !c.Request(ctx, sample(name, name)) {
			t.Fatal("a sample was refused instead of queued")
		}
	}
	servedNow := func() int {
		mu.Lock()
		defer mu.Unlock()
		return served
	}
	want := c.Limiter.Burst() - lowPriorityReserve
	for deadline := time.Now().Add(3 * time.Second); servedNow() < want && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(3 * drainEvery)
	c.Wait()
	if servedNow() != want || c.Queued() != 12-want {
		t.Fatalf("served %d and %d wait, want %d served: the budget's all but its reserve", servedNow(), c.Queued(), want)
	}
	if !c.Request(ctx, sample("w11", "w11-later")) || c.Queued() != 12-want || c.Replaced() != 1 {
		t.Fatalf("a newer sample of a waiting workload: %d wait, %d replaced; want it to replace the older one",
			c.Queued(), c.Replaced())
	}
	if !c.Request(ctx, LogRequest{Workload: web, Namespace: "shop", Pod: "web-1", Container: "web", Reason: "crash", Key: "crash"}) {
		t.Fatal("a crash fetch found no budget left after samples")
	}
	c.Wait()
}

// sampleWatcher returns a watcher over namespace shop with the web
// Deployment's ready pods, and the log requests it makes.
func sampleWatcher(t *testing.T, pods ...*corev1.Pod) (*Watcher, *[]LogRequest) {
	t.Helper()
	podIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	rsIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	controller := true
	if err := rsIdx.Add(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &controller}}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range pods {
		if err := podIdx.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	rs := appslisters.NewReplicaSetLister(rsIdx)
	var requests []LogRequest
	w := &Watcher{
		SampleInterval: time.Minute,
		ns:             map[string]*namespaceListers{"shop": {pods: corelisters.NewPodLister(podIdx), replicaSets: rs, owners: Owners{ReplicaSets: rs}}},
		Logs:           &LogCollector{},
	}
	w.request = func(_ context.Context, r LogRequest) bool {
		requests = append(requests, r)
		return true
	}
	return w, &requests
}

func readyWebPod(name string, ready bool) *corev1.Pod {
	controller := true
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name),
			Labels:          map[string]string{"pod-template-hash": "abc"},
			Annotations:     map[string]string{"kubectl.kubernetes.io/default-container": "web"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy"}, {Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

func TestEachWorkloadIsSampledOncePerIntervalFromItsReadyPods(t *testing.T) {
	w, requests := sampleWatcher(t, readyWebPod("web-1", true), readyWebPod("web-2", true), readyWebPod("web-3", false))
	ctx := context.Background()
	now := t0
	for range 20 * 60 / 15 { // twenty minutes of 15-second ticks
		w.sampleWorkloads(ctx, now)
		now = now.Add(15 * time.Second)
	}
	if n := len(*requests); n < 19 || n > 20 {
		t.Fatalf("%d samples in twenty minutes, want one a minute", n)
	}
	pods := map[string]int{}
	for i, r := range *requests {
		pods[r.Pod]++
		if r.Workload != web || r.Container != "web" || r.Reason != ReasonSample || !r.LowPriority || r.Revision != "abc" {
			t.Fatalf("request = %+v", r)
		}
		if i > 0 {
			if gap := r.Since.Sub((*requests)[i-1].Since); gap != time.Minute {
				t.Fatalf("samples %v apart, want a minute", gap)
			}
		}
	}
	if pods["web-3"] != 0 || pods["web-1"] == 0 || pods["web-2"] == 0 {
		t.Fatalf("pods sampled = %v, want both ready pods in turn and never the unready one", pods)
	}
}

func TestARefusedSampleIsAskedForAgain(t *testing.T) {
	w, requests := sampleWatcher(t, readyWebPod("web-1", true))
	refuse := true
	w.request = func(_ context.Context, r LogRequest) bool {
		*requests = append(*requests, r)
		return !refuse
	}
	ctx := context.Background()
	now := t0
	for range 8 { // two minutes: the first turn comes and is refused
		w.sampleWorkloads(ctx, now)
		now = now.Add(15 * time.Second)
	}
	asked := len(*requests)
	if asked == 0 {
		t.Fatal("no sample was asked for")
	}
	refuse = false
	w.sampleWorkloads(ctx, now)
	if len(*requests) != asked+1 {
		t.Fatal("a refused sample was not asked for at the next tick")
	}
	w.sampleWorkloads(ctx, now.Add(15*time.Second))
	if len(*requests) != asked+1 {
		t.Fatal("a served sample was asked for again within the interval")
	}
}
