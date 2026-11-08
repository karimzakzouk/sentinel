package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/redact"
)

// LogRequest asks for one container's logs.
type LogRequest struct {
	Workload  model.ObjectRef
	Revision  string
	Namespace string
	Pod       string
	Container string
	// Previous asks for the last terminated container's logs, which the
	// kubelet keeps only until the next restart.
	Previous bool
	Reason   string
	// Key identifies the request; the same key is fetched once.
	Key string
}

// LogEvidence is a fetched log, summarized and redacted.
type LogEvidence struct {
	Workload    model.ObjectRef `json:"workload"`
	Revision    string          `json:"revision,omitempty"`
	Pod         string          `json:"pod"`
	Container   string          `json:"container"`
	Previous    bool            `json:"previous,omitempty"`
	Reason      string          `json:"reason"`
	CollectedAt time.Time       `json:"collectedAt"`
	Bytes       int             `json:"bytes"`
	// Truncated reports a fetch that reached its line or byte limit, so
	// older lines were not seen.
	Truncated bool         `json:"truncated,omitempty"`
	Summary   logs.Summary `json:"summary"`
}

// LogCollector fetches container logs within a budget and passes on their
// summaries. It never blocks the watcher: requests over budget are dropped
// and counted, and a later trigger may ask again.
type LogCollector struct {
	Client   kubernetes.Interface
	Redactor *redact.Redactor
	// Limiter bounds fetches across the cluster.
	Limiter     *rate.Limiter
	Concurrency int
	TailLines   int64
	LimitBytes  int64
	Timeout     time.Duration
	OnEvidence  func(context.Context, LogEvidence)
	Log         *slog.Logger

	mu        sync.Mutex
	seen      map[string]time.Time
	inflight  int
	dropped   int
	forbidden map[string]bool
	wg        sync.WaitGroup
}

// NewLogCollector returns a collector with the budget of the phase 1 plan:
// 30 fetches a minute, two at a time, 300 lines and 64 KiB each.
func NewLogCollector(client kubernetes.Interface, log *slog.Logger, onEvidence func(context.Context, LogEvidence)) *LogCollector {
	return &LogCollector{
		Client: client, Redactor: redact.New(), Limiter: rate.NewLimiter(rate.Every(2*time.Second), 10),
		Concurrency: 2, TailLines: 300, LimitBytes: 64 << 10, Timeout: 20 * time.Second,
		OnEvidence: onEvidence, Log: log,
	}
}

// Request starts a fetch unless the key was fetched before, the namespace
// denies logs, or the budget is spent. It reports whether a fetch started.
func (c *LogCollector) Request(ctx context.Context, r LogRequest) bool {
	now := time.Now()
	c.mu.Lock()
	if c.seen == nil {
		c.seen, c.forbidden = map[string]time.Time{}, map[string]bool{}
	}
	if _, done := c.seen[r.Key]; done || c.forbidden[r.Namespace] {
		c.mu.Unlock()
		return false
	}
	if c.inflight >= c.Concurrency || !c.Limiter.AllowN(now, 1) {
		c.dropped++
		c.mu.Unlock()
		return false
	}
	c.seen[r.Key] = now
	for k, t := range c.seen {
		if now.Sub(t) > time.Hour {
			delete(c.seen, k)
		}
	}
	c.inflight++
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.inflight--
			c.mu.Unlock()
			c.wg.Done()
		}()
		c.fetch(ctx, r)
	}()
	return true
}

// Dropped reports how many requests the budget refused.
func (c *LogCollector) Dropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Wait blocks until fetches in flight finish.
func (c *LogCollector) Wait() { c.wg.Wait() }

func (c *LogCollector) fetch(ctx context.Context, r LogRequest) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	opts := &corev1.PodLogOptions{Container: r.Container, Previous: r.Previous, Timestamps: true,
		TailLines: &c.TailLines, LimitBytes: &c.LimitBytes}
	raw, err := c.Client.CoreV1().Pods(r.Namespace).GetLogs(r.Pod, opts).DoRaw(ctx)
	if err != nil {
		if apierrors.IsForbidden(err) {
			c.mu.Lock()
			first := !c.forbidden[r.Namespace]
			c.forbidden[r.Namespace] = true
			c.mu.Unlock()
			if first {
				c.Log.Warn("container logs are forbidden; log evidence is off for the namespace", "namespace", r.Namespace)
			}
			return
		}
		c.Log.Debug("log fetch failed", "pod", r.Namespace+"/"+r.Pod, "container", r.Container, "previous", r.Previous, "error", err)
		return
	}
	s := logs.Summarize(raw, c.Redactor)
	c.OnEvidence(ctx, LogEvidence{
		Workload: r.Workload, Revision: r.Revision, Pod: r.Pod, Container: r.Container, Previous: r.Previous,
		Reason: r.Reason, CollectedAt: time.Now().UTC(), Bytes: len(raw),
		Truncated: int64(len(raw)) >= c.LimitBytes || int64(s.Lines) >= c.TailLines, Summary: s,
	})
}

// LogRequests returns the log fetches a pod's facts call for: the previous
// container after a restart or while it crash-loops, and the current one
// while the pod is not ready past its grace period.
func LogRequests(pod *corev1.Pod, facts []Fact) []LogRequest {
	var out []LogRequest
	statuses := map[string]corev1.ContainerStatus{}
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		statuses[cs.Name] = cs
	}
	base := func(f Fact, container string) LogRequest {
		return LogRequest{Workload: f.Workload, Revision: f.Revision, Namespace: pod.Namespace, Pod: pod.Name, Container: container}
	}
	for _, f := range facts {
		switch f.Kind {
		case FactRestarted, FactOOMKilled, FactCrashLoop:
			cs, ok := statuses[f.Container]
			if !ok {
				continue
			}
			// The kubelet serves a crashed instance's logs as the current ones
			// while it waits to restart, and as the previous ones after.
			r := base(f, f.Container)
			var crashed *corev1.ContainerStateTerminated
			switch {
			case cs.State.Terminated != nil:
				crashed = cs.State.Terminated
			case cs.LastTerminationState.Terminated != nil:
				crashed, r.Previous = cs.LastTerminationState.Terminated, true
			default:
				continue
			}
			instance := crashed.ContainerID
			if instance == "" {
				instance = fmt.Sprint(cs.RestartCount)
			}
			r.Reason = "crash"
			r.Key = fmt.Sprintf("%s/%s/%s", pod.UID, f.Container, instance)
			out = append(out, r)
		case FactNotReady:
			since := readySince(pod)
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Ready {
					continue
				}
				r := base(f, cs.Name)
				r.Reason = "not-ready"
				r.Key = fmt.Sprintf("%s/%s/not-ready/%d", pod.UID, cs.Name, since.Unix())
				out = append(out, r)
			}
		}
	}
	return out
}

func readySince(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}
