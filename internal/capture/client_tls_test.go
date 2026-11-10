package capture

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClientTLSFromPodUsesAnnotationSecret(t *testing.T) {
	certPEM, keyPEM := mustSelfSigned(t)
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-tls", Namespace: "shop"},
		Data:       map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "p", Namespace: "shop",
		Annotations: map[string]string{ClientTLSAnnotation: "app-tls"},
	}}
	got, err := ClientTLSFromPod(context.Background(), client, pod, "")
	if err != nil || got == nil {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestInjectMITMTrustOnlySkipsProxy(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	InjectMITMOpts(spec, InjectOptions{TrustOnly: true})
	for _, e := range spec.Containers[0].Env {
		if e.Name == "HTTPS_PROXY" || e.Name == "HTTP_PROXY" {
			t.Fatalf("unexpected proxy env %#v", spec.Containers[0].Env)
		}
	}
	if !hasEnv(spec.Containers[0].Env, "SSL_CERT_FILE") {
		t.Fatal("expected trust env")
	}
}

func mustSelfSigned(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
	return certPEM, keyPEM
}
