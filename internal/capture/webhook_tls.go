package capture

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// WebhookTLSSecret holds the serving cert for the inject mutating webhook.
const WebhookTLSSecret = "sentinel-capture-mitm-webhook"

// EnsureWebhookTLS mints or loads a serving certificate for the inject webhook
// and patches the MutatingWebhookConfiguration's caBundle when name is set.
func EnsureWebhookTLS(ctx context.Context, client kubernetes.Interface, namespace, serviceDNS, webhookConfig string) (certPEM, keyPEM []byte, err error) {
	name := WebhookTLSSecret
	sec, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil && len(sec.Data["tls.crt"]) > 0 && len(sec.Data["tls.key"]) > 0 {
		certPEM, keyPEM = sec.Data["tls.crt"], sec.Data["tls.key"]
	} else {
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		certPEM, keyPEM, err = mintWebhookCert(serviceDNS)
		if err != nil {
			return nil, nil, err
		}
		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{
				"app.kubernetes.io/name": "sentinel-capture-mitm",
			}},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
		}
		if apierrors.IsNotFound(err) {
			_, err = client.CoreV1().Secrets(namespace).Create(ctx, desired, metav1.CreateOptions{})
		} else {
			sec.Data = desired.Data
			sec.Type = desired.Type
			_, err = client.CoreV1().Secrets(namespace).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "sentinel-capture-mitm"})
		}
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, nil, err
		}
	}
	if webhookConfig != "" {
		bundle := base64.StdEncoding.EncodeToString(certPEM)
		patch := fmt.Appendf(nil, `{"webhooks":[{"name":"capture-mitm.sentinel.karimzakzouk.github.io","clientConfig":{"caBundle":%q}}]}`, bundle)
		_, err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Patch(ctx, webhookConfig, types.StrategicMergePatchType, patch, metav1.PatchOptions{FieldManager: "sentinel-capture-mitm"})
		if err != nil && !apierrors.IsNotFound(err) {
			return certPEM, keyPEM, fmt.Errorf("patch webhook caBundle: %w", err)
		}
	}
	return certPEM, keyPEM, nil
}

func mintWebhookCert(dnsName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsName, Organization: []string{"sentinel-capture-mitm"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{dnsName, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pemEncodeEC(key), nil
}

// ListenAndServeTLS starts the inject webhook HTTPS server with the given PEMs.
func ListenAndServeTLS(addr string, certPEM, keyPEM []byte, handler http.Handler) error {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, TLSConfig: cfg}
	return server.Serve(tls.NewListener(ln, cfg))
}
