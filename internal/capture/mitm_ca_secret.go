package capture

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// MITMCASecret is the default Secret name holding the capture MITM CA.
const MITMCASecret = "sentinel-capture-mitm-ca"

// EnsureCASecret loads the capture CA from the named Secret, or mints one and
// creates the Secret when it is missing. Returns cert and key PEM.
func EnsureCASecret(ctx context.Context, client kubernetes.Interface, namespace, name string) (certPEM, keyPEM []byte, err error) {
	if name == "" {
		name = MITMCASecret
	}
	sec, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		certPEM, keyPEM = sec.Data["ca.crt"], sec.Data["ca.key"]
		if len(certPEM) == 0 || len(keyPEM) == 0 {
			return nil, nil, fmt.Errorf("secret %s/%s needs ca.crt and ca.key", namespace, name)
		}
		return certPEM, keyPEM, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	ca, err := NewAuthority()
	if err != nil {
		return nil, nil, err
	}
	certPEM, keyPEM = ca.CertPEM(), ca.KeyPEM()
	_, err = client.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{
			"app.kubernetes.io/name": "sentinel-capture-mitm",
		}},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"ca.crt": certPEM, "ca.key": keyPEM},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return EnsureCASecret(ctx, client, namespace, name)
	}
	return certPEM, keyPEM, err
}
