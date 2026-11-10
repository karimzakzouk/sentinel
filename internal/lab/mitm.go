package lab

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// MITMName is the Lab Service/pod that terminates HTTPS from copies and
// dials the Lab fake (or other sealed destination) with the same name.
const MITMName = "lab-mitm"

// MITMPort is where copies point HTTPS_PROXY.
const MITMPort = 8080

// ServeMITM runs an HTTP CONNECT proxy that presents Lab-CA certificates to
// clients and dials the requested host:port inside the Lab (where DNS already
// answers fake hosts). TLS-only clients that honour HTTPS_PROXY and trust the
// Lab CA then have the proxy decrypt and re-encrypt to the fake, so body-aware
// HTTPS stubs stay reachable without a production MITM.
func ServeMITM(listen, caCertFile, caKeyFile string) error {
	if listen == "" {
		return fmt.Errorf("lab-mitm needs --listen")
	}
	certPEM, err := os.ReadFile(caCertFile) //nolint:gosec // Lab mounts its own CA Secret
	if err != nil {
		return fmt.Errorf("Lab CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(caKeyFile) //nolint:gosec
	if err != nil {
		return fmt.Errorf("Lab CA key: %w", err)
	}
	ca, err := authorityFromPEM(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if !strings.Contains(listen, ":") {
		listen = ":" + listen
	}
	s := &http.Server{
		Addr:              listen,
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect {
				http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
				return
			}
			host, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				host, port = r.Host, "443"
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			client, _, err := hj.Hijack()
			if err != nil {
				return
			}
			defer client.Close()
			if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				return
			}
			leafCert, leafKey, err := ca.Issue(host)
			if err != nil {
				return
			}
			tlsCert, err := tls.X509KeyPair(leafCert, leafKey)
			if err != nil {
				return
			}
			tlsClient := tls.Server(client, &tls.Config{
				Certificates: []tls.Certificate{tlsCert},
				MinVersion:   tls.VersionTLS12,
				NextProtos:   []string{"http/1.1"},
			})
			if err := tlsClient.Handshake(); err != nil {
				return
			}
			defer tlsClient.Close()
			upstream, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, port),
				&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, ServerName: host}) //nolint:gosec // Lab dials its own fakes
			if err != nil {
				return
			}
			defer upstream.Close()
			errc := make(chan error, 2)
			go func() { _, e := io.Copy(upstream, tlsClient); errc <- e }()
			go func() { _, e := io.Copy(tlsClient, upstream); errc <- e }()
			<-errc
		}),
	}
	return s.ListenAndServe()
}

// mitmOf builds the Lab CONNECT proxy that decrypts HTTPS from copies with
// the Lab CA and dials the fake (or other sealed host) by name.
func mitmOf(ns string) *Fake {
	labels := map[string]string{Label: WorldRole, "app": MITMName}
	meta := worldMeta(ns, MITMName, labels)
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
				Name: "mitm", Image: Image, Command: []string{"/sentinel", "lab-mitm",
					"--listen=:" + fmt.Sprint(MITMPort),
					"--ca-cert=/etc/lab-ca/ca.crt", "--ca-key=/etc/lab-ca/ca.key"},
				Ports: []corev1.ContainerPort{{ContainerPort: int32(MITMPort)}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(int32(MITMPort))}},
					PeriodSeconds: 2},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "ca", MountPath: "/etc/lab-ca", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "ca", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: LabCAName, DefaultMode: ptr.To[int32](0440)}}}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: worldMeta(ns, MITMName, labels),
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{
			Name: "proxy", Port: int32(MITMPort), TargetPort: intstr.FromInt32(int32(MITMPort))}}},
	}
	return &Fake{Host: MITMName, Service: svc, Pod: pod}
}

func authorityFromPEM(certPEM, keyPEM []byte) (*Authority, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("decode Lab CA certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("decode Lab CA key")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &Authority{cert: cert, key: key, certPEM: append([]byte(nil), certPEM...), keyPEM: append([]byte(nil), keyPEM...)}, nil
}
