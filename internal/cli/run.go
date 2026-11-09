package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

	"github.com/karimzakzouk/sentinel/internal/act"
	"github.com/karimzakzouk/sentinel/internal/api"
	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	webui "github.com/karimzakzouk/sentinel/internal/ui"
	"github.com/karimzakzouk/sentinel/internal/version"
)

func newRunCmd(kf *kube.Flags) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the Sentinel controller",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			var level slog.Level
			if v := os.Getenv("SENTINEL_LOG_LEVEL"); v != "" {
				if err := level.UnmarshalText([]byte(v)); err != nil {
					return fmt.Errorf("SENTINEL_LOG_LEVEL: %w", err)
				}
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
			rc, err := kf.RESTConfig(version.UserAgent("controller"))
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
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			return runController(ctx, log, cfg, client, dyn)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/sentinel/config.yaml", "path to the controller config file")
	return cmd
}

func runController(ctx context.Context, log *slog.Logger, cfg *config.Config, client kubernetes.Interface, dyn dynamic.Interface) error {
	l, err := ledger.Open(ctx, cfg.Ledger.Path)
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	n, err := l.Verify(ctx)
	if err != nil {
		return fmt.Errorf("refusing to start on a ledger that fails verification: %w", err)
	}
	seq, head, err := l.Head(ctx)
	if err != nil {
		return err
	}
	log.Info("ledger verified", "events", n, "headSeq", seq, "headHash", head, "version", version.Get().Version)
	go checkpoints(ctx, log, l, time.Hour)

	var ready atomic.Bool
	srv := &http.Server{Addr: cfg.HealthAddr, Handler: healthHandler(&ready), ReadHeaderTimeout: 5 * time.Second}
	var acting atomic.Pointer[act.Requests]
	ui := &http.Server{Addr: cfg.UI.Address, ReadHeaderTimeout: 5 * time.Second, Handler: (&api.Server{
		Ledger: l, Version: version.Get().Version, Verified: api.Verification{Events: n, At: time.Now().UTC()}, UI: webui.Handler(),
		Acting: func() bool { r := acting.Load(); return r != nil && r.Acting() },
	}).Handler()}
	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.ListenAndServe() }()
	go func() { serveErr <- ui.ListenAndServe() }()
	log.Info("UI listening", "address", cfg.UI.Address)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = ui.Shutdown(shutdownCtx)
	}()

	w, requests, err := newObserver(ctx, log, cfg, client, dyn, l)
	if err != nil {
		return err
	}
	acting.Store(requests)
	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Run(ctx) }()

	detector := &capability.Detector{Client: client, Options: capability.Options{
		Namespaces: cfg.Namespaces, AgentNamespace: cfg.AgentNamespace, ActorUser: cfg.ActorUser,
	}}
	var last [sha256.Size]byte
	tick := time.NewTicker(cfg.Capabilities.Interval.Duration)
	defer tick.Stop()
	for {
		if err := recordCapabilities(ctx, log, l, detector, &last); err != nil {
			log.Error("capability report failed", "error", err)
		} else {
			ready.Store(true)
		}
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return nil
		case err := <-serveErr:
			if !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("http server: %w", err)
			}
			return nil
		case err := <-watchErr:
			if err != nil {
				return fmt.Errorf("watcher: %w", err)
			}
			return nil
		case <-tick.C:
		}
	}
}

// recordCapabilities appends a report to the Ledger only when it differs from
// the last one recorded, so a stable cluster does not grow the log.
func recordCapabilities(ctx context.Context, log *slog.Logger, l *ledger.Ledger, d *capability.Detector, last *[sha256.Size]byte) error {
	rep, err := d.Run(ctx)
	if err != nil {
		return err
	}
	fingerprint := *rep
	fingerprint.GeneratedAt = time.Time{}
	b, err := json.Marshal(fingerprint)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if sum == *last {
		return nil
	}
	if _, err := l.Append(ctx, ledger.Draft{Type: "sentinel.capability.reported", Source: "/controller", Subject: "cluster", Data: rep}); err != nil {
		return err
	}
	*last = sum
	var enabled, disabled []string
	for _, f := range rep.Features {
		if f.Enabled {
			enabled = append(enabled, f.Name)
		} else {
			disabled = append(disabled, f.Name)
		}
	}
	log.Info("capability report recorded", "platform", rep.Cluster.Platform, "identity", rep.Identity,
		"enabled", enabled, "disabled", disabled, "warnings", rep.Warnings)
	return nil
}

func healthHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "no capability report yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// checkpoints logs the Ledger's Merkle root every interval. Logs leave the
// cluster for wherever they are kept, out of Sentinel's reach, so a root
// written there pins the log: sentinel ledger prove shows any event in it,
// and that the log at any later root still begins with it.
func checkpoints(ctx context.Context, log *slog.Logger, l *ledger.Ledger, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if size, root, err := l.Root(ctx, 0); err != nil {
			log.Error("could not compute the ledger's root", "error", err)
		} else {
			log.Info("ledger checkpoint", "size", size, "root", root)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
