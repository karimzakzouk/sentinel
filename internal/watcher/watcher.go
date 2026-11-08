package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/topology"
)

// Watcher runs informers in the allowed namespaces, turns what they see into
// facts and changes, and ticks the detector.
type Watcher struct {
	Client      kubernetes.Interface
	Namespaces  []string
	Detector    *CountDetector
	OnChange    func(context.Context, Change)
	OnDetection func(context.Context, Detection)
	// OnTick runs after every tick's detections have been delivered.
	OnTick func(context.Context)
	// Logs, when set, fetches logs that facts call for.
	Logs *LogCollector
	// SampleInterval, when positive and Logs is set, is how often each
	// workload's errors are sampled.
	SampleInterval time.Duration
	// Metrics, when set, is polled every MetricsInterval for resource use.
	Metrics         MetricsSource
	MetricsInterval time.Duration
	Log             *slog.Logger

	metrics metricsState
	// sampled holds each workload's last error sample; only the tick loop
	// uses it.
	sampled map[string]time.Time
	// request replaces Logs.Request in tests.
	request func(context.Context, LogRequest) bool

	mu          sync.Mutex
	ns          map[string]*namespaceListers
	eventCounts map[string]int32
	rollouts    map[string]*rollout
	// hadEndpoints marks Services seen with ready endpoints.
	hadEndpoints map[types.UID]bool
}

type namespaceListers struct {
	pods        corelisters.PodLister
	replicaSets appslisters.ReplicaSetLister
	deployments appslisters.DeploymentLister
	statefulSet appslisters.StatefulSetLister
	services    corelisters.ServiceLister
	slices      discoverylisters.EndpointSliceLister
	owners      Owners
}

// Run starts the informers and blocks until ctx ends.
func (w *Watcher) Run(ctx context.Context) error {
	w.ns = map[string]*namespaceListers{}
	w.eventCounts = map[string]int32{}
	w.rollouts = map[string]*rollout{}
	w.hadEndpoints = map[types.UID]bool{}
	if w.Logs != nil {
		toBrain := w.Logs.OnEvidence
		w.Logs.OnEvidence = func(ctx context.Context, ev LogEvidence) {
			w.rolloutEvidence(ctx, ev)
			if ev.Reason == ReasonSample {
				w.Detector.Expose(ev.Workload, FactLogErrors, ev.Exposure)
				w.Detector.Observe(SampleFacts(ev)...)
			}
			toBrain(ctx, ev)
		}
	}
	start := time.Now().UTC()
	var synced []cache.InformerSynced
	for _, ns := range w.Namespaces {
		f := informers.NewSharedInformerFactoryWithOptions(w.Client, 0, informers.WithNamespace(ns), informers.WithTransform(Transform))
		pods, rs, deps, sts := f.Core().V1().Pods(), f.Apps().V1().ReplicaSets(), f.Apps().V1().Deployments(), f.Apps().V1().StatefulSets()
		cms, events := f.Core().V1().ConfigMaps(), f.Core().V1().Events()
		svcs, slices := f.Core().V1().Services(), f.Discovery().V1().EndpointSlices()
		l := &namespaceListers{pods: pods.Lister(), replicaSets: rs.Lister(), deployments: deps.Lister(), statefulSet: sts.Lister(),
			services: svcs.Lister(), slices: slices.Lister(), owners: Owners{ReplicaSets: rs.Lister()}}
		w.ns[ns] = l

		if _, err := pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj any) {
				old, cur := oldObj.(*corev1.Pod), newObj.(*corev1.Pod)
				workload, rev := l.owners.Resolve(cur)
				facts := TerminationFacts(old, cur, workload, rev, time.Now().UTC())
				w.Detector.Observe(facts...)
				w.requestLogs(ctx, cur, facts)
			},
		}); err != nil {
			return err
		}
		if _, err := deps.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) { w.Detector.Register(deploymentRef(obj.(*appsv1.Deployment))) },
			UpdateFunc: func(oldObj, newObj any) {
				w.changes(ctx, DeploymentChanges(oldObj.(*appsv1.Deployment), newObj.(*appsv1.Deployment), time.Now().UTC()))
			},
			DeleteFunc: func(obj any) {
				if d, ok := deleted(obj).(*appsv1.Deployment); ok {
					w.forget(ctx, deploymentRef(d))
				}
			},
		}); err != nil {
			return err
		}
		if _, err := sts.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) { w.Detector.Register(statefulSetRef(obj.(*appsv1.StatefulSet))) },
			UpdateFunc: func(oldObj, newObj any) {
				w.changes(ctx, StatefulSetChanges(oldObj.(*appsv1.StatefulSet), newObj.(*appsv1.StatefulSet), time.Now().UTC()))
			},
			DeleteFunc: func(obj any) {
				if s, ok := deleted(obj).(*appsv1.StatefulSet); ok {
					w.forget(ctx, statefulSetRef(s))
				}
			},
		}); err != nil {
			return err
		}
		if _, err := cms.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj any) {
				w.changes(ctx, ConfigMapChanges(oldObj.(*corev1.ConfigMap), newObj.(*corev1.ConfigMap), time.Now().UTC()))
			},
		}); err != nil {
			return err
		}
		eventHandler := func(obj any, initial bool) {
			ev, ok := obj.(*corev1.Event)
			if !ok || ev.InvolvedObject.Kind != "Pod" {
				return
			}
			w.mu.Lock()
			prev := w.eventCounts[string(ev.UID)]
			w.eventCounts[string(ev.UID)] = max(ev.Count, 1)
			w.mu.Unlock()
			// Events listed at startup, or last seen before it, already happened:
			// they set the baseline count and are not new facts.
			if initial || HistoricEvent(ev, start) {
				return
			}
			pod, err := l.pods.Pods(ev.Namespace).Get(ev.InvolvedObject.Name)
			if err != nil {
				return
			}
			workload, rev := l.owners.Resolve(pod)
			w.Detector.Observe(EventFacts(prev, ev, workload, rev, time.Now().UTC())...)
		}
		if _, err := events.Informer().AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
			AddFunc:    eventHandler,
			UpdateFunc: func(_, newObj any) { eventHandler(newObj, false) },
		}); err != nil {
			return err
		}
		f.Start(ctx.Done())
		synced = append(synced, pods.Informer().HasSynced, rs.Informer().HasSynced, deps.Informer().HasSynced,
			sts.Informer().HasSynced, cms.Informer().HasSynced, events.Informer().HasSynced,
			svcs.Informer().HasSynced, slices.Informer().HasSynced)
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return fmt.Errorf("informer caches did not sync")
	}
	w.Log.Info("watcher started", "namespaces", w.Namespaces, "window", w.Detector.Window, "tick", w.Detector.Tick, "alpha", w.Detector.Alpha())

	tick := time.NewTicker(w.Detector.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			now := time.Now().UTC()
			w.pollMetrics(ctx, now)
			for ns, l := range w.ns {
				w.Detector.Observe(w.serviceFacts(ns, l, now)...)
				pods, err := l.pods.List(labels.Everything())
				if err != nil {
					continue
				}
				for _, p := range pods {
					workload, rev := l.owners.Resolve(p)
					facts := StateFacts(p, workload, rev, now)
					w.Detector.Observe(facts...)
					w.requestLogs(ctx, p, facts)
					w.Detector.Observe(MemoryFacts(p, w.podUsage(ns, p.Name), workload, rev, now)...)
				}
			}
			w.sampleRollouts(ctx, now)
			w.sampleWorkloads(ctx, now)
			for _, d := range w.Detector.Advance(now) {
				w.OnDetection(ctx, d)
			}
			if w.OnTick != nil {
				w.OnTick(ctx)
			}
		}
	}
}

