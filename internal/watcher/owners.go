package watcher

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Owners attributes pods to the workloads that own them.
type Owners struct {
	ReplicaSets appslisters.ReplicaSetLister
}

// Resolve returns the workload that owns a pod and the pod's revision. A pod
// of a Deployment is owned through its ReplicaSet; a pod nobody owns is its
// own workload.
func (o Owners) Resolve(pod *corev1.Pod) (model.ObjectRef, string) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return model.ObjectRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, ""
	}
	switch ref.Kind {
	case "ReplicaSet":
		revision := pod.Labels["pod-template-hash"]
		if o.ReplicaSets != nil {
			if rs, err := o.ReplicaSets.ReplicaSets(pod.Namespace).Get(ref.Name); err == nil {
				if dep := metav1.GetControllerOf(rs); dep != nil && dep.Kind == "Deployment" {
					return model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: pod.Namespace, Name: dep.Name}, revision
				}
			}
		}
		return model.ObjectRef{Group: "apps", Kind: "ReplicaSet", Namespace: pod.Namespace, Name: ref.Name}, revision
	case "StatefulSet", "DaemonSet":
		return model.ObjectRef{Group: "apps", Kind: ref.Kind, Namespace: pod.Namespace, Name: ref.Name}, pod.Labels["controller-revision-hash"]
	case "Job":
		return model.ObjectRef{Group: "batch", Kind: "Job", Namespace: pod.Namespace, Name: ref.Name}, ""
	default:
		return model.ObjectRef{Kind: ref.Kind, Namespace: pod.Namespace, Name: ref.Name}, ""
	}
}
