package capability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
)

// Options configure a detection run.
type Options struct {
	// Namespaces are the workload namespaces Sentinel is allowed to watch.
	Namespaces []string
	// AgentNamespace is where Sentinel's own workloads run, including the
	// node agent. Node-agent and eBPF probes are dry-run there.
	AgentNamespace string
	// LabNamespace is one namespace from the Lab pool. The user-namespace
	// probe and the active NetworkPolicy probe run there.
	LabNamespace string
	// ActorUser is the identity that executes actions, such as
	// "system:serviceaccount:sentinel:sentinel-actor".
	ActorUser   string
	ActorGroups []string
	// NetworkPolicy is the result of an active NetworkPolicyProbe, if one ran.
	NetworkPolicy *Check
}

// Detector computes capability reports.
type Detector struct {
	Client  kubernetes.Interface
	Options Options
	Now     func() time.Time
}

// Run computes a report. It fails only when it cannot start; individual
// checks that fail are reported as unknown.
func (d *Detector) Run(ctx context.Context) (*Report, error) {
	if len(d.Options.Namespaces) == 0 {
		return nil, errors.New("no namespaces given: Sentinel watches only namespaces it is explicitly allowed to")
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	rep := &Report{GeneratedAt: now().UTC(), Identity: d.whoAmI(ctx)}
	if v, err := d.Client.Discovery().ServerVersion(); err == nil {
		rep.Cluster.ServerVersion = v.GitVersion
	} else {
		rep.Cluster.ServerVersion = "unknown"
	}

	apis := d.discover(rep)
	nodes := d.listNodes(ctx, rep)
	rep.Cluster.Nodes = len(nodes)
	rep.Cluster.Platform = detectPlatform(nodes, apis)
	rep.Cluster.NetworkPlugin = detectNetworkPlugin(nodes, apis, rep.Cluster.Platform)

	watchable, loggable, measurable := d.checkWatcherAccess(ctx, rep)
	d.checkLeastPrivilege(ctx, rep)
	actable := d.checkActorAccess(ctx, rep)
	probes := d.runDryRunProbes(ctx, rep)
	sandbox := d.checkRuntimeClasses(ctx, rep, apis)
	d.checkNetworkPolicy(rep, apis)
	d.checkOwners(ctx, rep, apis, watchable)

	d.deriveFeatures(rep, access{watchable, loggable, measurable, actable}, probes, sandbox)
	rep.Nodes = coverNodes(nodes, probes, len(watchable) > 0)
	rep.sortChecks()
	return rep, nil
}

func (d *Detector) whoAmI(ctx context.Context) string {
	res, err := d.Client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil || res.Status.UserInfo.Username == "" {
		return "unknown"
	}
	return res.Status.UserInfo.Username
}

// apiSet records which group/resource pairs the cluster serves.
type apiSet struct {
	has    map[string]bool
	failed map[string]bool
}

func (a apiSet) serves(group, resource string) bool { return a.has[group+"/"+resource] }

func (d *Detector) discover(rep *Report) apiSet {
	set := apiSet{has: map[string]bool{}, failed: map[string]bool{}}
	_, lists, err := d.Client.Discovery().ServerGroupsAndResources()
	if err != nil {
		var gdf *discovery.ErrGroupDiscoveryFailed
		if !errors.As(err, &gdf) {
			rep.add("api.discovery", Unknown, "API discovery failed: %v", err)
			return set
		}
		for gv, gErr := range gdf.Groups {
			set.failed[gv.Group] = true
			rep.add("api.discovery/"+gv.String(), Unknown, "the API server could not list this group: %v", gErr)
		}
	}
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if !strings.Contains(r.Name, "/") {
				set.has[gv.Group+"/"+r.Name] = true
			}
		}
	}
	apiCheck := func(id, group, resource, yes, no string) {
		switch {
		case set.serves(group, resource):
			rep.add(id, Available, "%s", yes)
		case set.failed[group]:
			rep.add(id, Unknown, "%s could not be discovered", group)
		default:
			rep.add(id, Unavailable, "%s", no)
		}
	}
	apiCheck("api.metrics", "metrics.k8s.io", "pods",
		"metrics.k8s.io serves pod CPU and memory", "metrics.k8s.io is not installed (no metrics-server)")
	apiCheck("api.network-policy", "networking.k8s.io", "networkpolicies",
		"NetworkPolicy API exists (enforcement is a separate check)", "NetworkPolicy API is missing")
	apiCheck("api.admission-policy", "admissionregistration.k8s.io", "validatingadmissionpolicies",
		"ValidatingAdmissionPolicy is served, so the fence can be installed", "ValidatingAdmissionPolicy is missing; the fence needs Kubernetes 1.30 or newer")
	return set
}

func (d *Detector) listNodes(ctx context.Context, rep *Report) []corev1.Node {
	list, err := d.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		rep.add("cluster.nodes", Unknown, "could not list nodes: %v", truncate(err.Error(), 200))
		return nil
	}
	rep.add("cluster.nodes", Available, "%d node(s)", len(list.Items))
	return list.Items
}

