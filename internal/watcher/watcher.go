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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/internal/model"
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
	Log  *slog.Logger

	mu          sync.Mutex
	ns          map[string]*namespaceListers
	eventCounts map[string]int32
}

type namespaceListers struct {
	pods        corelisters.PodLister
	replicaSets appslisters.ReplicaSetLister
	statefulSet appslisters.StatefulSetLister
	owners      Owners
}

// Run starts the informers and blocks until ctx ends.
func (w *Watcher) Run(ctx context.Context) error {
	w.ns = map[string]*namespaceListers{}
	w.eventCounts = map[string]int32{}
	start := time.Now().UTC()
	var synced []cache.InformerSynced
	for _, ns := range w.Namespaces {
		f := informers.NewSharedInformerFactoryWithOptions(w.Client, 0, informers.WithNamespace(ns), informers.WithTransform(Transform))
		pods, rs, deps, sts := f.Core().V1().Pods(), f.Apps().V1().ReplicaSets(), f.Apps().V1().Deployments(), f.Apps().V1().StatefulSets()
		cms, events := f.Core().V1().ConfigMaps(), f.Core().V1().Events()
		l := &namespaceListers{pods: pods.Lister(), replicaSets: rs.Lister(), statefulSet: sts.Lister(), owners: Owners{ReplicaSets: rs.Lister()}}
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
				if d, ok := obj.(*appsv1.Deployment); ok {
					w.Detector.Forget(deploymentRef(d))
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
				if s, ok := obj.(*appsv1.StatefulSet); ok {
					w.Detector.Forget(statefulSetRef(s))
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
			sts.Informer().HasSynced, cms.Informer().HasSynced, events.Informer().HasSynced)
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
			for _, l := range w.ns {
				pods, err := l.pods.List(labels.Everything())
				if err != nil {
					continue
				}
				for _, p := range pods {
					workload, rev := l.owners.Resolve(p)
					facts := StateFacts(p, workload, rev, now)
					w.Detector.Observe(facts...)
					w.requestLogs(ctx, p, facts)
				}
			}
			for _, d := range w.Detector.Advance(now) {
				w.OnDetection(ctx, d)
			}
			if w.OnTick != nil {
				w.OnTick(ctx)
			}
		}
	}
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
