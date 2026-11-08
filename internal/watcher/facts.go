// Package watcher collects Kubernetes facts and changes from the API server.
// It reads only: informers, listers and logs, never writes.
package watcher

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// FactKind names an observable Kubernetes fact.
type FactKind string

// Events are counted once per occurrence.
const (
	FactOOMKilled     FactKind = "pod.oom-killed"
	FactRestarted     FactKind = "pod.restarted"
	FactProbeFailed   FactKind = "pod.probe-failed"
	FactUnschedulable FactKind = "pod.unschedulable"
)

// States are counted once per pod and tick while they hold, because a single
// transition would undercount a pod that stays stuck.
const (
	FactImagePullError FactKind = "pod.image-pull-error"
	FactCrashLoop      FactKind = "pod.crash-loop"
	FactConfigError    FactKind = "pod.config-error"
	FactNotReady       FactKind = "pod.not-ready"
)

// FactKinds lists every kind, in a stable order.
var FactKinds = []FactKind{
	FactOOMKilled, FactRestarted, FactProbeFailed, FactUnschedulable,
	FactImagePullError, FactCrashLoop, FactConfigError, FactNotReady,
}

// Fact is one observation about a pod, attributed to the workload that owns it.
type Fact struct {
	Kind      FactKind
	Workload  model.ObjectRef
	Pod       string
	Container string
	// Revision is the pod's template hash, which tells new pods from old ones
	// during a rollout.
	Revision string
	At       time.Time
	// Detail is text written by the workload or the kubelet. It is untrusted.
	Detail string
}

// TerminationFacts compares two observations of a pod and returns one fact per
// container restart that happened in between. Restarts that happened before
// Sentinel first saw the pod are not counted, and resyncs never double-count,
// because only increases of the restart counter produce facts.
func TerminationFacts(old, cur *corev1.Pod, workload model.ObjectRef, revision string, now time.Time) []Fact {
	if old == nil || cur == nil {
		return nil
	}
	before := map[string]int32{}
	for _, cs := range append(old.Status.InitContainerStatuses, old.Status.ContainerStatuses...) {
		before[cs.Name] = cs.RestartCount
	}
	var out []Fact
	for _, cs := range append(cur.Status.InitContainerStatuses, cur.Status.ContainerStatuses...) {
		prev, seen := before[cs.Name]
		if !seen || cs.RestartCount <= prev {
			continue
		}
		kind, detail := FactRestarted, ""
		if t := cs.LastTerminationState.Terminated; t != nil {
			detail = fmt.Sprintf("exit code %d, reason %s", t.ExitCode, t.Reason)
			if t.Reason == "OOMKilled" {
				kind = FactOOMKilled
			}
		}
		for range cs.RestartCount - prev {
			out = append(out, Fact{Kind: kind, Workload: workload, Pod: cur.Name, Container: cs.Name, Revision: revision, At: now, Detail: detail})
		}
	}
	return out
}

// StateFacts returns the state facts that hold for a pod right now.
func StateFacts(pod *corev1.Pod, workload model.ObjectRef, revision string, now time.Time) []Fact {
	if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return nil
	}
	fact := func(kind FactKind, container, detail string) Fact {
		return Fact{Kind: kind, Workload: workload, Pod: pod.Name, Container: container, Revision: revision, At: now, Detail: detail}
	}
	var out []Fact
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			out = append(out, fact(FactUnschedulable, "", c.Message))
		}
	}
	restarts := pod.Spec.RestartPolicy != corev1.RestartPolicyNever
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		// During a restart back-off, recent Kubernetes versions report the
		// container as terminated rather than waiting with CrashLoopBackOff.
		if t := cs.State.Terminated; t != nil && restarts && t.ExitCode != 0 {
			out = append(out, fact(FactCrashLoop, cs.Name, fmt.Sprintf("exited with code %d (%s), waiting to restart", t.ExitCode, t.Reason)))
			continue
		}
		w := cs.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull":
			out = append(out, fact(FactImagePullError, cs.Name, w.Reason+": "+w.Message))
		case "CrashLoopBackOff":
			out = append(out, fact(FactCrashLoop, cs.Name, w.Message))
		case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
			out = append(out, fact(FactConfigError, cs.Name, w.Reason+": "+w.Message))
		}
	}
	if notReadyPastGrace(pod, now) {
		out = append(out, fact(FactNotReady, "", "running but not ready"))
	}
	return out
}

// notReadyPastGrace reports a running pod that is not ready although every
// container has been running longer than its own readiness grace period:
// initial delay plus failure threshold times period. Pods still starting up
// are not counted, so a normal rollout does not look like an incident.
func notReadyPastGrace(pod *corev1.Pod, now time.Time) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return false
		}
	}
	grace := map[string]time.Duration{}
	for _, c := range pod.Spec.Containers {
		grace[c.Name] = readinessGrace(c.ReadinessProbe)
	}
	for _, cs := range pod.Status.ContainerStatuses {
		r := cs.State.Running
		if r == nil || now.Sub(r.StartedAt.Time) < grace[cs.Name] {
			return false
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}

func readinessGrace(p *corev1.Probe) time.Duration {
	if p == nil {
		return 0
	}
	period := p.PeriodSeconds
	if period == 0 {
		period = 10
	}
	threshold := p.FailureThreshold
	if threshold == 0 {
		threshold = 3
	}
	return time.Duration(p.InitialDelaySeconds+threshold*period) * time.Second
}

// HistoricEvent reports an event whose last occurrence came before start.
func HistoricEvent(ev *corev1.Event, start time.Time) bool {
	last := ev.LastTimestamp.Time
	if ev.EventTime.After(last) {
		last = ev.EventTime.Time
	}
	if ev.Series != nil && ev.Series.LastObservedTime.After(last) {
		last = ev.Series.LastObservedTime.Time
	}
	return !last.IsZero() && last.Before(start)
}

// EventFacts turns a Kubernetes event about a pod into facts. Events are
// aggregated by the API server, so the count since the last observation is
// what is new. Event counts are lower bounds: Kubernetes thins them.
func EventFacts(prevCount int32, ev *corev1.Event, workload model.ObjectRef, revision string, now time.Time) []Fact {
	if ev.InvolvedObject.Kind != "Pod" || ev.Type != corev1.EventTypeWarning {
		return nil
	}
	var kind FactKind
	switch ev.Reason {
	case "Unhealthy":
		kind = FactProbeFailed
	default:
		return nil
	}
	count := max(ev.Count, 1)
	if ev.Series != nil {
		count = max(count, ev.Series.Count)
	}
	n := count - prevCount
	out := make([]Fact, 0, max(n, 0))
	for range n {
		out = append(out, Fact{Kind: kind, Workload: workload, Pod: ev.InvolvedObject.Name, Revision: revision, At: now, Detail: ev.Message})
	}
	return out
}
