package settings

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capture"
)

func syncMITMRuntime(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	transparent := spec.Capture.MitmTransparent != nil && *spec.Capture.MitmTransparent
	clientTLS := spec.Capture.MitmClientTLS != nil && *spec.Capture.MitmClientTLS
	enabled := spec.Capture.MitmEnabled != nil && *spec.Capture.MitmEnabled
	// Transparent implies trust-only inject (no HTTPS_PROXY).
	trustOnly := transparent
	data := map[string]string{
		"transparent":              strconv.FormatBool(transparent && enabled),
		"clientTLS":                strconv.FormatBool(clientTLS && enabled),
		"trustOnly":                strconv.FormatBool(trustOnly),
		capture.ClientTLSEnabledFile: boolFile(clientTLS && enabled),
		"trust-only":               boolFile(trustOnly),
		"transparent-enabled":      boolFile(transparent && enabled),
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      t.CaptureMITMRuntimeCM,
			Namespace: t.CaptureNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "sentinel-capture-mitm",
			},
		},
		Data: data,
	}
	cm, err := client.CoreV1().ConfigMaps(t.CaptureNamespace).Get(ctx, t.CaptureMITMRuntimeCM, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(t.CaptureNamespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create mitm runtime: %w", err)
		}
	} else if err != nil {
		return err
	} else {
		cm.Data = data
		if _, err := client.CoreV1().ConfigMaps(t.CaptureNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"}); err != nil {
			return fmt.Errorf("update mitm runtime: %w", err)
		}
	}
	// Bounce redirect DaemonSet so it reloads flags / reinstalls nft rules.
	if probe(ctx, client, t).CaptureMITMTransparent {
		ds, err := client.AppsV1().DaemonSets(t.CaptureNamespace).Get(ctx, t.CaptureMITMTransparent, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if ds.Spec.Template.Annotations == nil {
			ds.Spec.Template.Annotations = map[string]string{}
		}
		ds.Spec.Template.Annotations["sentinel.karimzakzouk.github.io/settings"] = fmt.Sprintf("t=%v,c=%v", transparent, clientTLS)
		if _, err := client.AppsV1().DaemonSets(t.CaptureNamespace).Update(ctx, ds, metav1.UpdateOptions{FieldManager: "sentinel-settings"}); err != nil {
			return fmt.Errorf("bounce mitm redirect: %w", err)
		}
	}
	return nil
}

func boolFile(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
