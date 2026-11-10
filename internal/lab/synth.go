package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/topology"
)

// WorldRole is the Lab label's value on a synthesized world's objects.
const WorldRole = "world"

// CopyOf labels a world's pod with the production workload it copies.
const CopyOf = "sentinel.karimzakzouk.github.io/copy-of"

// SecretPlaceholder stands in for the value a key of a Secret holds in a
// world. The Lab never reads the value; the placeholder names the key, so a
// name a copy looks up from it, as a host taken from a secret, says which
// secret the world lacks.
func SecretPlaceholder(secret, key string) string {
	sum := sha256.Sum256([]byte(secret + "\x00" + key))
	return placeholderPrefix + hex.EncodeToString(sum[:5])
}

const placeholderPrefix = "sentinel-lab-secret-"

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
	// Claims says the workload keeps state in persistent volumes, and
	// ClaimTemplates names a StatefulSet's.
	Claims         bool
	ClaimTemplates []string
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
	// Calls are outbound calls traffic capture saw, used to fake outside
	// hosts with the statuses production got.
	Calls []capture.Call
}

// Plan is the Lab's copy of a world, and every way it differs from
// production.
type Plan struct {
	Workloads  []model.ObjectRef
	ConfigMaps []*corev1.ConfigMap
	// Secrets are honeytoken stand-ins the Lab minted; never production values.
	Secrets  []*corev1.Secret
	Services []*corev1.Service
	Pods     []*corev1.Pod
	Fidelity []string
	// Empty lists the datastores copied without their data.
	Empty []model.ObjectRef
}

