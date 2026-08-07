// Package main is the sentinel server entry point.
//
// This is a placeholder for Phase 1. The full entry point (wiring all
// internal packages, goroutine spawning, signal handling) is built in
// Phase 10 after the rest of the internal packages exist.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// Version is the build version, set via -ldflags. Defaults to "dev".
var Version = version.Version

func main() {
	addr := flag.String("addr", "", "listen address (overrides PORT env)")
	dbPath := flag.String("db", "", "SQLite path (overrides SENTINEL_SQLITE_PATH env)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("sentinel", Version)
		return
	}

	cfg := config.Load()
	if *addr != "" {
		_ = *addr // wire into cfg once the API server is built
	}
	if *dbPath != "" {
		cfg.SQLitePath = *dbPath
	}

	logging.Init(cfg.LogLevel)
	logging.Info("sentinel starting",
		"version", Version,
		"namespace", cfg.Namespace,
		"api_port", cfg.APIPort,
	)

	errs, warns := cfg.Validate()
	for _, e := range errs {
		logging.Error("config error", "err", e)
	}
	for _, w := range warns {
		logging.Warn("config warning", "warn", w)
	}
	if len(errs) > 0 {
		os.Exit(1)
	}

	// Phase 1 placeholder: real wiring comes in Phase 10.
	logging.Info("sentinel ready (phase 1 placeholder — wiring pending)")
}
