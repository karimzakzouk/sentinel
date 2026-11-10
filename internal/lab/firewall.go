package lab

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

// FirewallName is the init container that seals a Lab pod by itself where
// the network plugin does not enforce NetworkPolicy. It runs before
// anything else in the pod, from Sentinel's own image, and leaves the
// pod's network able to open connections only to the destinations the Lab
// names. The pod's own containers have no capability to change it.
const FirewallName = "lab-seal"

// FirewallCommand is the firewall's entry point in Sentinel's image.
var FirewallCommand = []string{"/sentinel"}

// Allow is one destination a sealed pod may open connections to.
type Allow struct {
	// Proto is tcp or udp.
	Proto string
	Addr  netip.AddrPort
}

func (a Allow) String() string { return a.Proto + "/" + a.Addr.String() }

// ParseAllow reads an Allow as String writes it: "tcp/10.96.0.10:53".
func ParseAllow(s string) (Allow, error) {
	proto, addr, ok := strings.Cut(s, "/")
	if !ok || proto != "tcp" && proto != "udp" {
		return Allow{}, fmt.Errorf("%q is not tcp/ADDRESS:PORT or udp/ADDRESS:PORT", s)
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return Allow{}, fmt.Errorf("%q: %w", s, err)
	}
	if ap.Port() == 0 {
		return Allow{}, fmt.Errorf("%q names no port", s)
	}
	return Allow{Proto: proto, Addr: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// Firewall is the init container that seals the pod it runs in, letting
// it open connections only to allow. It runs as root with NET_ADMIN alone,
// the one container of a Lab pod that may, and exits once the seal is set.
func Firewall(image string, allow []Allow) corev1.Container {
	args := []string{"lab-seal"}
	for _, a := range allow {
		args = append(args, "--allow="+a.String())
	}
	return corev1.Container{
		Name: FirewallName, Image: image, Command: slices.Clone(FirewallCommand), Args: args,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: ptr.To[int64](0), RunAsNonRoot: ptr.To(false),
			AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities:   &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}},
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}

// seal puts the firewall before everything else in pod, when the Lab seals
// pods itself; otherwise it returns pod as it is.
func (p *Prover) seal(pod *corev1.Pod, allow []Allow) *corev1.Pod {
	if p.SealImage == "" {
		return pod
	}
	pod = pod.DeepCopy()
	pod.Spec.InitContainers = append([]corev1.Container{Firewall(p.SealImage, allow)}, pod.Spec.InitContainers...)
	return pod
}

// serviceAllow is where a client of services may connect: each Service's
// cluster addresses, at every port it serves over TCP or UDP.
func serviceAllow(services []*corev1.Service) []Allow {
	var out []Allow
	for _, svc := range services {
		ips := svc.Spec.ClusterIPs
		if len(ips) == 0 && svc.Spec.ClusterIP != "" {
			ips = []string{svc.Spec.ClusterIP}
		}
		for _, ip := range ips {
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				continue
			}
			for _, port := range svc.Spec.Ports {
				proto := strings.ToLower(string(port.Protocol))
				if proto == "" {
					proto = "tcp"
				}
				if proto != "tcp" && proto != "udp" || port.Port <= 0 || port.Port > 65535 {
					continue
				}
				out = append(out, Allow{Proto: proto, Addr: netip.AddrPortFrom(addr.Unmap(), uint16(port.Port))})
			}
		}
	}
	return out
}

// dnsAllow is the Lab DNS, over UDP and TCP.
func dnsAllow(dns string) []Allow {
	addr, err := netip.ParseAddr(dns)
	if err != nil {
		return nil
	}
	ap := netip.AddrPortFrom(addr.Unmap(), 53)
	return []Allow{{Proto: "udp", Addr: ap}, {Proto: "tcp", Addr: ap}}
}

// podAllow is a pod's address at ports, over TCP.
func podAllow(ip string, ports ...int32) []Allow {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	var out []Allow
	for _, port := range ports {
		if port > 0 && port <= 65535 {
			a := Allow{Proto: "tcp", Addr: netip.AddrPortFrom(addr.Unmap(), uint16(port))}
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}
