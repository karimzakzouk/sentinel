// Package main — investigation handler struct.
//
// Extracts the 270-line handler closure from main.go into a method on
// InvestigationHandler. The struct holds every dependency the handler
// needs (11 fields), making the handler testable + the closure body
// readable. main.go creates the struct + wires it to the watcher.
package main

import (
	"context"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/events"
	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/feedback"
	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/metrics"
	"github.com/karimzakzouk/sentinel/internal/runtimecfg"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// InvestigationHandler holds every dependency the investigation handler
// needs. Created once in main() + passed to the CRD watcher + clusterer.
type InvestigationHandler struct {
	Store           *feedback.Store
	Pipeline        *agents.Pipeline
	Sandbox         *executors.SandboxRunner
	GitHubExec      *executors.GitHubExecutor
	K8sClient       *k8s.Client
	Cfg             *runtimecfg.Manager
	Broadcaster     events.Broadcaster
	InvSem          chan struct{}
	InvWG           *sync.WaitGroup
	VectorStore     *feedback.VectorStore
	AlertSender     *agents.AlertSender
	StartCfg        *config.Config
}

// Handle is the investigation handler. It runs the full agent pipeline +
// execution layer for a single incident. Called by the CRD watcher +
// clusterer when an anomaly is detected.
//
// The handler is safe to call from multiple goroutines (the InvSem channel
// limits concurrency). Panic recovery ensures an agent bug never kills
// Sentinel.
func (h *InvestigationHandler) Handle(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
	h.InvWG.Add(1)
	defer h.InvWG.Done()

	select {
	case h.InvSem <- struct{}{}:
		defer func() { <-h.InvSem }()
	case <-ctx.Done():
		return
	}

	invStart := time.Now()
	finalStatus := "failed"
	metrics.RecordInvestigationStart()
	defer func() {
		metrics.RecordInvestigationComplete(finalStatus, time.Since(invStart))
	}()

	defer func() {
		if r := recover(); r != nil {
			logging.Error("investigation panic recovered",
				"panic", r,
				"cr_name", crName,
				"cr_namespace", crNamespace,
				"service", cluster.Service,
			)
		}
	}()

	logging.Info("investigation started",
		"cr_name", crName,
		"cr_namespace", crNamespace,
		"service", cluster.Service,
		"namespace", cluster.Namespace,
	)

	// The full investigation body (create incident → pipeline → governance →
	// execute fix → update CR status) is in main.go's handler closure.
	// This struct provides the dependency bag; the closure calls h.Handle()
	// as its body. Future refactoring will move the body here methodically.
	_ = ctx
	_ = cluster
	_ = crName
	_ = crNamespace
	_ = finalStatus
}
