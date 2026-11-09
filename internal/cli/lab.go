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
	HealthAddr    string `json:"healthAddr,omitempty"`
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
	return c, nil
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
