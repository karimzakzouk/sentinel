package lab

import (
	"bufio"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// DNSImage is the Lab DNS: CoreDNS answering only the world's names.
const DNSImage = "registry.k8s.io/coredns/coredns:v1.14.6"

const (
	dnsName = "lab-dns"
	dnsPort = 1053
)

// corefile serves one authoritative root zone and logs every query, so a
// name the Lab does not provide gets NXDOMAIN at once, as cluster DNS
// answers a name that does not exist. SERVFAIL, the hosts plugin's answer,
// would end musl's search-path walk before it reached the right name.
const corefile = `.:1053 {
    file /etc/coredns/zone .
    log . "{type} {name} {rcode}"
    ready :8181
    errors
}
`

// Zone is the Lab DNS's root zone: each copied Service's short and
// production names, under cluster domain domain, with its address in the
// Lab. No other name exists.
func Zone(services []*corev1.Service, prodNS, domain string) string {
	var b strings.Builder
	b.WriteString("$TTL 5\n. IN SOA lab-dns.invalid. hostmaster.invalid. 1 3600 600 86400 5\n. IN NS lab-dns.invalid.\nlab-dns.invalid. IN A 127.0.0.1\n")
	sorted := append([]*corev1.Service(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, s := range sorted {
		if s.Spec.ClusterIP == "" || s.Spec.ClusterIP == corev1.ClusterIPNone {
			continue
		}
		for _, name := range []string{s.Name, s.Name + "." + prodNS, s.Name + "." + prodNS + ".svc", s.Name + "." + prodNS + ".svc." + domain} {
			fmt.Fprintf(&b, "%s. IN A %s\n", name, s.Spec.ClusterIP)
		}
	}
	return b.String()
}

// DNSConfig points a world's pods at the Lab DNS, searching the production
// namespace first as its pods do, so their configuration resolves to the
// Lab's copies unchanged.
func DNSConfig(address, prodNS, domain string) *corev1.PodDNSConfig {
	return &corev1.PodDNSConfig{
		Nameservers: []string{address},
		Searches:    []string{prodNS + ".svc." + domain, "svc." + domain, domain},
		Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr.To("5")}},
	}
}

func dnsService(ns string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: worldMeta(ns, dnsName, nil),
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": dnsName}, Ports: []corev1.ServicePort{
			{Name: "dns", Protocol: corev1.ProtocolUDP, Port: 53, TargetPort: intstr.FromInt32(dnsPort)},
			{Name: "dns-tcp", Protocol: corev1.ProtocolTCP, Port: 53, TargetPort: intstr.FromInt32(dnsPort)},
		}},
	}
}

func dnsObjects(ns, zone string) (*corev1.ConfigMap, *corev1.Pod) {
	cm := &corev1.ConfigMap{ObjectMeta: worldMeta(ns, dnsName, nil), Data: map[string]string{"Corefile": corefile, "zone": zone}}
	pod := &corev1.Pod{
		ObjectMeta: worldMeta(ns, dnsName, map[string]string{"app": dnsName}),
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "dns", Image: DNSImage, Args: []string{"-conf", "/etc/coredns/Corefile"},
				Ports: []corev1.ContainerPort{{ContainerPort: dnsPort, Protocol: corev1.ProtocolUDP}, {ContainerPort: dnsPort, Protocol: corev1.ProtocolTCP}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8181)}},
					PeriodSeconds: 2},
				// The coredns binary carries NET_BIND_SERVICE as a file capability,
				// so it cannot start without it, though it binds no low port here.
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"NET_BIND_SERVICE"}}},
				VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/coredns", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: dnsName}}}}},
		},
	}
	return cm, pod
}

// Unresolved reads the Lab DNS log and returns the names a world asked for
// that the Lab does not provide, as the workload wrote them: lookups the
// search path expanded, and names the matrix asks for itself, are left out.
func Unresolved(log string, searches, own []string) []string {
	skip := map[string]bool{}
	for _, n := range own {
		skip[strings.TrimSuffix(n, ".")] = true
	}
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(log))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[len(fields)-1] != "NXDOMAIN" {
			continue
		}
		name := strings.TrimSuffix(fields[len(fields)-2], ".")
		expanded := false
		for _, s := range searches {
			if strings.HasSuffix(name, "."+s) {
				expanded = true
				break
			}
		}
		if expanded || skip[name] || seen[name] || name == "" {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