// serviceFacts reports the namespace's Services that have lost every ready
// endpoint after having had some.
func (w *Watcher) serviceFacts(ns string, l *namespaceListers, now time.Time) []Fact {
	svcs, err := l.services.Services(ns).List(labels.Everything())
	if err != nil {
		return nil
	}
	deps, _ := l.deployments.Deployments(ns).List(labels.Everything())
	sts, _ := l.statefulSet.StatefulSets(ns).List(labels.Everything())
	var out []Fact
	for _, svc := range svcs {
		if len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		slices, err := l.slices.EndpointSlices(ns).List(labels.SelectorFromSet(labels.Set{discoveryv1.LabelServiceName: svc.Name}))
		if err != nil {
			continue
		}
		ready := ReadyEndpoints(slices)
		w.mu.Lock()
		had := w.hadEndpoints[svc.UID]
		if ready > 0 {
			w.hadEndpoints[svc.UID] = true
		}
		w.mu.Unlock()
		if ready == 0 && had {
			out = append(out, ServiceFacts(svc, BackingWorkloads(svc, deps, sts), now)...)
		}
	}
	return out
}

// Topology returns the dependency map of the watched namespaces, from the
// workloads' configuration.
func (w *Watcher) Topology() *topology.Map {
	var edges []topology.Edge
	for ns, l := range w.ns {
		svcs, _ := l.services.Services(ns).List(labels.Everything())
		deps, _ := l.deployments.Deployments(ns).List(labels.Everything())
		sts, _ := l.statefulSet.StatefulSets(ns).List(labels.Everything())
		targets := map[string]topology.ServiceTarget{}
		for _, s := range svcs {
			targets[s.Name] = topology.ServiceTarget{Name: s.Name, Backing: BackingWorkloads(s, deps, sts)}
		}
		for _, d := range deps {
			edges = append(edges, topology.ConfigEdges(deploymentRef(d), d.Spec.Template, targets)...)
		}
		for _, s := range sts {
			edges = append(edges, topology.ConfigEdges(statefulSetRef(s), s.Spec.Template, targets)...)
		}
	}
	return topology.NewMap(edges)
}

