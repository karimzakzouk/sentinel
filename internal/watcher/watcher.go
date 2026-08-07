package watcher

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// sentinelIncidentGVR is the GroupVersionResource for SentinelIncident CRs.
var sentinelIncidentGVR = schema.GroupVersionResource{
	Group:    "sentinel.io",
	Version:  "v1alpha1",
	Resource: "sentinelincidents",
}

// Handler is called for every new SentinelIncident CR. It receives the
// synthetic anomaly cluster (with real pod logs attached) and should run
// the reasoning pipeline. The handler is invoked in its own goroutine so
// the watch loop is not blocked.
type Handler func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string)

// k8sHelper is the subset of *k8s.Client the watcher needs. Extracted as
// an interface so tests can inject a fake without spinning up a real K8s
// API server.
type k8sHelper interface {
	PatchCRStatus(ctx context.Context, name, namespace, phase string) error
	LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error)
	FetchCrashingPodLogs(ctx context.Context, service, namespace string) (string, error)
}

// Watcher watches SentinelIncident CRs and calls the Handler for new ones.
type Watcher struct {
	k8s     k8sHelper
	dynamic dynamic.Interface
	handler Handler
}

// New creates a Watcher. The k8s.Client provides the dynamic interface used
// to list + watch the SentinelIncident CRD.
func New(k8sClient *k8s.Client, handler Handler) (*Watcher, error) {
	if k8sClient == nil {
		return nil, fmt.Errorf("nil k8s client")
	}
	return &Watcher{
		k8s:     k8sClient,
		dynamic: k8sClient.DynamicClient(),
		handler: handler,
	}, nil
}

// newForTest constructs a Watcher with explicit dependencies for unit
// testing. Not exported — production code should use New.
func newForTest(dyn dynamic.Interface, helper k8sHelper, handler Handler) *Watcher {
	return &Watcher{
		k8s:     helper,
		dynamic: dyn,
		handler: handler,
	}
}

// Run starts the watch loop. Blocks until ctx is cancelled. Uses a real
// Watch() stream; falls back to polling for 30 seconds on stream errors
// (broken connections, API server restarts, etc.).
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := w.watchStream(ctx)
		if err != nil {
			logging.Error("watcher: stream error, falling back to poll for 30s", "err", err)
			w.pollFallback(ctx, 30*time.Second)
		}
	}
}

// watchStream lists existing CRs, processes any without a status, then
// opens a Watch() stream for new events. Returns an error if either the
// list or watch call fails — the caller falls back to polling.
func (w *Watcher) watchStream(ctx context.Context) error {
	list, err := w.dynamic.Resource(sentinelIncidentGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list CRs: %w", err)
	}

	resourceVersion := list.GetResourceVersion()

	// Process any pre-existing CRs that have not been handled yet.
	for i := range list.Items {
		item := &list.Items[i]
		status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if status != "" {
			continue
		}
		w.handleItem(ctx, item)
	}

	watcher, err := w.dynamic.Resource(sentinelIncidentGVR).Watch(ctx, metav1.ListOptions{
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				continue
			}
			item, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
			if status != "" {
				continue
			}
			w.handleItem(ctx, item)
		}
	}
}

// handleItem patches the CR status to "investigating" (so the same CR is
// not re-triggered on the next list/watch) and spawns the handler goroutine.
// Pod-log fetching + workload-annotation lookup run inside the goroutine so
// the watch loop is not blocked for ~10s per CR.
func (w *Watcher) handleItem(ctx context.Context, item *unstructured.Unstructured) {
	name := item.GetName()
	namespace := item.GetNamespace()

	spec, _, _ := unstructured.NestedMap(item.Object, "spec")
	service, _ := spec["service"].(string)
	if service == "" {
		service = "unknown"
	}
	svcNs, _ := spec["namespace"].(string)
	if svcNs == "" {
		svcNs = namespace
	}
	reason, _ := spec["reason"].(string)
	if reason == "" {
		reason = "triggered by CRD"
	}
	repoFromSpec, _ := spec["repo"].(string)

	logging.Info("watcher: new SentinelIncident",
		"cr_namespace", namespace, "cr_name", name,
		"service", service, "reason", reason)

	// Patch the CR status synchronously so we don't re-trigger on the next
	// list/watch. This is the only synchronous K8s API call in the watch loop.
	if err := w.k8s.PatchCRStatus(ctx, name, namespace, "investigating"); err != nil {
		logging.Info("watcher: could not patch CR status", "err", err)
	}

	if w.handler == nil {
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Info("watcher: handler panic recovered", "r", r)
			}
		}()

		// Discover sentinel.io/* annotations from the affected workload.
		annotations, _ := w.k8s.LookupWorkloadAnnotations(ctx, service, svcNs)
		if repoFromSpec != "" {
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["sentinel.io/repo"] = repoFromSpec
		}
		if len(annotations) > 0 {
			logging.Info("watcher: discovered annotations", "annotations", annotations)
		} else {
			logging.Info("watcher: no annotations found on workload — set sentinel.io/repo + sentinel.io/branch annotations on the Deployment",
				"namespace", svcNs, "service", service)
		}

		// Fetch REAL pod logs from the crashing pods — these become the
		// evidence the investigator reads first.
		podLogs, _ := w.k8s.FetchCrashingPodLogs(ctx, service, svcNs)
		if podLogs != "" {
			logging.Info("watcher: fetched pod logs", "service", service, "chars", len(podLogs))
		}

		explanation := fmt.Sprintf("SentinelIncident CR: %s", reason)
		if podLogs != "" {
			explanation += fmt.Sprintf("\n\n## Pod Logs (last 4KB)\n%s", podLogs)
		}

		cluster := schemas.AnomalyCluster{
			ClusterID: uuid.New(),
			Service:   service,
			Namespace: svcNs,
			Anomalies: []schemas.Anomaly{
				{
					AnomalyID:   uuid.New(),
					SignalID:    uuid.New(),
					Service:     service,
					Namespace:   svcNs,
					Detector:    "crd_watcher",
					Score:       0.8,
					Dimensions:  map[string]float64{"manual": 1.0},
					Explanation: explanation,
					Annotations: annotations,
				},
			},
			Summary:     fmt.Sprintf("SentinelIncident: %s", reason),
			Severity:    schemas.SeverityWarning,
			Annotations: annotations,
		}

		w.handler(ctx, cluster, name, namespace)
	}()
}

// pollFallback lists CRs every 10 seconds for the given duration. Used when
// the Watch() stream breaks — the next loop iteration will retry Watch.
func (w *Watcher) pollFallback(ctx context.Context, duration time.Duration) {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		list, err := w.dynamic.Resource(sentinelIncidentGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			time.Sleep(15 * time.Second)
			continue
		}

		for i := range list.Items {
			item := &list.Items[i]
			status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
			if status != "" {
				continue
			}
			w.handleItem(ctx, item)
		}

		time.Sleep(10 * time.Second)
	}
}
