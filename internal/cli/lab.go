package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/lab"
	"github.com/karimzakzouk/sentinel/internal/lead"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// LabConfig is the Lab's own configuration.
type LabConfig struct {
	// Pool lists the Lab namespaces created at install.
	Pool []string `json:"pool"`
	// Twins are the watched namespaces, which no Lab may reach.
	Twins []string `json:"twins"`
	// ClusterDomain is the cluster's DNS domain.
	ClusterDomain string `json:"clusterDomain,omitempty"`
	// Seal is how the Lab seals its namespaces: policy, firewall, or auto
	// (the default), which uses policy where the cluster enforces it.
	Seal string `json:"seal,omitempty"`
	// SealImage is the firewall's image, Sentinel's own, for firewall and
	// auto.
	SealImage  string `json:"sealImage,omitempty"`
	HealthAddr string `json:"healthAddr,omitempty"`
	// LeaderElection, when set, has replicas hold this Lease in turn; only
	// its holder works.
	LeaderElection *lead.Config `json:"leaderElection,omitempty"`
}

func loadLabConfig(path string) (LabConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's command line
	if err != nil {
		return LabConfig{}, err
	}
	var c LabConfig
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return LabConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Pool) == 0 {
		return LabConfig{}, errors.New("pool is empty: list the Lab namespaces")
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8081"
	}
	if c.ClusterDomain == "" {
		c.ClusterDomain = "cluster.local"
	}
	switch c.Seal {
	case "":
		c.Seal = lab.SealAuto
	case lab.SealAuto, lab.SealPolicy:
	case lab.SealFirewall:
		if c.SealImage == "" {
			return LabConfig{}, fmt.Errorf("%s: seal is firewall, but no sealImage names the firewall's image", path)
		}
	default:
		return LabConfig{}, fmt.Errorf("%s: seal is %q, not policy, firewall or auto", path, c.Seal)
	}
	return c, nil
}

func newLabSealCmd() *cobra.Command {
	var allows []string
	cmd := &cobra.Command{
		Use:    "lab-seal",
		Short:  "Seal the network of the Lab pod this runs in, so it may open connections only to the destinations named",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var allow []lab.Allow
			for _, s := range allows {
				a, err := lab.ParseAllow(s)
				if err != nil {
					return terminate(err)
				}
				allow = append(allow, a)
			}
			n, err := lab.ApplySeal(allow)
			if err != nil {
				return terminate(err)
			}
			msg := fmt.Sprintf("sealed: %d rules; new connections may go only to %d destinations", n, len(allow))
			_ = os.WriteFile("/dev/termination-log", []byte(msg), 0o600)
			_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
			return err
		},
	}
	cmd.Flags().StringArrayVar(&allows, "allow", nil, "a destination new connections may go to: tcp/ADDRESS:PORT or udp/ADDRESS:PORT")
	return cmd
}

func newLabGRPCFakeCmd() *cobra.Command {
	var routes, listen string
	cmd := &cobra.Command{
		Use:    "lab-grpc-fake",
		Short:  "Answer recorded gRPC statuses and optional response bodies for a Lab fake of an outside host",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := lab.ServeFakeGRPC(listen, routes); err != nil {
				return terminate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&routes, "routes", "/etc/lab-fake/grpc-routes", "recorded RPC path, gRPC status code, and optional base64 body lines")
	cmd.Flags().StringVar(&listen, "listen", "", "TCP port or address to listen on")
	_ = cmd.MarkFlagRequired("listen")
	return cmd
}

func newLabFakeCmd() *cobra.Command {
	var cert, key, routes, listen string
	cmd := &cobra.Command{
		Use:    "lab-fake",
		Short:  "Answer recorded HTTP statuses over TLS for a Lab fake of an outside host",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := lab.ServeFakeTLS(listen, cert, key, routes); err != nil {
				return terminate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cert, "cert", "/etc/lab-tls/tls.crt", "Lab-issued host certificate")
	cmd.Flags().StringVar(&key, "key", "/etc/lab-tls/tls.key", "Lab-issued host private key")
	cmd.Flags().StringVar(&routes, "routes", "/etc/lab-fake/routes", "recorded method path status lines")
	cmd.Flags().StringVar(&listen, "listen", "", "TCP port or address to listen on")
	_ = cmd.MarkFlagRequired("listen")
	return cmd
}

func newLabMITMCmd() *cobra.Command {
	var listen, caCert, caKey string
	cmd := &cobra.Command{
		Use:    "lab-mitm",
		Short:  "CONNECT proxy that MITMs HTTPS with the Lab CA toward Lab fakes",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := lab.ServeMITM(listen, caCert, caKey); err != nil {
				return terminate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "TCP port or address to listen on")
	cmd.Flags().StringVar(&caCert, "ca-cert", "/etc/lab-ca/ca.crt", "Lab CA certificate")
	cmd.Flags().StringVar(&caKey, "ca-key", "/etc/lab-ca/ca.key", "Lab CA private key")
	_ = cmd.MarkFlagRequired("listen")
	return cmd
}

// terminate records why a pod's container failed where Kubernetes shows it.
func terminate(err error) error {
	_ = os.WriteFile("/dev/termination-log", []byte(err.Error()), 0o600)
	return err
}

func newLabCmd(kf *kube.Flags) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "lab",
		Short: "Run the Lab, which seals a namespace from its pool and proves the seal before an experiment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadLabConfig(configPath)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			rc, err := kf.RESTConfig(version.UserAgent("lab"))
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(rc)
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
			}
			l := &lab.Lab{Prover: &lab.Prover{Client: client, Twins: cfg.Twins, Timeout: 3 * time.Minute}, Dynamic: dyn, Pool: cfg.Pool, Domain: cfg.ClusterDomain, Log: log,
				Seal: cfg.Seal, SealImage: cfg.SealImage,
				Enforced: func(ctx context.Context, ns string) (bool, string) {
					r := capability.NetworkPolicyProbe{Client: client, Namespace: ns, Settle: 30 * time.Second}.Run(ctx)
					return r.Status == capability.Available, r.Detail
				}}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			var ready atomic.Bool
			l.OnReady = func() { ready.Store(true) }
			srv := &http.Server{Addr: cfg.HealthAddr, Handler: healthHandler(&ready), ReadHeaderTimeout: 5 * time.Second}
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("health server stopped", "error", err)
				}
			}()
			defer func() { _ = srv.Close() }()
			if c := cfg.LeaderElection; c != nil {
				c.Identity = podName()
				// A replica standing by is ready: it takes over when the
				// holder stops renewing the Lease.
				ready.Store(true)
				return lead.Run(ctx, client, *c, log, l.Run)
			}
			return l.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/sentinel-lab/config.yaml", "path to the Lab's configuration")
	return cmd
}
