package watcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/stats"
)

// FactNewError is an error that a workload's new revision logs at a
// significantly higher rate than the revision it replaced.
const FactNewError FactKind = "log.new-error"

// Rollout sampling: the old revision's last minutes, before the rollout
// replaces it, against the new revision's first minutes.
const (
	rolloutWindow  = 5 * time.Minute
	rolloutPods    = 3
	rolloutTracked = 15 * time.Minute
	// rolloutRatio is the rate increase that counts as a new error: the test
	// asks whether the new rate is more than this many times the old.
	rolloutRatio = 2.0
	// rolloutAlpha is the false-alarm level per rollout, split across the
	// errors tested.
	rolloutAlpha = 1e-3
	// rolloutMinCount is the fewest occurrences worth testing.
	rolloutMinCount = 3
)

// rolloutSampleAges are the new pods' ages at which they are sampled.
var rolloutSampleAges = []time.Duration{time.Minute, 3 * time.Minute}

// rollout compares a new revision's logs with the old revision's. It raises
// at most one new-error detection, for the most significant error; its other
// errors reach the incident as evidence.
type rollout struct {
	workload model.ObjectRef
	started  time.Time
	// prefix starts the keys of this rollout's log requests.
	prefix string
	// before and after keep each pod's latest sample: samples of one pod
	// overlap in time and must not be added up.
	before, after map[string]LogEvidence
	episode       *Detection
}

func newRollout(ref model.ObjectRef, at time.Time) *rollout {
	return &rollout{workload: ref, started: at, prefix: fmt.Sprintf("rollout/%s/%d/", ref, at.UnixNano()),
		before: map[string]LogEvidence{}, after: map[string]LogEvidence{}}
}

func (r *rollout) key(phase, pod string, n int) string {
	return fmt.Sprintf("%s%s/%s/%d", r.prefix, phase, pod, n)
}

// comparison is what one comparison of a rollout's samples found.
type comparison struct {
	before, after float64 // exposure, seconds
	tested        int
	bestP, alpha  float64
}

// add takes a sample and returns the detection it completes, if any, and
// the comparison it ran.
func (r *rollout) add(ev LogEvidence, now time.Time) ([]Detection, *comparison) {
	switch ev.Reason {
	case "rollout-before":
		r.before[ev.Pod] = ev
		return nil, nil
	case "rollout-after":
		r.after[ev.Pod] = ev
	default:
		return nil, nil
	}
	if r.episode != nil {
		return nil, nil
	}
	d, cmp := r.compare(now)
	if d == nil {
		return nil, cmp
	}
	r.episode = d
	return []Detection{*d}, cmp
}

// end closes the rollout's episode, if it has one.
func (r *rollout) end(now time.Time) []Detection {
	if r.episode == nil {
		return nil
	}
	d := *r.episode
	d.Ended, d.At, d.Count = true, now, 0
	r.episode = nil
	return []Detection{d}
}

type errorCounts struct {
	before, after int
	sample        string
	revision      string
}

// compare tests each error the new revision logs: is its rate more than
// rolloutRatio times the old revision's? Bonferroni across the errors tested.
func (r *rollout) compare(now time.Time) (*Detection, *comparison) {
	cmp := &comparison{bestP: 1}
	for _, ev := range r.before {
		cmp.before += ev.Exposure
	}
	for _, ev := range r.after {
		cmp.after += ev.Exposure
	}
	t0, t1 := cmp.before, cmp.after
	if t0 <= 0 || t1 <= 0 {
		return nil, cmp
	}
	// Templates mined in separate samples are matched by mining them again
	// together; exceptions match by fingerprint.
	miner := logs.NewMiner()
	templates := map[*logs.Template]*errorCounts{}
	isError := map[*logs.Template]bool{}
	exceptions := map[string]*errorCounts{}
	count := func(c *errorCounts, n int, after bool, sample, revision string) {
		if after {
			c.after += n
			if c.sample == "" {
				c.sample, c.revision = sample, revision
			}
		} else {
			c.before += n
		}
	}
	for _, phase := range []struct {
		samples map[string]LogEvidence
		after   bool
	}{{r.before, false}, {r.after, true}} {
		for _, pod := range sortedKeys(phase.samples) {
			ev := phase.samples[pod]
			for _, t := range ev.Summary.Templates {
				tpl := miner.Add(t.Template)
				if templates[tpl] == nil {
					templates[tpl] = &errorCounts{}
				}
				isError[tpl] = isError[tpl] || t.Error
				count(templates[tpl], t.Count, phase.after, t.Sample, ev.Revision)
			}
			for _, e := range ev.Summary.Exceptions {
				if exceptions[e.Fingerprint] == nil {
					exceptions[e.Fingerprint] = &errorCounts{}
				}
				count(exceptions[e.Fingerprint], e.Count, phase.after, e.Sample, ev.Revision)
			}
		}
	}
	var tested []*errorCounts
	for tpl, c := range templates {
		if isError[tpl] && c.after >= rolloutMinCount {
			tested = append(tested, c)
		}
	}
	for _, c := range exceptions {
		if c.after >= rolloutMinCount {
			tested = append(tested, c)
		}
	}
	cmp.tested = len(tested)
	if len(tested) == 0 {
		return nil, cmp
	}
	sort.Slice(tested, func(i, j int) bool { return tested[i].sample < tested[j].sample })
	alpha := rolloutAlpha / float64(len(tested))
	cmp.alpha = alpha
	var c *errorCounts
	for _, e := range tested {
		if p := stats.RateRatioTest(e.before, t0, e.after, t1, rolloutRatio); p < cmp.bestP {
			cmp.bestP = p
			if p < alpha {
				c = e
			}
		}
	}
	if c == nil {
		return nil, cmp
	}
	bestP := cmp.bestP
	onset := now
	for _, ev := range r.after {
		if start := ev.CollectedAt.Add(-time.Duration(ev.Exposure * float64(time.Second))); start.Before(onset) {
			onset = start
		}
	}
	return &Detection{
		ID: model.NewID(), Workload: r.workload, Kind: FactNewError, Count: c.after, TailProbability: bestP,
		Alpha: alpha, Onset: onset, At: now, Revisions: map[string]int{c.revision: c.after}, Sample: c.sample,
		Window: map[FactKind]int{FactNewError: c.after},
	}, cmp
}

