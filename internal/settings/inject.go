package settings

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capture"
)

// PolicyResource is the SentinelPolicy GVR.
var PolicyResource = v1alpha1.GroupVersion.WithResource("sentinelpolicies")

// syncMITMInject publishes the capture CA into body-capturing namespaces and
// labels them for the inject webhook when MitmInject is on.
func syncMITMInject(ctx context.Context, client kubernetes.Interface, dyn dynamic.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	if dyn == nil || !probe(ctx, client, t).CaptureMITM {
		return nil
	}
	on := spec.Capture.MitmInject != nil && *spec.Capture.MitmInject &&
		spec.Capture.MitmEnabled != nil && *spec.Capture.MitmEnabled
	want, err := bodyCaptureNamespaces(ctx, dyn)
	if err != nil {
		return err
	}
	labeled, err := injectLabeledNamespaces(ctx, client)
	if err != nil {
		return err
	}
	wantSet := map[string]bool{}
	for _, ns := range want {
		wantSet[ns] = true
	}
	for _, ns := range labeled {
		if on && wantSet[ns] {
			continue
		}
		_ = capture.LabelNamespaceForInject(ctx, client, ns, false)
		_ = capture.DeleteTrustConfigMap(ctx, client, ns)
	}
	if !on {
		return nil
	}
	certPEM, p12, err := loadPublishedTrust(ctx, client, t.CaptureNamespace)
	if err != nil {
		return err
	}
	if certPEM == nil {
		// capture-mitm mints the CA on start; retry on the next Settings apply.
		return nil
	}
	if err := capture.SyncTrustConfigMap(ctx, client, t.CaptureNamespace, certPEM, p12); err != nil {
		return fmt.Errorf("capture trust configmap: %w", err)
	}
	for _, ns := range want {
		if err := capture.SyncTrustConfigMap(ctx, client, ns, certPEM, p12); err != nil {
			return fmt.Errorf("trust configmap in %s: %w", ns, err)
		}
		if err := capture.LabelNamespaceForInject(ctx, client, ns, true); err != nil {
			return fmt.Errorf("label %s for mitm inject: %w", ns, err)
		}
	}
	return nil
}

func injectLabeledNamespaces(ctx context.Context, client kubernetes.Interface) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: capture.InjectAnnotation + "=" + capture.InjectValue,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	return out, nil
}

func loadPublishedTrust(ctx context.Context, client kubernetes.Interface, namespace string) (certPEM, p12 []byte, err error) {
	sec, err := client.CoreV1().Secrets(namespace).Get(ctx, capture.MITMCASecret, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	certPEM = sec.Data["ca.crt"]
	p12 = sec.Data["ca.p12"]
	if len(certPEM) == 0 {
		return nil, nil, fmt.Errorf("secret %s/%s missing ca.crt", namespace, capture.MITMCASecret)
	}
	if len(p12) == 0 {
		ca, err := capture.AuthorityFromPEM(certPEM, sec.Data["ca.key"])
		if err != nil {
			return nil, nil, err
		}
		p12, err = ca.TrustStorePKCS12()
		if err != nil {
			return nil, nil, err
		}
	}
	return certPEM, p12, nil
}

func bodyCaptureNamespaces(ctx context.Context, dyn dynamic.Interface) ([]string, error) {
	list, err := dyn.Resource(PolicyResource).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list SentinelPolicies: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range list.Items {
		ns := item.GetNamespace()
		if ns == "" || seen[ns] {
			continue
		}
		bodies, found, _ := unstructuredBool(item.Object, "spec", "capture", "bodies")
		if !found || !bodies {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out, nil
}

func unstructuredBool(obj map[string]any, keys ...string) (val, found bool, err error) {
	cur := any(obj)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, false, nil
		}
		cur, ok = m[k]
		if !ok {
			return false, false, nil
		}
	}
	b, ok := cur.(bool)
	return b, ok, nil
}
