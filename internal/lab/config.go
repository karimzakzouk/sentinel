package lab

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// ConfigRevert is the failing copy with each container's command,
// arguments and environment taken from the previous revision's copy: its
// configuration reverted, its image and everything else kept. Raced beside
// the previous revision, it tells a fix in configuration from one in the
// image. It reports false when the two copies' containers differ in name or
// order, or agree in their configuration.
func ConfigRevert(failing, previous *corev1.Pod) (*corev1.Pod, bool) {
	out := failing.DeepCopy()
	changed := false
	for _, pair := range [][2][]corev1.Container{
		{out.Spec.InitContainers, previous.Spec.InitContainers},
		{out.Spec.Containers, previous.Spec.Containers},
	} {
		cur, prev := pair[0], pair[1]
		if len(cur) != len(prev) {
			return nil, false
		}
		for i := range cur {
			p := prev[i].DeepCopy()
			if cur[i].Name != p.Name {
				return nil, false
			}
			if !equality.Semantic.DeepEqual([]any{cur[i].Command, cur[i].Args, cur[i].Env, cur[i].EnvFrom},
				[]any{p.Command, p.Args, p.Env, p.EnvFrom}) {
				changed = true
			}
			cur[i].Command, cur[i].Args, cur[i].Env, cur[i].EnvFrom = p.Command, p.Args, p.Env, p.EnvFrom
		}
	}
	return out, changed
}

// sameContainers reports whether two copies run the same containers.
func sameContainers(a, b *corev1.Pod) bool {
	return equality.Semantic.DeepEqual(a.Spec.InitContainers, b.Spec.InitContainers) &&
		equality.Semantic.DeepEqual(a.Spec.Containers, b.Spec.Containers)
}