// detectPlatform names the distribution or managed service from node facts.
func detectPlatform(nodes []corev1.Node, apis apiSet) string {
	if apis.serves("config.openshift.io", "clusterversions") {
		return "openshift"
	}
	for _, n := range nodes {
		pid, labels, kubelet := n.Spec.ProviderID, n.Labels, n.Status.NodeInfo.KubeletVersion
		switch {
		case strings.HasPrefix(pid, "kind://"):
			return "kind"
		case labels["minikube.k8s.io/name"] != "":
			return "minikube"
		case strings.HasPrefix(pid, "k3s://"), strings.Contains(kubelet, "+k3s"), labels["node.kubernetes.io/instance-type"] == "k3s":
			return "k3s"
		case strings.Contains(kubelet, "+rke2"):
			return "rke2"
		case strings.Contains(kubelet, "+k0s"):
			return "k0s"
		case strings.HasPrefix(pid, "aws://") && hasLabelPrefix(labels, "eks.amazonaws.com/"):
			return "eks"
		case strings.HasPrefix(pid, "gce://") && labels["cloud.google.com/gke-nodepool"] != "":
			return "gke"
		case strings.HasPrefix(pid, "azure://") && hasLabelPrefix(labels, "kubernetes.azure.com/"):
			return "aks"
		}
	}
	return "unknown"
}

func detectNetworkPlugin(nodes []corev1.Node, apis apiSet, platform string) string {
	switch {
	case apis.serves("cilium.io", "ciliumnetworkpolicies"):
		return "cilium"
	case apis.serves("crd.projectcalico.org", "networkpolicies"), apis.serves("projectcalico.org", "networkpolicies"):
		return "calico"
	case apis.serves("crd.antrea.io", "antreaagentinfos"):
		return "antrea"
	}
	for _, n := range nodes {
		if n.Annotations["projectcalico.org/IPv4Address"] != "" {
			return "calico"
		}
	}
	if platform == "kind" {
		return "kindnet (kind default, unless replaced)"
	}
	return ""
}

func (d *Detector) checkRuntimeClasses(ctx context.Context, rep *Report, apis apiSet) []string {
	if !apis.serves("node.k8s.io", "runtimeclasses") {
		rep.add("runtime.sandbox", Unavailable, "RuntimeClass API is missing")
		return nil
	}
	list, err := d.Client.NodeV1().RuntimeClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		rep.add("runtime.sandbox", Unknown, "could not list RuntimeClasses: %s", truncate(err.Error(), 200))
		return nil
	}
	var sandboxes []string
	for _, rc := range list.Items {
		h := strings.ToLower(rc.Handler)
		if h == "runsc" || strings.Contains(h, "gvisor") || strings.Contains(h, "kata") {
			sandboxes = append(sandboxes, rc.Name)
		}
	}
	if len(sandboxes) == 0 {
		rep.add("runtime.sandbox", Unavailable, "no gVisor or Kata RuntimeClass (%d RuntimeClass(es) found)", len(list.Items))
		return nil
	}
	rep.add("runtime.sandbox", Available, "sandbox RuntimeClass(es): %s", strings.Join(sandboxes, ", "))
	return sandboxes
}

func (d *Detector) checkNetworkPolicy(rep *Report, apis apiSet) {
	if d.Options.NetworkPolicy != nil {
		c := *d.Options.NetworkPolicy
		c.ID = NetworkPolicyCheckID
		rep.Checks = append(rep.Checks, c)
		return
	}
	if !apis.serves("networking.k8s.io", "networkpolicies") {
		rep.add(NetworkPolicyCheckID, Unavailable, "the NetworkPolicy API is missing")
		return
	}
	hint := ""
	if p := rep.Cluster.NetworkPlugin; p != "" {
		hint = fmt.Sprintf(" (network plugin: %s)", p)
	}
	rep.add(NetworkPolicyCheckID, Unknown, "not verified%s; run the active probe with `sentinel capabilities --active`", hint)
}

type owner struct {
	name, group, resource, describe string
}

var owners = []owner{
	{"argocd", "argoproj.io", "applications", "Argo CD"},
	{"argo-rollouts", "argoproj.io", "rollouts", "Argo Rollouts"},
	{"flux", "kustomize.toolkit.fluxcd.io", "kustomizations", "Flux"},
	{"flux-helm", "helm.toolkit.fluxcd.io", "helmreleases", "Flux HelmRelease"},
	{"flagger", "flagger.app", "canaries", "Flagger"},
}

func (d *Detector) checkOwners(ctx context.Context, rep *Report, apis apiSet, watchable []string) {
	for _, o := range owners {
		if apis.serves(o.group, o.resource) {
			rep.add("owner."+o.name, Available, "%s is installed", o.describe)
		} else {
			rep.add("owner."+o.name, Unavailable, "%s is not installed", o.describe)
		}
	}
	// Helm keeps release state in Secrets, which Sentinel must not read; the
	// labels Helm puts on what it manages are enough to find its workloads.
	helmManaged := 0
	for _, ns := range watchable {
		list, err := d.Client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=Helm"})
		if err != nil {
			rep.add("owner.helm", Unknown, "could not list Deployments in %s: %s", ns, truncate(err.Error(), 200))
			return
		}
		helmManaged += len(list.Items)
	}
	if helmManaged > 0 {
		rep.add("owner.helm", Available, "%d Helm-managed Deployment(s) in watched namespaces", helmManaged)
	} else {
		rep.add("owner.helm", Unavailable, "no Helm-managed Deployments in watched namespaces")
	}
}

