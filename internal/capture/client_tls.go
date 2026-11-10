package capture

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClientTLSAnnotation names a Secret in the pod's namespace that holds a client
// certificate (tls.crt/tls.key or client.crt/client.key) the MITM may use when
// dialing upstream. Opt-in only: Settings capture.mitmClientTLS must be on.
const ClientTLSAnnotation = "sentinel.karimzakzouk.github.io/client-tls-secret"

// ClientTLSFromPod loads an operator-opted client certificate for MITM upstream
// dials. Empty secret name falls back to the pod annotation.
func ClientTLSFromPod(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, secretName string) (*tls.Certificate, error) {
	if pod == nil {
		return nil, fmt.Errorf("no pod")
	}
	if secretName == "" {
		secretName = pod.Annotations[ClientTLSAnnotation]
	}
	if secretName == "" {
		secretName = guessTLSSecret(pod)
	}
	if secretName == "" {
		return nil, nil
	}
	sec, err := client.CoreV1().Secrets(pod.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM := tlsPEM(sec.Data)
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, fmt.Errorf("secret %s/%s needs a certificate and key", pod.Namespace, secretName)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

func tlsPEM(data map[string][]byte) (cert, key []byte) {
	for _, ck := range []string{"tls.crt", "cert.pem", "client.crt", "certificate"} {
		if v := data[ck]; len(v) > 0 {
			cert = v
			break
		}
	}
	for _, kk := range []string{"tls.key", "key.pem", "client.key", "privatekey", "private_key"} {
		if v := data[kk]; len(v) > 0 {
			key = v
			break
		}
	}
	return cert, key
}

func guessTLSSecret(pod *corev1.Pod) string {
	for _, v := range pod.Spec.Volumes {
		if v.Secret == nil {
			continue
		}
		n := strings.ToLower(v.Name + v.Secret.SecretName)
		if strings.Contains(n, "tls") || strings.Contains(n, "mtls") || strings.Contains(n, "client") {
			return v.Secret.SecretName
		}
	}
	return ""
}

// ClientTLSEnabledFile is written by Settings apply so capture-mitm pods can
// see the runtime toggle without an API watch.
const ClientTLSEnabledFile = "mitm-client-tls"

// ClientTLSEnabled reports whether the mounted settings flag is on.
func ClientTLSEnabled(dir string) bool {
	b, err := os.ReadFile(dir + "/" + ClientTLSEnabledFile) //nolint:gosec // operator-mounted flag
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(b))
	return v == "1" || strings.EqualFold(v, "true")
}
