package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/rest"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// FactMemoryNearLimit is a container using at least memoryNearLimit of its
// memory limit, counted once per container and tick while it holds: the
// warning before an OOM kill.
const FactMemoryNearLimit FactKind = "pod.memory-near-limit"

const memoryNearLimit = 0.9

// ContainerUsage is one container's current resource use.
type ContainerUsage struct {
	Pod, Container string
	// CPU in cores, Memory as working-set bytes.
	CPU, Memory float64
}

// MetricsSource reads current resource use in a namespace.
type MetricsSource interface {
	Usage(ctx context.Context, namespace string) ([]ContainerUsage, error)
}

// APIMetrics reads the resource metrics API (metrics.k8s.io) that
// metrics-server serves.
type APIMetrics struct {
	Client rest.Interface
}

func (m APIMetrics) Usage(ctx context.Context, namespace string) ([]ContainerUsage, error) {
	raw, err := m.Client.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces", namespace, "pods").DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return parsePodMetrics(raw)
}

// parsePodMetrics reads a PodMetricsList.
func parsePodMetrics(raw []byte) ([]ContainerUsage, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Containers []struct {
				Name  string                       `json:"name"`
				Usage map[string]resource.Quantity `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode pod metrics: %w", err)
	}
	var out []ContainerUsage
	for _, p := range list.Items {
		for _, c := range p.Containers {
			cpu, mem := c.Usage["cpu"], c.Usage["memory"]
			out = append(out, ContainerUsage{Pod: p.Metadata.Name, Container: c.Name, CPU: cpu.AsApproximateFloat64(), Memory: mem.AsApproximateFloat64()})
		}
	}
	return out, nil
}

// MemoryFacts returns a near-limit fact for each of a pod's containers whose
// latest memory use is at least memoryNearLimit of its limit.
func MemoryFacts(pod *corev1.Pod, usage map[string]ContainerUsage, workload model.ObjectRef, revision string, now time.Time) []Fact {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return nil
	}
	var out []Fact
	for _, c := range pod.Spec.Containers {
		limit, ok := c.Resources.Limits[corev1.ResourceMemory]
		u, used := usage[c.Name]
		if !ok || !used || limit.IsZero() {
			continue
		}
		if share := u.Memory / limit.AsApproximateFloat64(); share >= memoryNearLimit {
			out = append(out, Fact{Kind: FactMemoryNearLimit, Workload: workload, Pod: pod.Name, Container: c.Name, Revision: revision, At: now,
				Detail: fmt.Sprintf("container %s uses %.0f%% of its %s memory limit", c.Name, 100*share, limit.String())})
		}
	}
	return out
}

// usageHistory keeps a workload's total CPU and memory over the last hour,
// one sample per metrics poll, for deviation scores.
type usageHistory struct {
	cpu, memory []float64
}

const usageHistoryLen = 120

func (h *usageHistory) add(cpu, memory float64) {
	h.cpu = append(h.cpu, cpu)
	h.memory = append(h.memory, memory)
	if len(h.cpu) > usageHistoryLen {
		h.cpu, h.memory = h.cpu[1:], h.memory[1:]
	}
}

// Deviation is how far a workload's latest resource use is from its usual,
// in robust standard deviations (median and MAD of its history).
type Deviation struct {
	Resource string
	Score    float64
}

// deviations returns the latest sample's robust deviations, once there is
// enough history to judge.
func (h *usageHistory) deviations() []Deviation {
	var out []Deviation
	for _, s := range []struct {
		name   string
		values []float64
	}{{"cpu", h.cpu}, {"memory", h.memory}} {
		if len(s.values) < 10 {
			continue
		}
		past := s.values[:len(s.values)-1]
		med := median(past)
		abs := make([]float64, len(past))
		for i, v := range past {
			abs[i] = math.Abs(v - med)
		}
		// A floor keeps a perfectly steady history from turning noise into
		// huge scores.
		scale := math.Max(1.4826*median(abs), 0.05*math.Abs(med)+1e-9)
		out = append(out, Deviation{Resource: s.name, Score: (s.values[len(s.values)-1] - med) / scale})
	}
	return out
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// metricsState is the latest usage per pod and the history per workload.
type metricsState struct {
	mu      sync.Mutex
	last    time.Time
	usage   map[string]map[string]ContainerUsage // namespace/pod → container → usage
	history map[string]*usageHistory             // workload key
}
