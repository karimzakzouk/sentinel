package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

func newCaptureMITMCmd(kf *kube.Flags) *cobra.Command {
	var listen, webhookListen, otlp, tokenFile, caDir, caSecret, namespace, webhookConfig string
	cmd := &cobra.Command{
		Use:    "capture-mitm",
		Short:  "Terminate HTTPS for opted-in clients and post bodies to Sentinel's capture receiver",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := os.ReadFile(tokenFile) //nolint:gosec // mounted capture token
			if err != nil {
				return fmt.Errorf("capture token: %w", err)
			}
			rc, err := kf.RESTConfig(version.UserAgent("capture-mitm"))
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(rc)
			if err != nil {
				return err
			}
			if namespace == "" {
				namespace = os.Getenv("POD_NAMESPACE")
			}
			if namespace == "" {
				return fmt.Errorf("capture-mitm needs --namespace or POD_NAMESPACE")
			}
			certPEM, keyPEM, p12, err := capture.EnsureTrustMaterial(cmd.Context(), client, namespace, caSecret)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(caDir, 0o700); err != nil {
				return err
			}
			certFile := filepath.Join(caDir, "ca.crt")
			keyFile := filepath.Join(caDir, "ca.key")
			if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
				return err
			}
			_ = os.WriteFile(filepath.Join(caDir, "ca.p12"), p12, 0o600)
			if err := capture.SyncTrustConfigMap(cmd.Context(), client, namespace, certPEM, p12); err != nil {
				return err
			}

			proxyURL := capture.ProxyURLFor(namespace)
			if webhookListen != "" {
				serviceDNS := fmt.Sprintf("sentinel-capture-mitm.%s.svc", namespace)
				wCert, wKey, err := capture.EnsureWebhookTLS(cmd.Context(), client, namespace, serviceDNS, webhookConfig)
				if err != nil {
					return err
				}
				go func() {
					h := &capture.InjectWebhook{ProxyURL: proxyURL}
					_ = capture.ListenAndServeTLS(listenAddr(webhookListen), wCert, wKey, h)
				}()
			}

			if err := capture.ServeMITM(capture.MITMConfig{
				Listen: listen, CACertFile: certFile, CAKeyFile: keyFile,
				OTLP: otlp, Token: strings.TrimSpace(string(token)),
				Resolve: func(clientIP string) (string, string, string, bool) {
					return resolvePodWorkload(cmd.Context(), client, clientIP)
				},
			}); err != nil {
				return terminate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "8888", "TCP port or address to listen on")
	cmd.Flags().StringVar(&webhookListen, "webhook-listen", "", "HTTPS address for the pod inject webhook (empty disables it)")
	cmd.Flags().StringVar(&webhookConfig, "webhook-config", "sentinel-capture-mitm", "MutatingWebhookConfiguration to patch with the serving caBundle")
	cmd.Flags().StringVar(&otlp, "otlp", "http://sentinel-capture:4318/v1/traces", "OTLP HTTP traces URL")
	cmd.Flags().StringVar(&tokenFile, "token-file", "/etc/sentinel-capture/token", "bearer token file for the capture receiver")
	cmd.Flags().StringVar(&caDir, "ca-dir", "/var/run/sentinel-capture-mitm", "directory to write the CA PEM files")
	cmd.Flags().StringVar(&caSecret, "ca-secret", capture.MITMCASecret, "Secret that holds or receives the capture CA")
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace of the CA Secret (defaults to POD_NAMESPACE)")
	return cmd
}

func listenAddr(v string) string {
	if strings.Contains(v, ":") {
		return v
	}
	return ":" + v
}

func resolvePodWorkload(ctx context.Context, client kubernetes.Interface, clientIP string) (ns, kind, name string, ok bool) {
	if clientIP == "" || net.ParseIP(clientIP) == nil {
		return "", "", "", false
	}
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.podIP=" + clientIP})
	if err != nil || len(pods.Items) == 0 {
		return "", "", "", false
	}
	pod := pods.Items[0]
	ns = pod.Namespace
	for _, ref := range pod.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		switch ref.Kind {
		case "ReplicaSet":
			rs, err := client.AppsV1().ReplicaSets(ns).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return ns, "ReplicaSet", ref.Name, true
			}
			for _, o := range rs.OwnerReferences {
				if o.Controller != nil && *o.Controller && o.Kind == "Deployment" {
					return ns, "Deployment", o.Name, true
				}
			}
			return ns, "ReplicaSet", ref.Name, true
		case "StatefulSet", "DaemonSet":
			return ns, ref.Kind, ref.Name, true
		}
	}
	return ns, "Pod", pod.Name, true
}
