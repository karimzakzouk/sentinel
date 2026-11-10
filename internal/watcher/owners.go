package watcher

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Owners attributes pods to the workloads that own them.
type Owners struct {
	ReplicaSets appslisters.ReplicaSetLister
	// Deployments, when set, let a Flagger Canary that owns a Deployment
	// claim the Deployment's pods so the Brain can abort the Canary.
	Deployments appslisters.DeploymentLister
}

// Resolve returns the workload that owns a pod and the pod's revision. A pod
// of a Deployment or an Argo Rollouts Rollout is owned through its ReplicaSet;
// a Deployment owned by a Flagger Canary attributes to the Canary; a pod
// nobody owns is its own workload.
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
				if owner := metav1.GetControllerOf(rs); owner != nil {
					switch {
					case owner.Kind == "Deployment":
						if o.Deployments != nil {
							if dep, err := o.Deployments.Deployments(pod.Namespace).Get(owner.Name); err == nil {
								if canary := metav1.GetControllerOf(dep); canary != nil &&
									canary.Kind == "Canary" && strings.HasPrefix(canary.APIVersion, "flagger.app/") {
									return model.ObjectRef{Group: "flagger.app", Kind: "Canary", Namespace: pod.Namespace, Name: canary.Name}, revision
								}
							}
						}
						return model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: pod.Namespace, Name: owner.Name}, revision
					case owner.Kind == "Rollout" && strings.HasPrefix(owner.APIVersion, "argoproj.io/"):
						// Progressive delivery owns the ReplicaSet; attribute
						// the pod to the Rollout so the Brain can abort it.
						return model.ObjectRef{Group: "argoproj.io", Kind: "Rollout", Namespace: pod.Namespace, Name: owner.Name}, revision
					}
				}
			}
		}
		return model.ObjectRef{Group: "apps", Kind: "ReplicaSet", Namespace: pod.Namespace, Name: ref.Name}, revision
	case "StatefulSet", "DaemonSet":
		return model.ObjectRef{Group: "apps", Kind: ref.Kind, Namespace: pod.Namespace, Name: ref.Name}, pod.Labels["controller-revision-hash"]
	case "Rollout":
		if strings.HasPrefix(ref.APIVersion, "argoproj.io/") {
			return model.ObjectRef{Group: "argoproj.io", Kind: "Rollout", Namespace: pod.Namespace, Name: ref.Name}, pod.Labels["pod-template-hash"]
		}
		return model.ObjectRef{Kind: ref.Kind, Namespace: pod.Namespace, Name: ref.Name}, ""
	case "Job":
		return model.ObjectRef{Group: "batch", Kind: "Job", Namespace: pod.Namespace, Name: ref.Name}, ""
	default:
		return model.ObjectRef{Kind: ref.Kind, Namespace: pod.Namespace, Name: ref.Name}, ""
	}
}
