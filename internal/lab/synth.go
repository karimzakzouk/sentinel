package lab

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/topology"
)

// WorldRole is the Lab label's value on a synthesized world's objects.
const WorldRole = "world"

// CopyOf labels a world's pod with the production workload it copies.
const CopyOf = "sentinel.karimzakzouk.github.io/copy-of"

// Placeholder stands in for every secret value in a world.
const Placeholder = "sentinel-lab-placeholder"

// meshContainers are the sidecars and init containers service meshes
// inject; a Lab copy runs without them.
var meshContainers = map[string]bool{
	"istio-proxy": true, "istio-init": true, "istio-validation": true,
	"linkerd-proxy": true, "linkerd-init": true, "linkerd-network-validator": true,
	"kuma-sidecar": true, "kuma-init": true,
	"consul-dataplane": true, "consul-connect-inject-init": true,
}

// reserved are the names the Lab's own objects use in a world.
var reserved = map[string]bool{"lab-dns": true, "lab-echo": true, "lab-prober": true}

// Workload is a production workload as the Lab copies it.
type Workload struct {
	Ref      model.ObjectRef
	Template corev1.PodTemplateSpec
	// Replicas is how many pods production runs.
	Replicas int32
	// Claims says the workload keeps state in persistent volumes.
	Claims bool
}

// World is what the Lab reads from a production namespace. It never holds
// a Secret.
type World struct {
	Namespace  string
	Workloads  map[model.ObjectRef]Workload
	Services   []corev1.Service
	ConfigMaps map[string]*corev1.ConfigMap
	// Digests maps an image, as templates name it, to the digest-pinned
	// reference production's pods run.
	Digests map[string]string
}

// Plan is the Lab's copy of a world, and every way it differs from
// production.
type Plan struct {
	Workloads  []model.ObjectRef
	ConfigMaps []*corev1.ConfigMap
	Services   []*corev1.Service
	Pods       []*corev1.Pod
	Fidelity   []string
}

// Synthesize copies target, and every workload it depends on, into Lab
// namespace ns. It refuses what it cannot copy without state or privilege.
func Synthesize(w World, target model.ObjectRef, ns string) (*Plan, error) {
	if _, ok := w.Workloads[target]; !ok {
		return nil, fmt.Errorf("%s is not in the namespace", target)
	}
	plan := &Plan{}
	noted := map[string]bool{}
	note := func(format string, args ...any) {
		if s := fmt.Sprintf(format, args...); !noted[s] {
			noted[s] = true
			plan.Fidelity = append(plan.Fidelity, s)
		}
	}
	closure := []model.ObjectRef{target}
	for _, ref := range dependencyMap(w).Dependencies(target) {
		if _, ok := w.Workloads[ref]; ok {
			closure = append(closure, ref)
		}
	}
	copiedMaps := map[string]bool{}
	for _, ref := range closure {
		wl := w.Workloads[ref]
		if wl.Claims {
			return nil, fmt.Errorf("%s keeps state in persistent volumes, which the Lab cannot copy yet", ref)
		}
		pod, err := copyPod(wl, w, ns, note)
		if err != nil {
			return nil, err
		}
		if wl.Replicas > 1 {
			note("%s runs one replica instead of %d", ref, wl.Replicas)
		}
		for _, name := range configMapsOf(pod.Spec) {
			if copiedMaps[name.name] {
				continue
			}
			cm, ok := w.ConfigMaps[name.name]
			if !ok {
				if name.optional {
					continue
				}
				return nil, fmt.Errorf("%s needs ConfigMap %s, which does not exist", ref, name.name)
			}
			copiedMaps[name.name] = true
			plan.ConfigMaps = append(plan.ConfigMaps, &corev1.ConfigMap{
				ObjectMeta: worldMeta(ns, cm.Name, nil), Data: cm.Data, BinaryData: cm.BinaryData})
		}
		plan.Pods = append(plan.Pods, pod)
		plan.Workloads = append(plan.Workloads, ref)
	}
	copied := map[string]bool{}
	for i := range w.Services {
		svc := &w.Services[i]
		if len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName || !selectsAny(svc, plan.Pods) {
			continue
		}
		if reserved[svc.Name] {
			return nil, fmt.Errorf("production Service %s has a name the Lab uses", svc.Name)
		}
		copied[svc.Name] = true
		plan.Services = append(plan.Services, copyService(svc, ns, note))
	}
	for _, ref := range closure {
		for _, host := range topology.ExternalHosts(w.Workloads[ref].Template, w.Namespace, copied) {
			note("%s names %s, which is outside the Lab and unreachable there", ref, host)
		}
	}
	sort.Strings(plan.Fidelity)
	return plan, nil
}

