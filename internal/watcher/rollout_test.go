package watcher

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

func sample(reason, pod, rev string, exposure float64, templates ...logs.TemplateCount) LogEvidence {
	return LogEvidence{Workload: web, Pod: pod, Revision: rev, Reason: reason, CollectedAt: t0, Exposure: exposure,
		Summary: logs.Summary{Templates: templates}}
}

var (
	served   = logs.TemplateCount{Template: "GET /products <num> <duration>", Count: 100, Sample: "GET /products 200 3ms"}
	rpcError = logs.TemplateCount{Template: "request error: rpc error: code = Unavailable", Count: 30, Error: true,
		Sample: `{"severity":"error","message":"request error","error":"rpc error: code = Unavailable"}`}
)

func TestANewErrorAfterADeployIsDetected(t *testing.T) {
	r := newRollout(web, t0)
	if d, _ := r.add(sample("rollout-before", "web-old", "old", 300, served), t0); len(d) != 0 {
		t.Fatalf("a before sample alone raised %+v", d)
	}
	afterServed := served
	afterServed.Count = 20
	dets, cmp := r.add(sample("rollout-after", "web-new", "new", 60, afterServed, rpcError), t0.Add(time.Minute))
	if len(dets) != 1 || cmp == nil || cmp.tested != 1 || cmp.bestP >= cmp.alpha {
		t.Fatalf("detections = %+v (comparison %+v), want one", dets, cmp)
	}
	d := dets[0]
	if d.Kind != FactNewError || d.Count != 30 || d.Revisions["new"] != 30 || d.Sample != rpcError.Sample || d.TailProbability >= d.Alpha {
		t.Fatalf("detection = %+v", d)
	}
	if again, _ := r.add(sample("rollout-after", "web-new-2", "new", 60, rpcError), t0.Add(2*time.Minute)); len(again) != 0 {
		t.Fatalf("a rollout raised a second detection: %+v", again)
	}
	ended := r.end(t0.Add(5 * time.Minute))
	if len(ended) != 1 || !ended[0].Ended || ended[0].ID != d.ID {
		t.Fatalf("end = %+v", ended)
	}
}

func TestAnErrorAtTheOldRateIsNotNew(t *testing.T) {
	r := newRollout(web, t0)
	before := rpcError
	before.Count = 150 // the same rate over five minutes as 30 in one
	r.add(sample("rollout-before", "web-old", "old", 300, served, before), t0)
	if d, _ := r.add(sample("rollout-after", "web-new", "new", 60, rpcError), t0.Add(time.Minute)); len(d) != 0 {
		t.Fatalf("an unchanged error rate raised %+v", d)
	}
}

func TestWithoutABeforeSampleNothingIsTested(t *testing.T) {
	r := newRollout(web, t0)
	if d, _ := r.add(sample("rollout-after", "web-new", "new", 60, rpcError), t0.Add(time.Minute)); len(d) != 0 {
		t.Fatalf("no baseline, yet %+v", d)
	}
}

// A pod's samples overlap in time; adding them up would count its first
// minute twice.
func TestAPodsLatestSampleReplacesItsEarlierOne(t *testing.T) {
	r := newRollout(web, t0)
	r.add(sample("rollout-before", "web-old", "old", 300, served), t0)
	few := rpcError
	few.Count = 2
	r.add(sample("rollout-after", "web-new", "new", 60, few), t0.Add(time.Minute))
	r.add(sample("rollout-after", "web-new", "new", 180, few), t0.Add(3*time.Minute))
	if n := len(r.after); n != 1 || r.after["web-new"].Exposure != 180 {
		t.Fatalf("after samples = %+v", r.after)
	}
}

// A DaemonSet's template change is a change like a Deployment's.
func TestADaemonSetsTemplateChangeIsAChange(t *testing.T) {
	old := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "shop"},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "agent:1"}}}}}}
	cur := old.DeepCopy()
	cur.Spec.Template.Spec.Containers[0].Image = "agent:2"
	got := DaemonSetChanges(old, cur, time.Now())
	if len(got) != 1 || got[0].Kind != ChangeTemplate || got[0].Object.Kind != "DaemonSet" || got[0].Fields[0] != "image:agent" {
		t.Fatalf("changes %+v", got)
	}
}