// access lists the namespaces where each kind of access was found.
type access struct {
	watchable, loggable, measurable, actable []string
}

func (d *Detector) deriveFeatures(rep *Report, a access, probes probeResults, sandbox []string) {
	status := func(id string) Status {
		c, _ := rep.Check(id)
		return c.Status
	}
	watchable, loggable, actable := a.watchable, a.loggable, a.actable
	if len(watchable) > 0 {
		rep.feature("detect.api", true, "Kubernetes API signals in %d namespace(s): %s", len(watchable), strings.Join(watchable, ", "))
	} else {
		rep.feature("detect.api", false, "Sentinel cannot read workloads in any allowed namespace")
	}
	if len(loggable) > 0 {
		rep.feature("detect.logs", true, "container log evidence in %d namespace(s): %s", len(loggable), strings.Join(loggable, ", "))
	} else {
		rep.feature("detect.logs", false, "Sentinel cannot read container logs in any allowed namespace")
	}
	switch {
	case status("api.metrics") != Available:
		rep.feature("detect.metrics", false, "%s", detail(rep, "api.metrics"))
	case len(a.measurable) == 0:
		rep.feature("detect.metrics", false, "Sentinel cannot read pod metrics in any allowed namespace")
	default:
		rep.feature("detect.metrics", true, "pod resource metrics in %d namespace(s): %s", len(a.measurable), strings.Join(a.measurable, ", "))
	}
	rep.feature("detect.node-agent", probes.nodeAgent == Available, "%s", detail(rep, "probe.node-agent"))
	rep.feature("detect.ebpf", probes.ebpf == Available, "%s", detail(rep, "probe.ebpf"))

	switch {
	case status("api.admission-policy") != Available:
		rep.feature("act", false, "the fence cannot be installed: %s", detail(rep, "api.admission-policy"))
	case len(actable) == 0:
		rep.feature("act", false, "the actor identity cannot patch workloads in any allowed namespace")
	default:
		rep.feature("act", true, "reversible actions possible in %d namespace(s): %s", len(actable), strings.Join(actable, ", "))
	}

	switch {
	case status(NetworkPolicyCheckID) != Available:
		rep.feature("lab", false, "the Lab stays off until NetworkPolicy enforcement is proven: %s", detail(rep, NetworkPolicyCheckID))
	case d.Options.LabNamespace == "":
		rep.feature("lab", false, "no Lab namespace is configured")
	default:
		rep.feature("lab", true, "NetworkPolicy enforcement is proven by live traffic")
	}
	rep.feature("lab.user-namespaces", probes.userNamespace == Available, "%s", detail(rep, "probe.user-namespace"))
	rep.feature("lab.sandbox-runtime", len(sandbox) > 0, "%s", detail(rep, "runtime.sandbox"))

	for _, o := range owners {
		rep.feature("owner."+o.name, status("owner."+o.name) == Available, "%s", detail(rep, "owner."+o.name))
	}
	rep.feature("owner.helm", status("owner.helm") == Available, "%s", detail(rep, "owner.helm"))
}

func detail(rep *Report, id string) string {
	c, ok := rep.Check(id)
	if !ok {
		return "not checked"
	}
	return c.Detail
}

// coverNodes builds the per-node coverage map.
func coverNodes(nodes []corev1.Node, probes probeResults, api bool) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		info := n.Status.NodeInfo
		node := Node{
			Name: n.Name, KubeletVersion: info.KubeletVersion, KernelVersion: info.KernelVersion,
			ContainerRuntime: info.ContainerRuntimeVersion, OSImage: info.OSImage, Architecture: info.Architecture,
		}
		if api {
			node.Tiers = append(node.Tiers, "api")
		}
		daemonSets := true
		switch {
		case n.Labels["eks.amazonaws.com/compute-type"] == "fargate":
			daemonSets = false
			node.Notes = append(node.Notes, "Fargate runs no DaemonSets")
		case n.Labels["type"] == "virtual-kubelet":
			daemonSets = false
			node.Notes = append(node.Notes, "virtual node runs no DaemonSets")
		}
		if daemonSets && probes.nodeAgent == Available {
			node.Tiers = append(node.Tiers, "node-agent")
		}
		if daemonSets && probes.ebpf == Available {
			if kernelAtLeast(info.KernelVersion, 5, 8) {
				node.Tiers = append(node.Tiers, "ebpf")
			} else {
				node.Notes = append(node.Notes, "kernel "+info.KernelVersion+" is older than 5.8, so no eBPF")
			}
		}
		out = append(out, node)
	}
	slices.SortFunc(out, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func hasLabelPrefix(labels map[string]string, prefix string) bool {
	for k := range labels {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