// dependencyMap maps the namespace's workloads from their configuration.
func dependencyMap(w World) *topology.Map {
	targets := map[string]topology.ServiceTarget{}
	for _, svc := range w.Services {
		t := topology.ServiceTarget{Name: svc.Name}
		if len(svc.Spec.Selector) > 0 {
			sel := labels.SelectorFromSet(svc.Spec.Selector)
			for ref, wl := range w.Workloads {
				if sel.Matches(labels.Set(wl.Template.Labels)) {
					t.Backing = append(t.Backing, ref)
				}
			}
			sort.Slice(t.Backing, func(i, j int) bool { return t.Backing[i].String() < t.Backing[j].String() })
		}
		targets[svc.Name] = t
	}
	var edges []topology.Edge
	for ref, wl := range w.Workloads {
		edges = append(edges, topology.ConfigEdges(ref, wl.Template, targets)...)
	}
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].From.String()+edges[i].To.String() < edges[j].From.String()+edges[j].To.String()
	})
	return topology.NewMap(edges)
}

// copyPod is one pod of a workload, as the Lab may run it.
func copyPod(wl Workload, w World, ns string, note func(string, ...any)) (*corev1.Pod, error) {
	ref := wl.Ref
	spec := *wl.Template.Spec.DeepCopy()
	if spec.HostNetwork || spec.HostPID || spec.HostIPC {
		return nil, fmt.Errorf("%s shares the node's namespaces, which no Lab pod may", ref)
	}
	if spec.ServiceAccountName != "" && spec.ServiceAccountName != "default" {
		note("%s runs without its service account %s or any API token", ref, spec.ServiceAccountName)
	}
	spec.ServiceAccountName, spec.DeprecatedServiceAccount = "", ""
	spec.AutomountServiceAccountToken = ptr.To(false)
	if spec.PriorityClassName != "" || spec.Priority != nil {
		note("%s runs at the default priority, so it never preempts production", ref)
	}
	spec.PriorityClassName, spec.Priority, spec.PreemptionPolicy = "", nil, nil
	if len(spec.ImagePullSecrets) > 0 {
		note("%s pulls without production's registry credentials, so its images must already be on the nodes", ref)
	}
	spec.ImagePullSecrets = nil
	spec.NodeName = ""
	spec.EphemeralContainers = nil
	spec.RestartPolicy = corev1.RestartPolicyAlways
	spec.DNSPolicy = corev1.DNSNone
	spec.InitContainers = withoutMesh(spec.InitContainers, ref, note)
	spec.Containers = withoutMesh(spec.Containers, ref, note)
	if len(spec.Containers) == 0 {
		return nil, fmt.Errorf("%s has no containers once its mesh sidecars are removed", ref)
	}
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			copyContainer(&cs[i], ref, w.Digests, note)
		}
	}
	if err := copyVolumes(spec.Volumes, ref, note); err != nil {
		return nil, err
	}
	name := ref.Name + "-lab"
	if ref.Kind == "StatefulSet" {
		name = ref.Name + "-0"
	}
	meta := worldMeta(ns, name, wl.Template.Labels)
	meta.Labels[CopyOf] = ref.Kind + "." + ref.Name
	meta.Annotations = map[string]string{}
	for k, v := range wl.Template.Annotations {
		meta.Annotations[k] = v
	}
	meta.Annotations["sidecar.istio.io/inject"] = "false"
	meta.Annotations["linkerd.io/inject"] = "disabled"
	return &corev1.Pod{ObjectMeta: meta, Spec: spec}, nil
}

func withoutMesh(cs []corev1.Container, ref model.ObjectRef, note func(string, ...any)) []corev1.Container {
	return slices.DeleteFunc(cs, func(c corev1.Container) bool {
		if meshContainers[c.Name] {
			note("%s runs without its service mesh container %s", ref, c.Name)
			return true
		}
		return false
	})
}

