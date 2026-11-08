package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// ChangeKind classifies a change.
type ChangeKind string

const (
	ChangeTemplate  ChangeKind = "template"
	ChangeScale     ChangeKind = "scale"
	ChangeConfigMap ChangeKind = "configmap"
)

// Change is one observed change to a workload or its configuration.
type Change struct {
	ID     model.ID        `json:"id"`
	Kind   ChangeKind      `json:"kind"`
	Object model.ObjectRef `json:"object"`
	// Fields lists what changed, such as "image:web", "env:web" or "key:app.conf".
	Fields []string  `json:"fields,omitempty"`
	At     time.Time `json:"at"`
	// Actor is the field manager that last wrote the object, such as
	// kubectl-client-side-apply, helm or argocd-controller.
	Actor string `json:"actor,omitempty"`
}

const (
	annotationManager    = "sentinel.karimzakzouk.github.io/last-manager"
	annotationKeyHashes  = "sentinel.karimzakzouk.github.io/key-sha256"
	restartAnnotationKey = "kubectl.kubernetes.io/restartedAt"
)

// Transform trims objects before they enter the informer cache: it records
// the last writer, drops managedFields, and replaces ConfigMap values with
// per-key hashes, so no value is ever held in memory.
func Transform(obj any) (any, error) {
	m, ok := obj.(metav1.Object)
	if !ok {
		return obj, nil
	}
	if fields := m.GetManagedFields(); len(fields) > 0 {
		latest := fields[0]
		for _, f := range fields[1:] {
			if f.Time != nil && (latest.Time == nil || f.Time.After(latest.Time.Time)) {
				latest = f
			}
		}
		setAnnotation(m, annotationManager, latest.Manager)
		m.SetManagedFields(nil)
	}
	if cm, ok := obj.(*corev1.ConfigMap); ok {
		hashes := map[string]string{}
		for k, v := range cm.Data {
			hashes[k] = shortHash([]byte(v))
		}
		for k, v := range cm.BinaryData {
			hashes[k] = shortHash(v)
		}
		b, _ := json.Marshal(hashes)
		setAnnotation(cm, annotationKeyHashes, string(b))
		cm.Data, cm.BinaryData = nil, nil
	}
	return obj, nil
}

func setAnnotation(m metav1.Object, key, value string) {
	a := m.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[key] = value
	m.SetAnnotations(a)
}

func shortHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// DeploymentChanges returns the changes between two observations of a Deployment.
func DeploymentChanges(old, cur *appsv1.Deployment, now time.Time) []Change {
	return workloadChanges(model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: cur.Namespace, Name: cur.Name},
		cur, &old.Spec.Template, &cur.Spec.Template, old.Spec.Replicas, cur.Spec.Replicas, now)
}

// StatefulSetChanges returns the changes between two observations of a StatefulSet.
func StatefulSetChanges(old, cur *appsv1.StatefulSet, now time.Time) []Change {
	return workloadChanges(model.ObjectRef{Group: "apps", Kind: "StatefulSet", Namespace: cur.Namespace, Name: cur.Name},
		cur, &old.Spec.Template, &cur.Spec.Template, old.Spec.Replicas, cur.Spec.Replicas, now)
}

func workloadChanges(ref model.ObjectRef, cur metav1.Object, oldT, curT *corev1.PodTemplateSpec, oldR, curR *int32, now time.Time) []Change {
	actor := cur.GetAnnotations()[annotationManager]
	var out []Change
	if fields := TemplateFields(oldT, curT); len(fields) > 0 {
		out = append(out, Change{ID: model.NewID(), Kind: ChangeTemplate, Object: ref, Fields: fields, At: now, Actor: actor})
	}
	if replicas(oldR) != replicas(curR) {
		out = append(out, Change{ID: model.NewID(), Kind: ChangeScale, Object: ref, At: now, Actor: actor,
			Fields: []string{"replicas:" + itoa(replicas(oldR)) + "->" + itoa(replicas(curR))}})
	}
	return out
}

