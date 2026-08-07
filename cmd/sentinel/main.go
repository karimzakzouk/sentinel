// Package main is the Sentinel server entry point.
//
// One binary, one process, one pod. The wiring is split across six files:
//
//   - main.go          — flag parsing, config load, wire() call, run(), shutdown.
//   - wire.go          — wire() (the construction entry point).
//   - wiring.go        — the wiring struct + every adapter method.
//   - investigation.go — the per-incident orchestrator.
//   - callbacks.go     — runtime callbacks (applyRuntimeConfig, SLO, chaos).
//   - recover.go       — the goroutine panic-recovery helper.
//
// The flow is intentionally linear so a new contributor can read main()
// top-to-bottom: parse flags → load config → validate → init logging →
// build the wiring struct → start long-running goroutines → serve the
// API → wait for SIGTERM → graceful shutdown.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// Version is the build version, sourced from internal/version (which is
// set via -ldflags from the VERSION file at build time).
var Version = version.Version

// main is the Sentinel entry point. See the package doc for the flow.
func main() {
	addr := flag.String("addr", "", "HTTP listen address (overrides env)")
	dbPath := flag.String("db", "", "SQLite DB path (overrides env)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("sentinel", Version)
		return
	}

	cfg := config.Load()
	if *dbPath != "" {
		cfg.SQLitePath = *dbPath
	}
	listenAddr := fmt.Sprintf("%s:%d", cfg.APIHost, cfg.APIPort)
	if *addr != "" {
		listenAddr = *addr
	}

	// Validate config before initializing the logger so we can surface
	// validation errors structurally. Validate-then-init also means an
	// empty SENTINEL_LLM_API_KEY errors out before any I/O.
	if errs, warns := cfg.Validate(); len(errs) > 0 || len(warns) > 0 {
		logging.Init(cfg.LogLevel)
		for _, w := range warns {
			logging.Warn("config validation warning: " + w)
		}
		for _, e := range errs {
			logging.Error("config validation error: " + e)
		}
		if len(errs) > 0 {
			logging.Error("refusing to start due to config validation errors")
			os.Exit(1)
		}
	}
	logging.Init(cfg.LogLevel)

	logging.Info("sentinel starting",
		"version", Version,
		"llm_provider", cfg.LLMProvider,
		"llm_frontier_model", cfg.LLMFrontierModel,
		"llm_embedding_model", cfg.LLMEmbeddingModel,
		"sqlite_path", cfg.SQLitePath,
		"api_addr", listenAddr,
		"log_level", cfg.LogLevel,
	)

	if cfg.LLMEmbeddingModel == "" {
		logging.Info("embedding model not configured — using hash fallback (similar-incident retrieval disabled). Set SENTINEL_LLM_EMBEDDING_MODEL to enable.",
			"provider", cfg.LLMProvider,
			"embedding_dim", cfg.LLMEmbeddingDim,
		)
	}

	w, err := wire(cfg, listenAddr)
	if err != nil {
		logging.Error("startup failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	run(ctx, cancel, w)
}

// wire constructs the wiring struct by instantiating every Sentinel
// component in dependency order. Returns an error if any unrecoverable
// initialization fails (e.g. SQLite open, K8s client init).
//
// Components are constructed in five phases:
//  1. Storage + infra (store, k8s, router, embedder, vectorStore, pipeline).
//  2. Executors + safety (ghExec, k8sExec, sandbox, kaniko, policy).
//  3. Detection + correlation (tracker, clusterer, detector, correlator,
//     outcomeWatcher, alertmanager receiver, CRD watcher).
//  4. Tier 1/2 features (attest, prometheus, postmerge, slo, causal,
//     admission, llmcost-implicit, chaos).
//  5. API surface (prreview, chatops, automerge, slackbot, apiServer).
//
// The runtimecfg apply callback is registered LAST, after every component
// it mutates has been constructed.
func run(ctx context.Context, cancel context.CancelFunc, w *wiring) {
	// Long-running goroutines. Each recovers its own panics so a bug in
	// one component does not kill the process.
	go func() {
		defer recoverPanic("CRD watcher")
		logging.Info("CRD watcher started")
		w.crdWatcher.Run(ctx)
	}()

	go func() {
		defer recoverPanic("sandbox reaper")
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		w.sandbox.ReapStaleSandboxNamespaces(ctx, 30*time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.sandbox.ReapStaleSandboxNamespaces(ctx, 30*time.Minute)
			}
		}
	}()

	go func() {
		defer recoverPanic("detection layer")
		logging.Info("detection layer started (live log streaming + pod status)")
		w.detector.Run(ctx)
	}()

	w.amReceiver.StartPurger(ctx)

	// Tier 1 goroutines.
	if w.sloWatcher != nil {
		go func() {
			defer recoverPanic("slo watcher")
			w.sloWatcher.Start(ctx)
		}()
	}
	if w.admissionSrv != nil {
		go func() {
			defer recoverPanic("admission webhook")
			cfg := w.Config()
			logging.Info("admission: webhook server starting", "addr", cfg.AdmissionWebhookAddr)
			if err := w.admissionSrv.ListenAndServeTLS(
				cfg.AdmissionWebhookCertDir+"/tls.crt",
				cfg.AdmissionWebhookCertDir+"/tls.key",
			); err != nil && err != http.ErrServerClosed {
				logging.Error("admission: webhook server failed", "error", err)
			}
		}()
	}
	if w.chaosRunner != nil {
		go func() {
			defer recoverPanic("chaos runner")
			w.chaosRunner.Start(ctx)
		}()
	}

	// Auto-merge merger + auto-remediation controller.
	if w.Config().AutoMergeEnabled {
		go func() {
			defer recoverPanic("auto-merge merger")
			w.autoMergeMerger.Run(ctx, 30*time.Second)
		}()
		logging.Info("auto-merge merger started", "poll_interval", "30s")
	}
	if w.remediation != nil {
		go func() {
			defer recoverPanic("auto-remediation")
			w.remediation.Run(ctx, 5*time.Minute)
		}()
		logging.Info("auto-remediation controller started", "poll_interval", "5m")
	}

	// Flip the readiness flag after a 2s grace period so the detector +
	// CRD watcher have had a chance to start streaming.
	go func() {
		defer recoverPanic("ready-setter")
		time.Sleep(2 * time.Second)
		w.ready.Store(true)
		logging.Info("sentinel ready — watching for incidents")
	}()

	// API server. Blocks until Shutdown is called or the listener fails.
	go func() {
		defer recoverPanic("API server")
		if err := w.apiServer.Start(); err != nil && err != http.ErrServerClosed {
			logging.Error("API server failed — exiting", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logging.Info("shutting down...")

	// Stop detection first so no new investigations spawn.
	w.clusterer.Stop()
	w.detector.StopAllLogStreams()

	if w.postMerge != nil {
		w.postMerge.Stop()
	}
	if w.sloWatcher != nil {
		w.sloWatcher.Stop()
	}
	if w.chaosRunner != nil {
		w.chaosRunner.Stop()
	}
	if w.admissionSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = w.admissionSrv.Shutdown(shutdownCtx)
		cancel()
	}

	cancel()

	// Wait for in-flight investigations (15s deadline) so the SQLite
	// store doesn't get closed mid-write.
	done := make(chan struct{})
	go func() {
		defer recoverPanic("shutdown waiter")
		w.invWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		logging.Info("all in-flight investigations completed")
	case <-time.After(15 * time.Second):
		logging.Warn("15s shutdown deadline exceeded — proceeding with force-close (some investigations may be incomplete)")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = w.apiServer.Shutdown(shutdownCtx)

	if err := w.store.Close(); err != nil {
		logging.Error("store close failed", "error", err)
	}
	logging.Info("stopped")
}