func copyContainer(c *corev1.Container, ref model.ObjectRef, digests map[string]string, note func(string, ...any)) {
	if pinned, ok := digests[c.Image]; ok {
		c.Image = pinned
	} else if !strings.Contains(c.Image, "@sha256:") {
		note("%s's image %s is not pinned: production's digest is unknown", ref, c.Image)
	}
	for i := range c.Env {
		if from := c.Env[i].ValueFrom; from != nil && from.SecretKeyRef != nil {
			note("%s's %s holds a placeholder instead of secret %s", ref, c.Env[i].Name, from.SecretKeyRef.Name)
			c.Env[i] = corev1.EnvVar{Name: c.Env[i].Name, Value: Placeholder}
		}
	}
	c.EnvFrom = slices.DeleteFunc(c.EnvFrom, func(e corev1.EnvFromSource) bool {
		if e.SecretRef != nil {
			note("%s runs without the variables of secret %s", ref, e.SecretRef.Name)
			return true
		}
		return false
	})
}

// copyVolumes keeps volumes that carry no state or secret, and turns
// secrets and per-pod claims into empty directories.
func copyVolumes(vs []corev1.Volume, ref model.ObjectRef, note func(string, ...any)) error {
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	for i := range vs {
		v := &vs[i]
		switch {
		case v.EmptyDir != nil, v.ConfigMap != nil, v.DownwardAPI != nil:
		case v.Projected != nil && !slices.ContainsFunc(v.Projected.Sources, func(s corev1.VolumeProjection) bool {
			return s.ConfigMap == nil && s.DownwardAPI == nil
		}):
		case v.HostPath != nil:
			return fmt.Errorf("%s mounts a path from the node, which no Lab pod may", ref)
		case v.PersistentVolumeClaim != nil:
			return fmt.Errorf("%s keeps state in persistent volumes, which the Lab cannot copy yet", ref)
		case v.Secret != nil:
			note("%s's volume %s is an empty directory instead of secret %s", ref, v.Name, v.Secret.SecretName)
			v.VolumeSource = empty
		default:
			note("%s's volume %s is an empty directory", ref, v.Name)
			v.VolumeSource = empty
		}
	}
	return nil
}

type configMapRef struct {
	name     string
	optional bool
}

// configMapsOf lists the ConfigMaps a pod spec reads, in order.
func configMapsOf(spec corev1.PodSpec) []configMapRef {
	var out []configMapRef
	add := func(name string, optional *bool) {
		out = append(out, configMapRef{name: name, optional: ptr.Deref(optional, false)})
	}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
				add(e.ValueFrom.ConfigMapKeyRef.Name, e.ValueFrom.ConfigMapKeyRef.Optional)
			}
		}
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				add(e.ConfigMapRef.Name, e.ConfigMapRef.Optional)
			}
		}
	}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			add(v.ConfigMap.Name, v.ConfigMap.Optional)
		}
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if s.ConfigMap != nil {
					add(s.ConfigMap.Name, s.ConfigMap.Optional)
				}
			}
		}
	}
	return out
}

func selectsAny(svc *corev1.Service, pods []*corev1.Pod) bool {
	sel := labels.SelectorFromSet(svc.Spec.Selector)
	return slices.ContainsFunc(pods, func(p *corev1.Pod) bool { return sel.Matches(labels.Set(p.Labels)) })
}

func copyService(svc *corev1.Service, ns string, note func(string, ...any)) *corev1.Service {
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		note("headless Service %s answers with one address in the Lab", svc.Name)
	}
	ports := make([]corev1.ServicePort, len(svc.Spec.Ports))
	for i, p := range svc.Spec.Ports {
		ports[i] = corev1.ServicePort{Name: p.Name, Protocol: p.Protocol, Port: p.Port, TargetPort: p.TargetPort, AppProtocol: p.AppProtocol}
	}
	return &corev1.Service{
		ObjectMeta: worldMeta(ns, svc.Name, nil),
		Spec: corev1.ServiceSpec{Selector: svc.Spec.Selector, Ports: ports, SessionAffinity: svc.Spec.SessionAffinity,
			PublishNotReadyAddresses: svc.Spec.PublishNotReadyAddresses},
	}
}

func worldMeta(ns, name string, extra map[string]string) metav1.ObjectMeta {
	l := map[string]string{}
	for k, v := range extra {
		l[k] = v
	}
	l[Label] = WorldRole
	return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: l}
}