// ConfigMapChanges returns the change between two observations of a
// ConfigMap that went through Transform, naming the keys whose values changed.
func ConfigMapChanges(old, cur *corev1.ConfigMap, now time.Time) []Change {
	var before, after map[string]string
	_ = json.Unmarshal([]byte(old.Annotations[annotationKeyHashes]), &before)
	_ = json.Unmarshal([]byte(cur.Annotations[annotationKeyHashes]), &after)
	var fields []string
	for k, h := range after {
		if before[k] != h {
			fields = append(fields, "key:"+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			fields = append(fields, "key:"+k)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	sort.Strings(fields)
	ref := model.ObjectRef{Kind: "ConfigMap", Namespace: cur.Namespace, Name: cur.Name}
	return []Change{{ID: model.NewID(), Kind: ChangeConfigMap, Object: ref, Fields: fields, At: now, Actor: cur.Annotations[annotationManager]}}
}

// TemplateFields names what differs between two pod templates, such as
// "image:web" or "resources:web". It returns nil when they are equal.
func TemplateFields(old, cur *corev1.PodTemplateSpec) []string {
	set := map[string]bool{}
	add := func(changed bool, name string) {
		if changed {
			set[name] = true
		}
	}
	containerFields(old.Spec.Containers, cur.Spec.Containers, "", add)
	containerFields(old.Spec.InitContainers, cur.Spec.InitContainers, "init:", add)
	s, o := cur.Spec, old.Spec
	eq := equality.Semantic.DeepEqual
	add(!eq(o.Volumes, s.Volumes), "volumes")
	add(o.ServiceAccountName != s.ServiceAccountName, "service-account")
	add(!eq(o.SecurityContext, s.SecurityContext), "pod-security")
	add(!eq(o.NodeSelector, s.NodeSelector) || !eq(o.Affinity, s.Affinity) || !eq(o.Tolerations, s.Tolerations) ||
		!eq(o.TopologySpreadConstraints, s.TopologySpreadConstraints), "scheduling")
	add(!eq(old.Labels, cur.Labels), "template-labels")
	oldAnn, curAnn := withoutKey(old.Annotations, restartAnnotationKey), withoutKey(cur.Annotations, restartAnnotationKey)
	add(!eq(oldAnn, curAnn), "template-annotations")
	add(old.Annotations[restartAnnotationKey] != cur.Annotations[restartAnnotationKey], "restart")
	if len(set) == 0 && !eq(old, cur) {
		set["other"] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containerFields(old, cur []corev1.Container, prefix string, add func(bool, string)) {
	byName := map[string]corev1.Container{}
	for _, c := range old {
		byName[c.Name] = c
	}
	eq := equality.Semantic.DeepEqual
	for _, c := range cur {
		o, ok := byName[c.Name]
		if !ok {
			add(true, prefix+"added:"+c.Name)
			continue
		}
		delete(byName, c.Name)
		add(o.Image != c.Image, prefix+"image:"+c.Name)
		add(!eq(o.Command, c.Command) || !eq(o.Args, c.Args), prefix+"args:"+c.Name)
		add(!eq(o.Env, c.Env) || !eq(o.EnvFrom, c.EnvFrom), prefix+"env:"+c.Name)
		add(!eq(o.Resources, c.Resources), prefix+"resources:"+c.Name)
		add(!eq(o.ReadinessProbe, c.ReadinessProbe) || !eq(o.LivenessProbe, c.LivenessProbe) || !eq(o.StartupProbe, c.StartupProbe), prefix+"probes:"+c.Name)
		add(!eq(o.Ports, c.Ports), prefix+"ports:"+c.Name)
		add(!eq(o.VolumeMounts, c.VolumeMounts), prefix+"mounts:"+c.Name)
		add(!eq(o.SecurityContext, c.SecurityContext), prefix+"security:"+c.Name)
	}
	for name := range byName {
		add(true, prefix+"removed:"+name)
	}
}

func withoutKey(m map[string]string, key string) map[string]string {
	if _, ok := m[key]; !ok {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func replicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func itoa(i int32) string {
	b, _ := json.Marshal(i)
	return string(b)
}
