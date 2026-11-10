package lab

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capture"
)

// FakeMITM marks the Lab CONNECT proxy that terminates TLS with the Lab CA
// and answers recorded routes with bodies, without dialling outside the seal.
const FakeMITM = "sentinel.karimzakzouk.github.io/fake-mitm"

// MITMName is the Lab Service and pod that run the HTTPS MITM proxy.
const MITMName = "lab-mitm"

// MITMPort is the CONNECT listen port inside the Lab.
const MITMPort = 8888

// MITMProxyURL is where copies that already use an HTTP(S) proxy are pointed.
const MITMProxyURL = "http://lab-mitm:8888"

// mitmListenScript prefers Sentinel's lab-mitm (SealImage) and otherwise keeps
// the port open so readiness passes; per-host HTTPS fakes still cover direct
// clients when the MITM binary is absent.
const mitmListenScript = `set -e
if [ -x /sentinel ]; then
  while true; do /sentinel lab-mitm --ca-cert=/etc/lab-ca/ca.crt --ca-key=/etc/lab-ca/ca.key --routes=/etc/lab-mitm/routes --listen=8888 || true; done
fi
while true; do busybox nc -l -p 8888 -w 1 -e /bin/true; done
`

// proxyEnvNames are the variables clients use for an outbound HTTP proxy.
var proxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

// mitmRoute is one recorded answer keyed by the outside host the client asked for.
type mitmRoute struct {
	Host         string
	Method, Path string
	Status       int
	Body         string
}

// ServeMITM runs an HTTP CONNECT proxy that terminates TLS with certificates
// issued by the Lab CA and answers recorded method+path statuses (and bodies)
// for those hosts. It never dials outside the Lab: unmatched hosts and paths
// get an empty 404 after the handshake. listen is a port or host:port.
func ServeMITM(listen, caCertFile, caKeyFile, routesFile string) error {
	if listen == "" {
		return fmt.Errorf("lab-mitm needs --listen")
	}
	if _, err := strconv.Atoi(listen); err == nil {
		listen = ":" + listen
	}
	certPEM, err := os.ReadFile(caCertFile) //nolint:gosec // Lab stub mounts the Lab CA Secret
	if err != nil {
		return fmt.Errorf("read Lab CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(caKeyFile) //nolint:gosec // Lab stub mounts the Lab CA Secret
	if err != nil {
		return fmt.Errorf("read Lab CA key: %w", err)
	}
	ca, err := AuthorityFromPEM(certPEM, keyPEM)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(routesFile) //nolint:gosec // path is the Lab stub's mounted ConfigMap
	if err != nil {
		return fmt.Errorf("read MITM routes: %w", err)
	}
	byHost := groupMITMRoutes(parseMITMRoutes(string(raw)))
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{
		Handler:           mitmHandler(ca, byHost),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.Serve(ln)
}

// mitmHandler answers CONNECT by terminating TLS as the requested host, then
// serving the recorded routes for that host. Plain absolute-form HTTP is
// answered the same way for clients that proxy cleartext.
func mitmHandler(ca *Authority, byHost map[string][]fakeRoute) http.Handler {
	certs := &mitmCertCache{ca: ca}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			handleMITMConnect(w, r, certs, byHost)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(host)
		fakeHTTPHandler(byHost[host]).ServeHTTP(w, r)
	})
}

func handleMITMConnect(w http.ResponseWriter, r *http.Request, certs *mitmCertCache, byHost map[string][]fakeRoute) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		http.Error(w, "CONNECT needs a host", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, bufrw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}
	raw := client
	if bufrw.Reader.Buffered() > 0 {
		raw = &bufConn{Conn: client, r: bufrw.Reader}
	}
	tlsConn := tls.Server(raw, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := host
			if chi.ServerName != "" {
				name = strings.ToLower(chi.ServerName)
			}
			return certs.certificate(name)
		},
	})
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	defer func() { _ = tlsConn.Close() }()
	serveMITMOnce(tlsConn, fakeHTTPHandler(byHost[host]))
}

// serveMITMOnce reads one HTTP request from the decrypted CONNECT tunnel and
// writes the fake's response. Connection: close matches the stub handlers.
func serveMITMOnce(conn net.Conn, handler http.Handler) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	rw := &mitmResponse{conn: conn, header: make(http.Header)}
	handler.ServeHTTP(rw, req)
	_ = rw.flush()
}

// bufConn keeps buffered bytes left after hijacking the CONNECT request.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// mitmResponse is a minimal ResponseWriter over a hijacked TLS connection.
type mitmResponse struct {
	conn        net.Conn
	header      http.Header
	status      int
	wroteHeader bool
	buf         []byte
}

func (m *mitmResponse) Header() http.Header { return m.header }

func (m *mitmResponse) Write(p []byte) (int, error) {
	if !m.wroteHeader {
		m.WriteHeader(http.StatusOK)
	}
	m.buf = append(m.buf, p...)
	return len(p), nil
}

func (m *mitmResponse) WriteHeader(status int) {
	if m.wroteHeader {
		return
	}
	m.wroteHeader = true
	m.status = status
}

