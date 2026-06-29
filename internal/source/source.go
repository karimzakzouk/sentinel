// Package source defines the interface for incident sources — anything that
// produces signals which Sentinel should investigate.
//
// A Source is anything that can produce AnomalyCluster signals: a K8s log
// stream, a pod status watcher, an Alertmanager webhook, a Datadog monitor,
// etc. Each Source registers itself with the clusterer, which deduplicates
// and emits ONE trigger per service per time window.
//
// To add a new source:
//   1. Implement the Source interface in a new file under internal/source/
//   2. Wire it up in cmd/sentinel/main.go (or wherever the clusterer is constructed)
//
// Sources are responsible for their own lifecycle (start/stop) and for
// calling handler.Handle() when they detect an anomaly.
package source

import (
	"context"
)

// Source is anything that produces anomaly signals for Sentinel to investigate.
//
// Implementations:
//   - K8s log stream watcher (live log_error detection)
//   - K8s pod status watcher (CrashLoopBackOff, OOMKilled, ImagePullBackOff)
//   - Alertmanager webhook receiver
//   - Datadog monitor webhook
//   - Manual trigger via CRD
type Source interface {
	// Name returns a stable identifier for this source (e.g. "log_watcher",
	// "alertmanager"). Used in metrics + logs.
	Name() string

	// Start begins producing signals. The source calls handler.Handle() for
	// each anomaly it detects. Start blocks until ctx is cancelled or an
	// unrecoverable error occurs.
	Start(ctx context.Context, handler Handler) error

	// Stop performs a graceful shutdown. In-flight signals may be dropped.
	Stop() error
}

// Handler is what Sources call when they detect an anomaly. Typically the
// detection.Clusterer implements this interface.
type Handler interface {
	// Handle delivers a signal to the clusterer for deduplication + grouping.
	Handle(signal Signal)
}

// Signal is the data a Source emits when it detects something worth investigating.
// Mirrors detection.Signal but lives in a neutral package so Sources don't
// have to import the detection package (avoids circular deps when the
// detection package imports source for its Handler impl).
type Signal struct {
	// Type classifies the signal source.
	Type SignalType

	// Service is the affected service name (typically the K8s Deployment name
	// or a logical service identifier from the alert labels).
	Service string

	// Namespace is the K8s namespace (empty for non-K8s sources).
	Namespace string

	// Reason is a human-readable summary of why this signal was emitted
	// (e.g. "ZeroDivisionError in main.py:42" or "CrashLoopBackOff").
	Reason string

	// Repo is the GitHub repo (owner/name) the service maps to. May be empty
	// if the source can't determine it (the clusterer will look it up via
	// K8s annotations).
	Repo string

	// RawLogs is the raw log/error text that triggered the signal. The
	// investigator uses this for root-cause analysis.
	RawLogs string

	// Labels carries arbitrary metadata (e.g. Alertmanager labels, Datadog
	// tags). Used for deduplication keying + display.
	Labels map[string]string
}

// SignalType classifies what kind of source produced a Signal.
type SignalType string

const (
	// SignalLogError: a runtime error was detected in pod logs
	// (TypeError, ZeroDivisionError, panic, etc.).
	SignalLogError SignalType = "log_error"

	// SignalPodStatus: a pod entered a bad status (CrashLoopBackOff,
	// OOMKilled, ImagePullBackOff).
	SignalPodStatus SignalType = "pod_status"

	// SignalK8sEvent: a K8s Event with a warning reason was emitted.
	SignalK8sEvent SignalType = "k8s_event"

	// SignalAlertmanager: an Alertmanager webhook was received.
	SignalAlertmanager SignalType = "alertmanager"

	// SignalDatadog: a Datadog monitor fired.
	SignalDatadog SignalType = "datadog"

	// SignalManual: a manual trigger via CRD or sentinelctl.
	SignalManual SignalType = "manual"
)
