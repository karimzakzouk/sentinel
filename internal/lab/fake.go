package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capture"
)

// FakeHost names the outside host a Lab fake Service stands in for. The Lab
// DNS answers it with the Service's address.
const FakeHost = "sentinel.karimzakzouk.github.io/fake-host"

// FakeTLS marks a fake stub that speaks TLS with a Lab-issued certificate.
const FakeTLS = "sentinel.karimzakzouk.github.io/fake-tls"

// outboundFrom turns Experiment fake calls into the catalog synthesis uses.
func outboundFrom(fakes []v1alpha1.FakeCall) []capture.Call {
	out := make([]capture.Call, 0, len(fakes))
	for _, f := range fakes {
		if f.Host == "" || f.Method == "" || f.Path == "" {
			continue
		}
		out = append(out, capture.Call{Host: f.Host, Method: f.Method, Path: f.Path, Port: int(f.Port), Status: int(f.Status)})
	}
	return out
}

// Fake is a Lab Service and stub pod that answer an outside host's recorded
// HTTP statuses inside the seal.
type Fake struct {
	Host      string
	Service   *corev1.Service
	Pod       *corev1.Pod
	ConfigMap *corev1.ConfigMap
	// Secrets hold the host certificate when the fake speaks TLS; the shared
	// Lab CA Secret is added once on the plan.
	Secrets []*corev1.Secret
	// Recorded is true when captured Call statuses shaped the stub.
	Recorded bool
	// TLS is true when the stub speaks TLS with a Lab-issued certificate.
	TLS bool
}

type fakeRoute struct {
	Method, Path string
	Status       int
}

// fakeOf builds a Service, ConfigMap and stub pod for host from calls to it.
// The stub listens on high ports; the Service maps each recorded (or default)
// port onto them, so a non-root pod can answer without NET_BIND_SERVICE.
// When ca is set and the host needs TLS (port 443), the stub mounts a
// Lab-issued certificate and speaks TLS on those ports.
func fakeOf(host string, calls []capture.Call, ns string, ca *Authority) *Fake {
	routes, ports, recorded := fakeRoutes(host, calls)
	if len(ports) == 0 {
		ports = []int{80}
	}
	name := fakeName(host)
	tlsPorts := map[int]bool{}
	for _, p := range ports {
		if needsTLSPort(p) {
			tlsPorts[p] = true
		}
	}
	useTLS := ca != nil && len(tlsPorts) > 0
	targets := make([]int32, len(ports))
	svcPorts := make([]corev1.ServicePort, len(ports))
	var listenArgs []string
	for i, p := range ports {
		targets[i] = int32(8080 + i) //nolint:gosec // 8080+i stays well below 65536
		svcPorts[i] = corev1.ServicePort{
			Name: "p" + strconv.Itoa(p), Protocol: corev1.ProtocolTCP,
			Port: int32(p), TargetPort: intstr.FromInt32(targets[i]), //nolint:gosec // port from capture or 80
		}
		mode := "plain"
		if tlsPorts[p] && useTLS {
			mode = "tls"
		}
		listenArgs = append(listenArgs, mode+":"+strconv.Itoa(int(targets[i])))
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: worldMeta(ns, name, nil),
		Data:       map[string]string{"respond.sh": respondScript(routes), "routes": routesFile(routes)},
	}
	labels := map[string]string{"app": name}
	meta := worldMeta(ns, name, labels)
	svcMeta := worldMeta(ns, name, nil)
	svcMeta.Annotations = map[string]string{FakeHost: host}
	mounts := []corev1.VolumeMount{{Name: "script", MountPath: "/etc/lab-fake", ReadOnly: true}}
	volumes := []corev1.Volume{{Name: "script", VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name},
			DefaultMode: ptr.To[int32](0555)}}}}
	var secrets []*corev1.Secret
	if useTLS {
		certPEM, keyPEM, err := ca.Issue(host)
		if err != nil {
			// Synthesis cannot proceed without a host cert when TLS is required.
			panic(err)
		}
		secName := tlsSecretName(name)
		secrets = append(secrets, &corev1.Secret{
			ObjectMeta: worldMeta(ns, secName, nil),
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "tls", MountPath: "/etc/lab-tls", ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "tls", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secName, DefaultMode: ptr.To[int32](0440)}}})
		meta.Annotations = map[string]string{FakeTLS: "true"}
	}
	pod := &corev1.Pod{
		ObjectMeta: meta,
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			DNSPolicy:                    corev1.DNSNone,
			RestartPolicy:                corev1.RestartPolicyAlways,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "fake", Image: Image, Command: []string{"sh", "-c", fakeListenScript}, Args: listenArgs,
				Ports: containerPorts(targets),
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(targets[0])}},
					PeriodSeconds: 2},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				VolumeMounts: mounts,
			}},
			Volumes: volumes,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: svcMeta,
		Spec:       corev1.ServiceSpec{Selector: labels, Ports: svcPorts},
	}
	return &Fake{Host: host, Service: svc, Pod: pod, ConfigMap: cm, Secrets: secrets, Recorded: recorded, TLS: useTLS}
}

// needsTLSPort is true for the HTTPS port, which needs a Lab-issued certificate.
func needsTLSPort(port int) bool {
	return port == 443
}

