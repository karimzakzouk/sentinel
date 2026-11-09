package watcher

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// ReasonSample marks the logs of error sampling: every SampleInterval, the
// last interval of one ready pod's logs per workload, so a workload whose
// pods stay up while it fails (its dependency gone, say) still shows its
// errors. Each error record is a log.errors fact, and the count detector
// learns every workload's usual error volume. Sampling one pod at a time
// keeps the count a per-pod rate, which scaling does not change.
const ReasonSample = "sample"

// SampleFacts turns a sample's error records into facts, one per record.
// Exception records are counted through their templates, which are marked
// as errors.
func SampleFacts(ev LogEvidence) []Fact {
	var out []Fact
	for _, t := range ev.Summary.Templates {
		if !t.Error {
			continue
		}
		for range t.Count {
			out = append(out, Fact{Kind: FactLogErrors, Workload: ev.Workload, Pod: ev.Pod, Container: ev.Container,
				Revision: ev.Revision, At: ev.CollectedAt, Detail: t.Template})
		}
	}
	return out
}

// sampleWorkloads asks for the samples that are due. Workloads' turns are
// spread over the interval. The collector queues samples and serves them
// while its budget keeps a reserve for crashes and rollouts; a sample that
// waits past its workload's next turn is replaced by the newer one, and the
// detector counts the seconds of logs read, so waiting costs knowledge, not
// accuracy. A sample the collector refuses outright is asked for again at
// the next tick.
func (w *Watcher) sampleWorkloads(ctx context.Context, now time.Time) {
	if w.Logs == nil || w.SampleInterval <= 0 {
		return
	}
	if w.sampled == nil {
		w.sampled = map[string]time.Time{}
	}
	seen := map[string]bool{}
	// A pod's first minutes are its startup, which logs warnings and errors
	// that are no rate; crash, readiness and rollout fetches cover them.
	settled := now.Add(-max(w.SampleInterval, 2*time.Minute))
	for ns, l := range w.ns {
		for ref, pods := range readyPodsByWorkload(l, ns, settled) {
			key := ref.String()
			seen[key] = true
			last, ok := w.sampled[key]
			if !ok {
				last = now.Add(-spread(key, w.SampleInterval))
				w.sampled[key] = last
			}
			if now.Sub(last) < w.SampleInterval {
				continue
			}
			request := w.Logs.Request
			if w.request != nil {
				request = w.request
			}
			p := pods[int(now.UnixNano()/int64(w.SampleInterval))%len(pods)]
			if request(ctx, LogRequest{Workload: ref, Revision: p.revision, Namespace: ns, Pod: p.pod.Name,
				Container: defaultContainer(p.pod), Since: now.Add(-w.SampleInterval), Reason: ReasonSample,
				Key: fmt.Sprintf("sample/%s/%d", p.pod.UID, now.Unix()), LowPriority: true}) {
				w.sampled[key] = now
			}
		}
	}
	for key := range w.sampled {
		if !seen[key] {
			delete(w.sampled, key)
		}
	}
}

// spread places a workload's first sample within the first interval.
func spread(key string, interval time.Duration) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(float64(interval) * float64(h.Sum32()) / (1 << 32))
}

// sampled lists the workload kinds whose error rate is a series. A Job's
// errors end with it.
var sampled = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// readyPodsByWorkload groups a namespace's ready pods whose first container
// started before settled by workload, in a stable order so sampling rotates
// through them.
func readyPodsByWorkload(l *namespaceListers, ns string, settled time.Time) map[model.ObjectRef][]readyPod {
	pods, err := l.pods.Pods(ns).List(labels.Everything())
	if err != nil {
		return nil
	}
	out := map[model.ObjectRef][]readyPod{}
	for _, p := range pods {
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || len(p.Spec.Containers) == 0 || !podReady(p) ||
			containerStarted(p).After(settled) {
			continue
		}
		workload, rev := l.owners.Resolve(p)
		if !sampled[workload.Kind] || workload.Group != "apps" {
			continue
		}
		out[workload] = append(out[workload], readyPod{pod: p, revision: rev})
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].pod.Name < list[j].pod.Name })
	}
	return out
}

// defaultContainer is the container kubectl logs reads by default: the one
// the pod's annotation names, or the first.
func defaultContainer(p *corev1.Pod) string {
	if name := p.Annotations["kubectl.kubernetes.io/default-container"]; name != "" {
		for _, c := range p.Spec.Containers {
			if c.Name == name {
				return name
			}
		}
	}
	return p.Spec.Containers[0].Name
}
