package lab

import (
	"path"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// datastores are the engines the Lab runs empty in place of a copy of
// their state: a world needs them to answer, not to hold production's data.
var datastores = []string{"redis", "valkey", "postgres", "postgresql", "mysql", "mariadb", "mongo", "mongodb", "kafka", "rabbitmq"}

// datastoreEngine names the datastore a workload's first container runs,
// by its image's name, when it is one the Lab can run empty.
func datastoreEngine(spec corev1.PodSpec) (string, bool) {
	if len(spec.Containers) == 0 {
		return "", false
	}
	name, _, _ := strings.Cut(spec.Containers[0].Image, "@")
	name = path.Base(name)
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return name, slices.Contains(datastores, name)
}

// throwaway is a datastore without its persistent volumes: each claim, and
// each of a StatefulSet's claim templates, becomes an empty directory, so
// the copy starts as a new install of the same engine and version.
func throwaway(wl Workload) Workload {
	spec := wl.Template.Spec.DeepCopy()
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	for i := range spec.Volumes {
		if spec.Volumes[i].PersistentVolumeClaim != nil {
			spec.Volumes[i].VolumeSource = empty
		}
	}
	for _, name := range wl.ClaimTemplates {
		if !slices.ContainsFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Name == name }) {
			spec.Volumes = append(spec.Volumes, corev1.Volume{Name: name, VolumeSource: empty})
		}
	}
	wl.Template.Spec, wl.Claims, wl.ClaimTemplates = *spec, false, nil
	return wl
}