// fakeListenScript runs one stub per Service target port. Plain ports use
// busybox netcat and respond.sh; TLS ports prefer Sentinel's lab-fake (the
// Build step swaps the image to SealImage) and fall back to agnhost netexec
// with the Lab-issued certificate so the handshake still succeeds.
const fakeListenScript = `set -e
for spec in "$@"; do
  mode=${spec%%:*}; p=${spec#*:}
  (
    if [ "$mode" = tls ]; then
      if [ -x /sentinel ]; then
        while true; do /sentinel lab-fake --cert=/etc/lab-tls/tls.crt --key=/etc/lab-tls/tls.key --routes=/etc/lab-fake/routes --listen="$p" || true; done
      else
        /agnhost netexec --http-port "$p" --udp-port -1 \
          --tls-cert-file /etc/lab-tls/tls.crt --tls-private-key-file /etc/lab-tls/tls.key &
        wait
      fi
    else
      while true; do busybox nc -l -p "$p" -w 5 -e /bin/sh /etc/lab-fake/respond.sh; done
    fi
  ) &
done
wait
`

func containerPorts(ports []int32) []corev1.ContainerPort {
	out := make([]corev1.ContainerPort, len(ports))
	for i, p := range ports {
		out[i] = corev1.ContainerPort{ContainerPort: p, Protocol: corev1.ProtocolTCP}
	}
	return out
}

// fakeRoutes lists distinct HTTP method+path statuses for host, most frequent
// first as Calls ordered them, and the ports those calls used.
func fakeRoutes(host string, calls []capture.Call) (routes []fakeRoute, ports []int, recorded bool) {
	seenPath := map[string]bool{}
	seenPort := map[int]bool{}
	host = strings.ToLower(host)
	for _, c := range calls {
		if strings.ToLower(c.Host) != host || c.Method == "" || c.Method == "gRPC" {
			continue
		}
		recorded = true
		if c.Port > 0 && !seenPort[c.Port] {
			seenPort[c.Port] = true
			ports = append(ports, c.Port)
		}
		key := c.Method + " " + c.Path
		if c.Path == "" || seenPath[key] {
			continue
		}
		seenPath[key] = true
		status := c.Status
		if status == 0 {
			status = 200
		}
		routes = append(routes, fakeRoute{Method: c.Method, Path: c.Path, Status: status})
	}
	sort.Ints(ports)
	return routes, ports, recorded
}

// stubStatus is the recorded HTTP status for method+path against calls to
// host, or 0 when nothing matches (the stub answers 404 and fidelity notes
// unmatched calls).
func stubStatus(host string, calls []capture.Call, method, path string) int {
	host = strings.ToLower(host)
	for _, c := range calls {
		if strings.ToLower(c.Host) != host || c.Method != method || c.Method == "gRPC" {
			continue
		}
		if pathMatch(c.Path, path) {
			if c.Status == 0 {
				return 200
			}
			return c.Status
		}
	}
	return 0
}

// pathMatch is true when path equals pattern, or pattern's {param} segments
// each cover one non-empty path segment.
func pathMatch(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if !strings.Contains(pattern, "{") {
		return false
	}
	pp, ps := splitPath(pattern), splitPath(path)
	if len(pp) != len(ps) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], "{") && strings.HasSuffix(pp[i], "}") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if pp[i] != ps[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// respondScript is the busybox netcat -e handler: read one HTTP request,
// answer the matching recorded status, or 404 when unmatched.
func respondScript(routes []fakeRoute) string {
	var b strings.Builder
	b.WriteString(`#!/bin/sh
path_match() {
  pat="$1"; path="$2"
  while :; do
    [ -z "$pat" ] && [ -z "$path" ] && return 0
    [ -z "$pat" ] || [ -z "$path" ] && return 1
    pat=${pat#/}; path=${path#/}
    pseg=${pat%%/*}; xseg=${path%%/*}
    if [ "$pseg" = "$pat" ]; then pat=""; else pat=${pat#*/}; fi
    if [ "$xseg" = "$path" ]; then path=""; else path=${path#*/}; fi
    case "$pseg" in
      \{*\}) [ -n "$xseg" ] || return 1 ;;
      *) [ "$pseg" = "$xseg" ] || return 1 ;;
    esac
  done
}
read -r req || exit 0
method=${req%% *}
rest=${req#* }
path=${rest%% *}
while IFS= read -r line; do
  line=${line%$'\r'}
  [ -z "$line" ] && break
done
status=404
`)
	for _, r := range routes {
		if strings.Contains(r.Path, "{") {
			fmt.Fprintf(&b, "if [ \"$method\" = %s ] && path_match %s \"$path\"; then status=%d; fi\n",
				shellQuote(r.Method), shellQuote(r.Path), r.Status)
		} else {
			fmt.Fprintf(&b, "if [ \"$method\" = %s ] && [ \"$path\" = %s ]; then status=%d; fi\n",
				shellQuote(r.Method), shellQuote(r.Path), r.Status)
		}
	}
	b.WriteString(`printf "HTTP/1.1 %s x\r\nContent-Length: 0\r\nConnection: close\r\n\r\n" "$status"
`)
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeName is a DNS-1123 Service name for an outside host.
func fakeName(host string) string {
	var b strings.Builder
	b.WriteString("fake-")
	for _, r := range strings.ToLower(host) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == ':':
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if name == "fake" || name == "fake-" {
		sum := sha256.Sum256([]byte(host))
		return "fake-" + hex.EncodeToString(sum[:8])
	}
	if len(name) > 63 {
		sum := sha256.Sum256([]byte(host))
		return "fake-" + hex.EncodeToString(sum[:8])
	}
	return name
}

// fakeableHost is an outside name the Lab can answer with DNS: not an IP.
func fakeableHost(host string) bool {
	return net.ParseIP(host) == nil && host != ""
}
