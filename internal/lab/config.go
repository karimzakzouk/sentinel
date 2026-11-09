package lab

import (
	"fmt"
	"strings"

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

// ImageCandidate is the failing copy with image in place of the image of
// its containers from the same repository: a fix to the code, built.
func ImageCandidate(failing *corev1.Pod, image string) (*corev1.Pod, error) {
	repo := imageRepository(image)
	out := failing.DeepCopy()
	replaced := false
	for _, cs := range [][]corev1.Container{out.Spec.InitContainers, out.Spec.Containers} {
		for i := range cs {
			if imageRepository(cs[i].Image) == repo {
				cs[i].Image, replaced = image, true
			}
		}
	}
	if !replaced {
		return nil, fmt.Errorf("no container of the failing copy runs %s", repo)
	}
	return out, nil
}

// imageRepository is an image reference without its tag or digest.
func imageRepository(image string) string {
	name, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name = name[:i]
	}
	return name
}
