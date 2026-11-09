package cli

import (
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

	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// ActorConfig is the actor's own configuration, kept apart from the
// controller's so that only the actor's namespace can change it.
type ActorConfig struct {
	// Namespaces are those the actor may act in.
	Namespaces []string `json:"namespaces"`
	// Enabled is the kill switch: false rejects every request. The file is
	// read again before each request, so a change to the mounted ConfigMap
	// takes effect without a restart.
	Enabled bool `json:"enabled"`
	// PerHour bounds the actions per namespace per hour.
	PerHour int `json:"perHour"`
	// Self lists Sentinel's own identities, which may not approve actions.
	Self       []string `json:"self,omitempty"`
	HealthAddr string   `json:"healthAddr,omitempty"`
}

func loadActorConfig(path string) (ActorConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's command line
	if err != nil {
		return ActorConfig{}, err
	}
	var c ActorConfig
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return ActorConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Namespaces) == 0 {
		return ActorConfig{}, errors.New("namespaces is empty: list the namespaces the actor may act in")
	}
	if c.PerHour < 0 {
		return ActorConfig{}, fmt.Errorf("perHour %d is negative", c.PerHour)
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8081"
	}
	return c, nil
}

func newActorCmd(kf *kube.Flags) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "actor",
		Short: "Run the actor, which carries out the controller's action requests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadActorConfig(configPath)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			rc, err := kf.RESTConfig(version.UserAgent("actor"))
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
			a := &actor.Actor{Client: client, Dynamic: dyn, Namespaces: cfg.Namespaces, PerHour: cfg.PerHour, Self: cfg.Self, Log: log,
				Disabled: func() bool {
					current, err := loadActorConfig(configPath)
					if err != nil {
						log.Error("could not read the actor's configuration; treating the kill switch as on", "error", err)
						return true
					}
					return !current.Enabled
				}}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			var ready atomic.Bool
			ready.Store(true)
			srv := &http.Server{Addr: cfg.HealthAddr, Handler: healthHandler(&ready), ReadHeaderTimeout: 5 * time.Second}
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("health server stopped", "error", err)
				}
			}()
			defer func() { _ = srv.Close() }()
			return a.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/sentinel-actor/config.yaml", "path to the actor's configuration")
	return cmd
}
