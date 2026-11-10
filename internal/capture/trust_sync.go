package capture

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EnsureTrustMaterial loads or mints the capture CA and returns PEM plus PKCS#12.
func EnsureTrustMaterial(ctx context.Context, client kubernetes.Interface, namespace, secretName string) (certPEM, keyPEM, p12 []byte, err error) {
	certPEM, keyPEM, err = EnsureCASecret(ctx, client, namespace, secretName)
	if err != nil {
		return nil, nil, nil, err
	}
	ca, err := AuthorityFromPEM(certPEM, keyPEM)
	if err != nil {
		return nil, nil, nil, err
	}
	p12, err = ca.TrustStorePKCS12()
	if err != nil {
		return nil, nil, nil, err
	}
	// Keep ca.p12 beside the CA so operators can copy it without reminting.
	sec, err := client.CoreV1().Secrets(namespace).Get(ctx, cmpOr(secretName, MITMCASecret), metav1.GetOptions{})
	if err != nil {
		return certPEM, keyPEM, p12, nil
	}
	if string(sec.Data["ca.p12"]) == string(p12) {
		return certPEM, keyPEM, p12, nil
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data["ca.p12"] = p12
	_, err = client.CoreV1().Secrets(namespace).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "sentinel-capture-mitm"})
	return certPEM, keyPEM, p12, err
}

// SyncTrustConfigMap writes ca.crt and ca.p12 into namespace as TrustConfigMap.
func SyncTrustConfigMap(ctx context.Context, client kubernetes.Interface, namespace string, certPEM, p12 []byte) error {
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TrustConfigMap,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sentinel-capture-mitm",
				"app.kubernetes.io/component":  "capture-mitm-ca",
				InjectAnnotation:               InjectValue,
			},
		},
		BinaryData: map[string][]byte{"ca.crt": certPEM, "ca.p12": p12},
	}
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, TrustConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	cm.BinaryData = desired.BinaryData
	cm.Data = nil
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	for k, v := range desired.Labels {
		cm.Labels[k] = v
	}
	_, err = client.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

// LabelNamespaceForInject sets or clears the namespace label the webhook matches.
func LabelNamespaceForInject(ctx context.Context, client kubernetes.Interface, namespace string, on bool) error {
	ns, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	cur, ok := ns.Labels[InjectAnnotation]
	if on && cur == InjectValue {
		return nil
	}
	if !on && !ok {
		return nil
	}
	if on {
		ns.Labels[InjectAnnotation] = InjectValue
	} else {
		delete(ns.Labels, InjectAnnotation)
	}
	_, err = client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// DeleteTrustConfigMap removes a copied CA ConfigMap when inject is turned off.
func DeleteTrustConfigMap(ctx context.Context, client kubernetes.Interface, namespace string) error {
	err := client.CoreV1().ConfigMaps(namespace).Delete(ctx, TrustConfigMap, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ProxyURLFor builds the in-cluster MITM proxy URL for a capture namespace.
func ProxyURLFor(captureNamespace string) string {
	if captureNamespace == "" {
		captureNamespace = "sentinel-capture"
	}
	return fmt.Sprintf("http://sentinel-capture-mitm.%s.svc:8888", captureNamespace)
}