func sortedKeys(m map[string]LogEvidence) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// startRollout begins tracking a workload's rollout and samples the pods it
// is about to replace. A rollout still tracked for the workload ends.
func (w *Watcher) startRollout(ctx context.Context, ref model.ObjectRef, at time.Time) {
	if w.Logs == nil {
		return
	}
	r := newRollout(ref, at)
	w.mu.Lock()
	var ended []Detection
	if prev := w.rollouts[ref.String()]; prev != nil {
		ended = prev.end(at)
	}
	w.rollouts[ref.String()] = r
	w.mu.Unlock()
	w.deliver(ctx, ended)
	for _, p := range w.readyPods(ref, "") {
		w.Logs.Request(ctx, LogRequest{Workload: ref, Revision: p.revision, Namespace: ref.Namespace, Pod: p.pod.Name,
			Container: p.pod.Spec.Containers[0].Name, Since: at.Add(-rolloutWindow), Reason: "rollout-before",
			Key: r.key("before", string(p.pod.UID), 0)})
	}
}

// sampleRollouts samples new pods as they reach each sample age, and ends
// rollouts tracked long enough.
func (w *Watcher) sampleRollouts(ctx context.Context, now time.Time) {
	w.mu.Lock()
	var active []*rollout
	var ended []Detection
	for key, r := range w.rollouts {
		if now.Sub(r.started) > rolloutTracked {
			ended = append(ended, r.end(now)...)
			delete(w.rollouts, key)
			continue
		}
		active = append(active, r)
	}
	w.mu.Unlock()
	w.deliver(ctx, ended)
	for _, r := range active {
		rev := w.NewestRevision(r.workload)
		if rev == "" {
			continue
		}
		for _, p := range w.readyPods(r.workload, rev) {
			age := now.Sub(p.started)
			for i, a := range rolloutSampleAges {
				if age < a {
					continue
				}
				since := now.Add(-min(age, rolloutWindow))
				w.Logs.Request(ctx, LogRequest{Workload: r.workload, Revision: rev, Namespace: r.workload.Namespace,
					Pod: p.pod.Name, Container: p.pod.Spec.Containers[0].Name, Since: since, Reason: "rollout-after",
					Key: r.key("after", string(p.pod.UID), i)})
			}
		}
	}
}

// rolloutEvidence routes a rollout sample to its rollout.
func (w *Watcher) rolloutEvidence(ctx context.Context, ev LogEvidence) {
	if !strings.HasPrefix(ev.Reason, "rollout-") {
		return
	}
	w.mu.Lock()
	r := w.rollouts[ev.Workload.String()]
	var dets []Detection
	var cmp *comparison
	if r != nil && strings.HasPrefix(ev.key, r.prefix) {
		dets, cmp = r.add(ev, time.Now().UTC())
	}
	w.mu.Unlock()
	if cmp != nil {
		w.Log.Debug("rollout compared", "workload", ev.Workload.String(), "beforeSeconds", cmp.before, "afterSeconds", cmp.after,
			"errorsTested", cmp.tested, "bestP", cmp.bestP, "alpha", cmp.alpha, "newError", len(dets) > 0)
	} else if r == nil || !strings.HasPrefix(ev.key, r.prefix) {
		w.Log.Debug("rollout sample without its rollout", "workload", ev.Workload.String(), "pod", ev.Pod)
	}
	w.deliver(ctx, dets)
}

func (w *Watcher) deliver(ctx context.Context, dets []Detection) {
	for _, d := range dets {
		w.OnDetection(ctx, d)
	}
}

type readyPod struct {
	pod      *corev1.Pod
	revision string
	started  time.Time
}

// readyPods returns up to rolloutPods ready pods of a workload, of one
// revision or of any, oldest first.
func (w *Watcher) readyPods(ref model.ObjectRef, revision string) []readyPod {
	l := w.ns[ref.Namespace]
	if l == nil {
		return nil
	}
	pods, err := l.pods.Pods(ref.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var out []readyPod
	for _, p := range pods {
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || len(p.Spec.Containers) == 0 || !podReady(p) {
			continue
		}
		workload, rev := l.owners.Resolve(p)
		if workload != ref || revision != "" && rev != revision {
			continue
		}
		out = append(out, readyPod{pod: p, revision: rev, started: containerStarted(p)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].started.Before(out[j].started) })
	return out[:min(len(out), rolloutPods)]
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// containerStarted is when the pod's first container last started.
func containerStarted(p *corev1.Pod) time.Time {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == p.Spec.Containers[0].Name && cs.State.Running != nil {
			return cs.State.Running.StartedAt.Time
		}
	}
	return p.CreationTimestamp.Time
}
