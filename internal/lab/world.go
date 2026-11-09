package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// ReadWorld reads what the Lab needs to copy target from production
// namespace ns: its workloads, with target's template at revision when one
// is named, their Services and the ConfigMaps their templates name, and
// the image digests their pods run. It reads no Secret.
func ReadWorld(ctx context.Context, client kubernetes.Interface, ns string, target model.ObjectRef, revision string) (World, error) {
	w := World{Namespace: ns, Workloads: map[model.ObjectRef]Workload{}, ConfigMaps: map[string]*corev1.ConfigMap{}, Digests: map[string]string{}}
	deps, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return w, fmt.Errorf("list Deployments: %w", err)
	}
	for _, d := range deps.Items {
		ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: ns, Name: d.Name}
		w.Workloads[ref] = Workload{Ref: ref, Template: d.Spec.Template, Replicas: ptr.Deref(d.Spec.Replicas, 1), Claims: claims(d.Spec.Template.Spec)}
	}
	sets, err := client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return w, fmt.Errorf("list StatefulSets: %w", err)
	}
	for _, s := range sets.Items {
		ref := model.ObjectRef{Group: "apps", Kind: "StatefulSet", Namespace: ns, Name: s.Name}
		w.Workloads[ref] = Workload{Ref: ref, Template: s.Spec.Template, Replicas: ptr.Deref(s.Spec.Replicas, 1),
			Claims: len(s.Spec.VolumeClaimTemplates) > 0 || claims(s.Spec.Template.Spec)}
	}
	wl, ok := w.Workloads[target]
	if !ok {
		return w, fmt.Errorf("%s not found", target)
	}
	if revision != "" {
		tpl, err := revisionTemplate(ctx, client, target, revision)
		if err != nil {
			return w, err
		}
		wl.Template = tpl
		wl.Claims = wl.Claims || claims(tpl.Spec)
		w.Workloads[target] = wl
	}
	svcs, err := client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return w, fmt.Errorf("list Services: %w", err)
	}
	w.Services = svcs.Items
	for _, wl := range w.Workloads {
		for _, cm := range configMapsOf(wl.Template.Spec) {
			if _, done := w.ConfigMaps[cm.name]; done {
				continue
			}
			got, err := client.CoreV1().ConfigMaps(ns).Get(ctx, cm.name, metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(err):
				continue
			case err != nil:
				return w, fmt.Errorf("read ConfigMap %s: %w", cm.name, err)
			}
			w.ConfigMaps[cm.name] = got
		}
	}
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return w, fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		for k, v := range digests(pod) {
			w.Digests[k] = v
		}
	}
	return w, nil
}

// revisionTemplate reads a workload's template at a revision: a
// Deployment's ReplicaSet or a StatefulSet's ControllerRevision, which must
// belong to it.
func revisionTemplate(ctx context.Context, client kubernetes.Interface, target model.ObjectRef, revision string) (corev1.PodTemplateSpec, error) {
	owned := func(meta metav1.ObjectMeta) bool {
		for _, o := range meta.OwnerReferences {
			if o.Kind == target.Kind && o.Name == target.Name {
				return true
			}
		}
		return false
	}
	switch target.Kind {
	case "Deployment":
		rs, err := client.AppsV1().ReplicaSets(target.Namespace).Get(ctx, revision, metav1.GetOptions{})
		if err != nil {
			return corev1.PodTemplateSpec{}, fmt.Errorf("read revision %s: %w", revision, err)
		}
		if !owned(rs.ObjectMeta) {
			return corev1.PodTemplateSpec{}, fmt.Errorf("revision %s does not belong to %s", revision, target)
		}
		tpl := *rs.Spec.Template.DeepCopy()
		delete(tpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		return tpl, nil
	case "StatefulSet":
		cr, err := client.AppsV1().ControllerRevisions(target.Namespace).Get(ctx, revision, metav1.GetOptions{})
		if err != nil {
			return corev1.PodTemplateSpec{}, fmt.Errorf("read revision %s: %w", revision, err)
		}
		if !owned(cr.ObjectMeta) {
			return corev1.PodTemplateSpec{}, fmt.Errorf("revision %s does not belong to %s", revision, target)
		}
		var patch struct {
			Spec struct {
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(cr.Data.Raw, &patch); err != nil {
			return corev1.PodTemplateSpec{}, fmt.Errorf("read revision %s: %w", revision, err)
		}
		return patch.Spec.Template, nil
	}
	return corev1.PodTemplateSpec{}, fmt.Errorf("the Lab copies Deployments and StatefulSets, not %s", target.Kind)
}

// PreviousTemplate reads the workload's revision before the failing one,
// or before its newest when revision is empty: the first fix the Lab
// tries. It says which revision that is.
func PreviousTemplate(ctx context.Context, client kubernetes.Interface, target model.ObjectRef, revision string) (corev1.PodTemplateSpec, string, error) {
	type rev struct {
		name   string
		number int64
		tpl    corev1.PodTemplateSpec
	}
	var revs []rev
	owned := func(meta metav1.ObjectMeta) bool {
		for _, o := range meta.OwnerReferences {
			if o.Kind == target.Kind && o.Name == target.Name {
				return true
			}
		}
		return false
	}
	switch target.Kind {
	case "Deployment":
		list, err := client.AppsV1().ReplicaSets(target.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return corev1.PodTemplateSpec{}, "", fmt.Errorf("list revisions: %w", err)
		}
		for _, rs := range list.Items {
			n, err := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
			if err != nil || !owned(rs.ObjectMeta) {
				continue
			}
			tpl := *rs.Spec.Template.DeepCopy()
			delete(tpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
			revs = append(revs, rev{rs.Name, n, tpl})
		}
	case "StatefulSet":
		list, err := client.AppsV1().ControllerRevisions(target.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return corev1.PodTemplateSpec{}, "", fmt.Errorf("list revisions: %w", err)
		}
		for _, cr := range list.Items {
			if !owned(cr.ObjectMeta) {
				continue
			}
			tpl, err := revisionTemplate(ctx, client, target, cr.Name)
			if err != nil {
				return corev1.PodTemplateSpec{}, "", err
			}
			revs = append(revs, rev{cr.Name, cr.Revision, tpl})
		}
	default:
		return corev1.PodTemplateSpec{}, "", fmt.Errorf("the Lab copies Deployments and StatefulSets, not %s", target.Kind)
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].number > revs[j].number })
	failing := 0
	if revision != "" {
		failing = slices.IndexFunc(revs, func(r rev) bool { return r.name == revision })
		if failing < 0 {
			return corev1.PodTemplateSpec{}, "", fmt.Errorf("revision %s does not belong to %s", revision, target)
		}
	}
	if failing+1 >= len(revs) {
		return corev1.PodTemplateSpec{}, "", fmt.Errorf("%s has no revision before the failing one", target)
	}
	return revs[failing+1].tpl, revs[failing+1].name, nil
}

func claims(spec corev1.PodSpec) bool {
	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			return true
		}
	}
	return false
}

// digests maps each image a pod's spec names to the digest-pinned
// reference its container runs, when the runtime reports one.
func digests(pod corev1.Pod) map[string]string {
	images := map[string]string{}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		images[c.Name] = c.Image
	}
	out := map[string]string{}
	for _, s := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		id := strings.TrimPrefix(s.ImageID, "docker-pullable://")
		if image, ok := images[s.Name]; ok && strings.Contains(id, "@sha256:") && !strings.HasPrefix(id, "sha256:") {
			out[image] = id
		}
	}
	return out
}
