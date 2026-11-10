package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

func newCaptureMITMCmd(kf *kube.Flags) *cobra.Command {
	var listen, webhookListen, otlp, tokenFile, caDir, caSecret, namespace, webhookConfig, flagsDir string
	var transparent bool
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

			flags := loadMITMFlags(flagsDir, transparent)
			proxyURL := capture.ProxyURLFor(namespace)
			if webhookListen != "" {
				serviceDNS := fmt.Sprintf("sentinel-capture-mitm.%s.svc", namespace)
				wCert, wKey, err := capture.EnsureWebhookTLS(cmd.Context(), client, namespace, serviceDNS, webhookConfig)
				if err != nil {
					return err
				}
				go func() {
					h := &capture.InjectWebhook{ProxyURL: proxyURL, TrustOnly: flags.trustOnly}
					_ = capture.ListenAndServeTLS(listenAddr(webhookListen), wCert, wKey, h)
				}()
			}

			cfg := capture.MITMConfig{
				Listen: listen, CACertFile: certFile, CAKeyFile: keyFile,
				OTLP: otlp, Token: strings.TrimSpace(string(token)),
				Resolve: func(clientIP string) (string, string, string, bool) {
					return resolvePodWorkload(cmd.Context(), client, clientIP)
				},
			}
			if flags.clientTLS {
				cfg.ClientTLS = func(clientIP string) *tls.Certificate {
					return clientTLSForIP(cmd.Context(), client, clientIP)
				}
			}

			if transparent {
				// Redirect DaemonSet: arm nft only while Settings says so.
				if !flags.transparent {
					_ = capture.ClearRedirect()
					cmd.Println("capture MITM redirect idle (mitmTransparent off)")
					<-cmd.Context().Done()
					return cmd.Context().Err()
				}
				port, err := listenPort(listen)
				if err != nil {
					return err
				}
				if err := capture.ApplyRedirect(port); err != nil {
					return err
				}
				defer func() { _ = capture.ClearRedirect() }()
				if err := capture.ServeTransparentMITM(cfg); err != nil {
					return terminate(err)
				}
				return nil
			}

			if err := capture.ServeMITM(cfg); err != nil {
				return terminate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "8888", "TCP port or address to listen on")
	cmd.Flags().BoolVar(&transparent, "transparent", false, "run as node redirect DaemonSet (nft REDIRECT + original-dst TLS)")
	cmd.Flags().StringVar(&flagsDir, "flags-dir", "", "directory with runtime flags from Settings (trust-only, mitm-client-tls, transparent-enabled)")
	cmd.Flags().StringVar(&webhookListen, "webhook-listen", "", "HTTPS address for the pod inject webhook (empty disables it)")
	cmd.Flags().StringVar(&webhookConfig, "webhook-config", "sentinel-capture-mitm", "MutatingWebhookConfiguration to patch with the serving caBundle")
	cmd.Flags().StringVar(&otlp, "otlp", "http://sentinel-capture:4318/v1/traces", "OTLP HTTP traces URL")
	cmd.Flags().StringVar(&tokenFile, "token-file", "/etc/sentinel-capture/token", "bearer token file for the capture receiver")
	cmd.Flags().StringVar(&caDir, "ca-dir", "/var/run/sentinel-capture-mitm", "directory to write the CA PEM files")
	cmd.Flags().StringVar(&caSecret, "ca-secret", capture.MITMCASecret, "Secret that holds or receives the capture CA")
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace of the CA Secret (defaults to POD_NAMESPACE)")
	return cmd
}

type mitmFlags struct {
	transparent bool
	trustOnly   bool
	clientTLS   bool
}

func loadMITMFlags(dir string, transparentFlag bool) mitmFlags {
	f := mitmFlags{transparent: transparentFlag, trustOnly: transparentFlag}
	if dir == "" {
		return f
	}
	if b, err := os.ReadFile(filepath.Join(dir, "transparent-enabled")); err == nil { //nolint:gosec
		v := strings.TrimSpace(string(b))
		f.transparent = v == "1" || strings.EqualFold(v, "true")
		if f.transparent {
			f.trustOnly = true
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "trust-only")); err == nil { //nolint:gosec
		v := strings.TrimSpace(string(b))
		f.trustOnly = v == "1" || strings.EqualFold(v, "true")
	}
	f.clientTLS = capture.ClientTLSEnabled(dir)
	return f
}

func listenPort(listen string) (uint16, error) {
	a := listenAddr(listen)
	_, port, err := net.SplitHostPort(a)
	if err != nil {
		return 0, err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("bad listen port %q", listen)
	}
	return uint16(p), nil
}

func listenAddr(v string) string {
	if strings.Contains(v, ":") {
		if _, _, err := net.SplitHostPort(v); err == nil {
			return v
		}
	}
	if _, err := strconv.Atoi(v); err == nil {
		return ":" + v
	}
	return v
}

func clientTLSForIP(ctx context.Context, client kubernetes.Interface, clientIP string) *tls.Certificate {
	if clientIP == "" || net.ParseIP(clientIP) == nil {
		return nil
	}
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.podIP=" + clientIP})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	cert, err := capture.ClientTLSFromPod(ctx, client, &pods.Items[0], "")
	if err != nil {
		return nil
	}
	return cert
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
