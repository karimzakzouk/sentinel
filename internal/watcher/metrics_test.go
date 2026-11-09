package watcher

import (
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParsePodMetrics(t *testing.T) {
	raw := []byte(`{"kind":"PodMetricsList","items":[{"metadata":{"name":"cart-1"},"containers":[
		{"name":"server","usage":{"cpu":"250m","memory":"80Mi"}},
		{"name":"sidecar","usage":{"cpu":"1234567n","memory":"1Gi"}}]}]}`)
	got, err := parsePodMetrics(raw)
	if err != nil || len(got) != 2 {
		t.Fatalf("parsed %+v, %v", got, err)
	}
	if got[0].Pod != "cart-1" || got[0].Container != "server" || got[0].CPU != 0.25 || got[0].Memory != 80*1024*1024 {
		t.Fatalf("server = %+v", got[0])
	}
	if math.Abs(got[1].CPU-0.001234567) > 1e-12 || got[1].Memory != 1<<30 {
		t.Fatalf("sidecar = %+v", got[1])
	}
}

func TestMemoryNearItsLimitIsAFact(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog-1"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "server", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Mi")}}},
			{Name: "unbounded"},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	near := map[string]ContainerUsage{"server": {Memory: 7.6 * 1024 * 1024}, "unbounded": {Memory: 1 << 30}}
	facts := MemoryFacts(pod, near, web, "rev", t0)
	if len(facts) != 1 || facts[0].Kind != FactMemoryNearLimit || facts[0].Container != "server" {
		t.Fatalf("facts = %+v", facts)
	}
	if f := MemoryFacts(pod, map[string]ContainerUsage{"server": {Memory: 4 * 1024 * 1024}}, web, "rev", t0); len(f) != 0 {
		t.Fatalf("half the limit raised %+v", f)
	}
	if f := MemoryFacts(pod, nil, web, "rev", t0); len(f) != 0 {
		t.Fatalf("no usage raised %+v", f)
	}
}

func TestDeviationsNeedHistoryAndScoreTheLatestSample(t *testing.T) {
	var h usageHistory
	for i := range 9 {
		h.add(0.1+0.001*float64(i%3), 100e6)
	}
	if d := h.deviations(); len(d) != 0 {
		t.Fatalf("judged with too little history: %+v", d)
	}
	h.add(0.5, 100e6) // CPU jumps fivefold; memory holds
	d := h.deviations()
	if len(d) != 2 || d[0].Resource != "cpu" || d[0].Score < 10 || math.Abs(d[1].Score) > 1 {
		t.Fatalf("deviations = %+v", d)
	}
	for range 200 {
		h.add(0.1, 100e6)
	}
	if len(h.cpu) != usageHistoryLen {
		t.Fatalf("history length %d, want the bound of %d", len(h.cpu), usageHistoryLen)
	}
}

// What a workload used while healthy leaves out the revision that failed,
// and only the latest samples are kept.
func TestMemoryUseLeavesOutTheFailedRevision(t *testing.T) {
	w := &Watcher{}
	for i := range memorySamples + 10 {
		w.metrics.addMemory(web.String(), "web", "good", float64(i))
	}
	w.metrics.addMemory(web.String(), "web", "bad", 1)
	use := w.MemoryUse(web, "bad")["web"]
	if len(use) != memorySamples-1 || use[0] != 11 {
		t.Fatalf("kept %d samples from %v; want %d from 11", len(use), use[0], memorySamples-1)
	}
	if all := w.MemoryUse(web, "")["web"]; len(all) != memorySamples {
		t.Fatalf("kept %d samples, want %d", len(all), memorySamples)
	}
}