// pollMetrics reads resource use when the interval has passed: each pod's
// latest usage, and each workload's totals into its history.
func (w *Watcher) pollMetrics(ctx context.Context, now time.Time) {
	m := &w.metrics
	m.mu.Lock()
	due := w.Metrics != nil && now.Sub(m.last) >= w.MetricsInterval
	if due {
		m.last = now
	}
	m.mu.Unlock()
	if !due {
		return
	}
	usage := map[string]map[string]ContainerUsage{}
	totals := map[string][2]float64{}
	for ns, l := range w.ns {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		list, err := w.Metrics.Usage(ctx, ns)
		cancel()
		if err != nil {
			w.Log.Debug("resource metrics unavailable", "namespace", ns, "error", err)
			continue
		}
		for _, u := range list {
			key := ns + "/" + u.Pod
			if usage[key] == nil {
				usage[key] = map[string]ContainerUsage{}
			}
			usage[key][u.Container] = u
			pod, err := l.pods.Pods(ns).Get(u.Pod)
			if err != nil {
				continue
			}
			workload, _ := l.owners.Resolve(pod)
			t := totals[workload.String()]
			totals[workload.String()] = [2]float64{t[0] + u.CPU, t[1] + u.Memory}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
	if m.history == nil {
		m.history = map[string]*usageHistory{}
	}
	for key, t := range totals {
		h := m.history[key]
		if h == nil {
			h = &usageHistory{}
			m.history[key] = h
		}
		h.add(t[0], t[1])
	}
}

func (w *Watcher) podUsage(ns, pod string) map[string]ContainerUsage {
	w.metrics.mu.Lock()
	defer w.metrics.mu.Unlock()
	return w.metrics.usage[ns+"/"+pod]
}

// Deviations reports how far a workload's latest resource use is from its
// usual, once there is enough history.
func (w *Watcher) Deviations(ref model.ObjectRef) []Deviation {
	w.metrics.mu.Lock()
	defer w.metrics.mu.Unlock()
	if h := w.metrics.history[ref.String()]; h != nil {
		return h.deviations()
	}
	return nil
}

// forget ends a deleted workload's episodes and rollout.
func (w *Watcher) forget(ctx context.Context, ref model.ObjectRef) {
	now := time.Now().UTC()
	ended := w.Detector.Forget(ref, now)
	w.mu.Lock()
	if r := w.rollouts[ref.String()]; r != nil {
		ended = append(ended, r.end(now)...)
		delete(w.rollouts, ref.String())
	}
	w.mu.Unlock()
	w.deliver(ctx, ended)
}

// deleted unwraps the tombstone an informer delivers when it missed a delete.
func deleted(obj any) any {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return t.Obj
	}
	return obj
}

func (w *Watcher) requestLogs(ctx context.Context, pod *corev1.Pod, facts []Fact) {
	if w.Logs == nil {
		return
	}
	for _, r := range LogRequests(pod, facts) {
		w.Logs.Request(ctx, r)
	}
}

func (w *Watcher) changes(ctx context.Context, cs []Change) {
	for _, c := range cs {
		w.OnChange(ctx, c)
		if c.Kind == ChangeTemplate {
			w.startRollout(ctx, c.Object, c.At)
		}
	}
}

// NewestRevision reports the pod revision a workload is rolling out, or ""
// when it cannot tell.
func (w *Watcher) NewestRevision(ref model.ObjectRef) string {
	l := w.ns[ref.Namespace]
	if l == nil {
		return ""
	}
	switch ref.Kind {
	case "Deployment":
		rss, err := l.replicaSets.ReplicaSets(ref.Namespace).List(labels.Everything())
		if err != nil {
			return ""
		}
		best, bestRev := "", int64(-1)
		for _, rs := range rss {
			owner := metav1.GetControllerOf(rs)
			if owner == nil || owner.Kind != "Deployment" || owner.Name != ref.Name {
				continue
			}
			rev, err := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
			if err == nil && rev > bestRev {
				best, bestRev = rs.Labels["pod-template-hash"], rev
			}
		}
		return best
	case "StatefulSet":
		sts, err := l.statefulSet.StatefulSets(ref.Namespace).Get(ref.Name)
		if err != nil {
			return ""
		}
		return sts.Status.UpdateRevision
	}
	return ""
}

func deploymentRef(d *appsv1.Deployment) model.ObjectRef {
	return model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: d.Namespace, Name: d.Name}
}

func statefulSetRef(s *appsv1.StatefulSet) model.ObjectRef {
	return model.ObjectRef{Group: "apps", Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name}
}