// Split separates target's copy from the rest of the world, which a race
// keeps up across its runs.
func (p *Plan) Split(target model.ObjectRef) (*corev1.Pod, *Plan) {
	rest := *p
	rest.Pods = nil
	var pod *corev1.Pod
	for _, pp := range p.Pods {
		if pp.Labels[CopyOf] == target.Kind+"."+target.Name {
			pod = pp
		} else {
			rest.Pods = append(rest.Pods, pp)
		}
	}
	return pod, &rest
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
	honeys := map[string]*corev1.Secret{}
	for _, ref := range closure {
		wl := w.Workloads[ref]
		if wl.Claims {
			engine, ok := datastoreEngine(wl.Template.Spec)
			if !ok {
				return nil, fmt.Errorf("%s keeps state in persistent volumes, which the Lab cannot copy yet", ref)
			}
			note("%s runs as an empty %s: the Lab copies none of its data", ref, engine)
			wl = throwaway(wl)
			plan.Empty = append(plan.Empty, ref)
		}
		pod, err := copyPod(wl, w, ns, note, honeys)
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
	for _, name := range sortedKeys(honeys) {
		plan.Secrets = append(plan.Secrets, honeys[name])
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
	faked := map[string]bool{}
	for _, ref := range closure {
		for _, host := range topology.ExternalHosts(w.Workloads[ref].Template, w.Namespace, copied) {
			if !fakeableHost(host) {
				note("%s names %s, which is outside the Lab and unreachable there", ref, host)
				continue
			}
			if faked[host] {
				continue
			}
			faked[host] = true
			fake := fakeOf(host, w.Calls, ns)
			if reserved[fake.Service.Name] || copied[fake.Service.Name] {
				return nil, fmt.Errorf("outside host %s fakes as %s, which the Lab already uses", host, fake.Service.Name)
			}
			plan.ConfigMaps = append(plan.ConfigMaps, fake.ConfigMap)
			plan.Services = append(plan.Services, fake.Service)
			plan.Pods = append(plan.Pods, fake.Pod)
			if fake.Recorded {
				note("%s's %s is a fake of the outside service from captured traffic", ref, host)
			} else {
				note("%s's %s is a Lab fake of the outside host (no captured statuses)", ref, host)
			}
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
func copyPod(wl Workload, w World, ns string, note func(string, ...any), secrets map[string]*corev1.Secret) (*corev1.Pod, error) {
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
	if err := copyVolumes(spec.Volumes, ref, note, secrets, ns); err != nil {
		return nil, err
	}
	if err := restrict(&spec, ref, note); err != nil {
		return nil, err
	}
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			trimCPURequest(&cs[i], ref, note)
		}
	}
	name := ref.Name + "-lab"
	if ref.Kind == "StatefulSet" {
		name = ref.Name + "-0"
	}
	meta := worldMeta(ns, name, wl.Template.Labels)
	meta.Labels[CopyOf] = ref.Kind + "." + ref.Name
	meta.Annotations = map[string]string{}
	scraped := false
	for k, v := range wl.Template.Annotations {
		if strings.HasPrefix(k, "prometheus.io/") {
			scraped = true
			continue
		}
		meta.Annotations[k] = v
	}
	if scraped {
		note("%s's copy drops its prometheus.io annotations, so production's monitoring does not scrape it", ref)
	}
	meta.Annotations["sidecar.istio.io/inject"] = "false"
	meta.Annotations["linkerd.io/inject"] = "disabled"
	return &corev1.Pod{ObjectMeta: meta, Spec: spec}, nil
}

// restrict fits a copy to the restricted Pod Security standard the Lab's
// namespaces enforce, noting each change from production; it refuses what
// cannot be made to fit. A copy whose image runs only as root then fails to
// start, and the Lab says so.
func restrict(spec *corev1.PodSpec, ref model.ObjectRef, note func(string, ...any)) error {
	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	pod := spec.SecurityContext
	runtimeDefault := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	unconfined := func(p *corev1.SeccompProfile) bool { return p != nil && p.Type == corev1.SeccompProfileTypeUnconfined }
	if pod.SeccompProfile == nil || unconfined(pod.SeccompProfile) {
		pod.SeccompProfile = runtimeDefault
		note("%s runs under the runtime's default seccomp profile, which production does not set", ref)
	}
	if !ptr.Deref(pod.RunAsNonRoot, false) {
		pod.RunAsNonRoot = ptr.To(true)
		note("%s must run as a non-root user, which production does not require", ref)
	}
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			c := &cs[i]
			if c.SecurityContext == nil {
				c.SecurityContext = &corev1.SecurityContext{}
			}
			sc := c.SecurityContext
			if ptr.Deref(sc.Privileged, false) {
				return fmt.Errorf("%s runs %s privileged, which no Lab pod may", ref, c.Name)
			}
			if sc.Capabilities != nil {
				for _, add := range sc.Capabilities.Add {
					if add != "NET_BIND_SERVICE" {
						return fmt.Errorf("%s gives %s the capability %s, which no Lab pod may have", ref, c.Name, add)
					}
				}
			}
			if ptr.Deref(sc.AllowPrivilegeEscalation, true) {
				sc.AllowPrivilegeEscalation = ptr.To(false)
				note("%s's %s may not gain privileges, which production allows", ref, c.Name)
			}
			if sc.Capabilities == nil || !slices.Contains(sc.Capabilities.Drop, "ALL") {
				if sc.Capabilities == nil {
					sc.Capabilities = &corev1.Capabilities{}
				}
				sc.Capabilities.Drop = append(sc.Capabilities.Drop, "ALL")
				note("%s's %s runs without Linux capabilities, which production keeps", ref, c.Name)
			}
			if unconfined(sc.SeccompProfile) {
				sc.SeccompProfile = runtimeDefault
				note("%s's %s runs under the runtime's default seccomp profile instead of none", ref, c.Name)
			}
			if sc.RunAsNonRoot != nil && !*sc.RunAsNonRoot {
				sc.RunAsNonRoot = ptr.To(true)
				note("%s's %s must run as a non-root user, which production forbids", ref, c.Name)
			}
			for j := range c.Ports {
				if c.Ports[j].HostPort != 0 {
					c.Ports[j].HostPort = 0
					note("%s's %s listens on no port of the node", ref, c.Name)
				}
			}
		}
	}
	return nil
}

// maxCPURequest is the most CPU a copy's container asks the scheduler for.
// A request only reserves room on a node; the limit still caps the copy as
// it caps production, so the world fits beside production on the same
// nodes.
var maxCPURequest = resource.MustParse("50m")

func trimCPURequest(c *corev1.Container, ref model.ObjectRef, note func(string, ...any)) {
	req, ok := c.Resources.Requests[corev1.ResourceCPU]
	if !ok {
		// Without a request, Kubernetes takes the limit as one.
		req, ok = c.Resources.Limits[corev1.ResourceCPU]
	}
	if !ok || req.Cmp(maxCPURequest) <= 0 {
		return
	}
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	c.Resources.Requests[corev1.ResourceCPU] = maxCPURequest
	note("%s's %s asks for %s of CPU instead of %s, so the world fits beside production; its limit is production's", ref, c.Name,
		maxCPURequest.String(), req.String())
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
			note("%s's %s holds a honeytoken instead of secret %s", ref, c.Env[i].Name, from.SecretKeyRef.Name)
			c.Env[i] = corev1.EnvVar{Name: c.Env[i].Name, Value: Honeytoken(from.SecretKeyRef.Name, from.SecretKeyRef.Key)}
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

// copyVolumes keeps volumes that carry no state, mounts honeytoken Secrets
// when the keys are named in the pod spec, and turns other secrets and
// per-pod claims into empty directories.
func copyVolumes(vs []corev1.Volume, ref model.ObjectRef, note func(string, ...any), secrets map[string]*corev1.Secret, ns string) error {
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
			if honey := honeytokenSecret(v.Secret, ns); honey != nil {
				secrets[honey.Name] = honey
				note("%s's volume %s mounts a honeytoken secret instead of %s", ref, v.Name, v.Secret.SecretName)
				break
			}
			note("%s's volume %s is an empty directory instead of secret %s", ref, v.Name, v.Secret.SecretName)
			v.VolumeSource = empty
		default:
			note("%s's volume %s is an empty directory", ref, v.Name)
			v.VolumeSource = empty
		}
	}
	return nil
}

// honeytokenSecret mints a Lab Secret for a volume that names its keys. With
// no Items the Lab cannot know the keys without reading production, so it
// returns nil and the volume becomes an empty directory.
func honeytokenSecret(src *corev1.SecretVolumeSource, ns string) *corev1.Secret {
	if src == nil || len(src.Items) == 0 {
		return nil
	}
	data := map[string][]byte{}
	for _, it := range src.Items {
		name := it.Path
		if name == "" {
			name = it.Key
		}
		data[name] = []byte(Honeytoken(src.SecretName, it.Key))
	}
	return &corev1.Secret{ObjectMeta: worldMeta(ns, src.SecretName, nil), Type: corev1.SecretTypeOpaque, Data: data}
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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