func (m *mitmResponse) flush() error {
	if !m.wroteHeader {
		m.WriteHeader(http.StatusOK)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d x\r\n", m.status)
	if _, ok := m.header["Content-Length"]; !ok {
		m.header.Set("Content-Length", strconv.Itoa(len(m.buf)))
	}
	_ = m.header.Write(&b)
	b.WriteString("\r\n")
	b.Write(m.buf)
	_, err := io.WriteString(m.conn, b.String())
	return err
}

type mitmCertCache struct {
	ca    *Authority
	mu    sync.Mutex
	certs map[string]*tls.Certificate
}

func (c *mitmCertCache) certificate(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.certs == nil {
		c.certs = map[string]*tls.Certificate{}
	}
	if cert, ok := c.certs[host]; ok {
		return cert, nil
	}
	certPEM, keyPEM, err := c.ca.Issue(host)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	c.certs[host] = &cert
	return &cert, nil
}

// mitmRoutesFile lists host method path status [body] lines for lab-mitm.
func mitmRoutesFile(routes []mitmRoute) string {
	var b strings.Builder
	for _, r := range routes {
		fr := fakeRoute{Method: r.Method, Path: r.Path, Status: r.Status, Body: r.Body}
		line := routesFile([]fakeRoute{fr})
		fmt.Fprintf(&b, "%s %s", r.Host, strings.TrimSuffix(line, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}

// parseMITMRoutes reads the multi-host routes file lab-mitm mounts.
func parseMITMRoutes(data string) []mitmRoute {
	var out []mitmRoute
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		status, err := strconv.Atoi(parts[3])
		if err != nil {
			continue
		}
		route := mitmRoute{Host: strings.ToLower(parts[0]), Method: parts[1], Path: parts[2], Status: status}
		if len(parts) >= 5 {
			if raw, err := decodeRouteBody(parts[4]); err == nil {
				route.Body = raw
			}
		}
		out = append(out, route)
	}
	return out
}

func decodeRouteBody(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func groupMITMRoutes(routes []mitmRoute) map[string][]fakeRoute {
	out := map[string][]fakeRoute{}
	for _, r := range routes {
		out[r.Host] = append(out[r.Host], fakeRoute{Method: r.Method, Path: r.Path, Status: r.Status, Body: r.Body})
	}
	return out
}

// mitmRoutesFromCalls builds MITM routes for HTTPS hosts from captured calls.
func mitmRoutesFromCalls(calls []capture.Call, hosts map[string]bool) []mitmRoute {
	seen := map[string]bool{}
	var out []mitmRoute
	for _, c := range calls {
		host := strings.ToLower(c.Host)
		if host == "" || c.Method == "" || c.Path == "" || c.Method == "gRPC" {
			continue
		}
		if len(hosts) > 0 && !hosts[host] {
			continue
		}
		if c.Port > 0 && !needsTLSPort(c.Port) {
			continue
		}
		key := host + " " + c.Method + " " + c.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		status := c.Status
		if status == 0 {
			status = 200
		}
		out = append(out, mitmRoute{Host: host, Method: c.Method, Path: c.Path, Status: status, Body: c.ResponseBody})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// mitmOf builds the Lab CONNECT proxy Service, ConfigMap and pod. The stub
// mounts the shared Lab CA Secret and never dials outside the seal.
func mitmOf(ns string, routes []mitmRoute) (*corev1.ConfigMap, *corev1.Service, *corev1.Pod) {
	name := MITMName
	cm := &corev1.ConfigMap{
		ObjectMeta: worldMeta(ns, name, nil),
		Data:       map[string]string{"routes": mitmRoutesFile(routes)},
	}
	labels := map[string]string{"app": name}
	meta := worldMeta(ns, name, labels)
	meta.Annotations = map[string]string{FakeMITM: "true"}
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
				Name:    "mitm",
				Image:   Image,
				Command: []string{"sh", "-c", mitmListenScript},
				Ports:   []corev1.ContainerPort{{ContainerPort: MITMPort, Protocol: corev1.ProtocolTCP}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(MITMPort)}},
					PeriodSeconds: 2},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "routes", MountPath: "/etc/lab-mitm", ReadOnly: true},
					{Name: "ca", MountPath: "/etc/lab-ca", ReadOnly: true},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "routes", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
				{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: LabCAName, DefaultMode: ptr.To[int32](0440)}}},
			},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: worldMeta(ns, name, nil),
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{
			Name: "proxy", Protocol: corev1.ProtocolTCP, Port: MITMPort, TargetPort: intstr.FromInt32(MITMPort),
		}}},
	}
	return cm, svc, pod
}

// rewriteProxyEnv points production HTTP(S)_PROXY values at the Lab MITM so
// TLS clients that already use a proxy decrypt inside the seal. Unset proxy
// variables stay unset: direct Lab DNS + trust (decision 111) remains enough.
func rewriteProxyEnv(spec *corev1.PodSpec, note func(string, ...any), ref interface{ String() string }) {
	if spec == nil {
		return
	}
	rewrote := false
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			c := &cs[i]
			for j := range c.Env {
				if !isProxyEnv(c.Env[j].Name) || c.Env[j].Value == "" {
					continue
				}
				c.Env[j].Value = MITMProxyURL
				c.Env[j].ValueFrom = nil
				rewrote = true
			}
		}
	}
	if rewrote {
		note("%s's HTTP(S)_PROXY now points at the Lab MITM (%s), which terminates TLS with the Lab CA and answers recorded bodies without leaving the seal",
			ref, MITMProxyURL)
	}
}

func isProxyEnv(name string) bool {
	for _, n := range proxyEnvNames {
		if name == n {
			return true
		}
	}
	return false
}
