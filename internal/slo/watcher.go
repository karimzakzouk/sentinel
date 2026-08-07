package slo

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// SLO describes a service-level objective for a (service, namespace) pair.
type SLO struct {
	Service   string
	Namespace string
	// Target is the success ratio, 0..1. e.g. 0.999 = 99.9%.
	Target float64
	// Window is the SLO window. 30d is industry standard.
	Window time.Duration

	// Query is the PromQL query that returns the success ratio over the
	// window. The literal {{window}} is substituted with the long/short
	// window duration, {{service}} with Service, {{namespace}} with
	// Namespace.
	Query string
}

// BurnRate is the multiplier of error-budget consumption. 1.0 = consuming
// budget at exactly the rate that exhausts it at the end of the SLO window.
// 14.4 = consuming 2% of the budget in 1 hour.
type BurnRate struct {
	Service   string
	Namespace string
	Long      float64
	Short     float64
	SampledAt time.Time
}

// AlertRule is a multi-window burn-rate alerting rule (Google SRE workbook §5.2).
type AlertRule struct {
	Name        string
	LongWindow  time.Duration
	ShortWindow time.Duration
	Threshold   float64
	Severity    string // "page" or "ticket".
	Cooldown    time.Duration
}

// DefaultRules returns the canonical Google SRE multi-window burn-rate rules.
// These are the exact thresholds used by Datadog, Splunk, and Dynatrace.
func DefaultRules() []AlertRule {
	return []AlertRule{
		{Name: "fast-burn-page", LongWindow: 1 * time.Hour, ShortWindow: 5 * time.Minute, Threshold: 14.4, Severity: "page", Cooldown: 30 * time.Minute},
		{Name: "medium-burn-page", LongWindow: 6 * time.Hour, ShortWindow: 30 * time.Minute, Threshold: 6.0, Severity: "page", Cooldown: 1 * time.Hour},
		{Name: "slow-burn-ticket", LongWindow: 3 * 24 * time.Hour, ShortWindow: 6 * time.Hour, Threshold: 3.0, Severity: "ticket", Cooldown: 6 * time.Hour},
		{Name: "always-on-ticket", LongWindow: 3 * 24 * time.Hour, ShortWindow: 6 * time.Hour, Threshold: 1.0, Severity: "ticket", Cooldown: 24 * time.Hour},
	}
}

// MetricsQuerier is the abstraction over Prometheus. The real implementation
// lives in internal/observability; this interface is owned by the consumer
// (slo) so observability can depend on slo without an import cycle.
type MetricsQuerier interface {
	QueryBurnRate(ctx context.Context, slo SLO, longWindow, shortWindow time.Duration) (long, short float64, err error)
}

// Incident is the synthetic incident emitted when a burn-rate rule fires.
// It's deliberately shaped to match the alertmanager payload so the existing
// investigation pipeline consumes it without modification.
type Incident struct {
	AlertName string
	Service   string
	Namespace string
	Severity  string
	LongBurn  float64
	ShortBurn float64
	Threshold float64
	RuleName  string
	SLOTarget float64
	FiredAt   time.Time
	Reason    string
}

// Watcher polls SLOs and emits Incidents when burn-rate rules fire. It is
// safe for concurrent use.
type Watcher struct {
	mu       sync.Mutex
	slos     []SLO
	rules    []AlertRule
	querier  MetricsQuerier
	stopCh   chan struct{}
	interval time.Duration

	lastFired map[string]time.Time // key: service|ruleName.

	// OnIncident is invoked synchronously when a rule fires. Wire this to
	// feed the synthetic incident into the investigation pipeline.
	OnIncident func(Incident)
}

// NewWatcher creates a Watcher with the default Google SRE rules.
func NewWatcher(querier MetricsQuerier) *Watcher {
	return &Watcher{
		rules:     DefaultRules(),
		querier:   querier,
		stopCh:    make(chan struct{}),
		interval:  1 * time.Minute,
		lastFired: make(map[string]time.Time),
	}
}

// RegisterSLO adds an SLO to the watcher.
func (w *Watcher) RegisterSLO(s SLO) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.slos = append(w.slos, s)
}

// SetInterval overrides the default 1-minute poll interval.
func (w *Watcher) SetInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if d > 0 {
		w.interval = d
	}
}

// SetRules replaces the default rule set. Pass nil to restore defaults.
func (w *Watcher) SetRules(rules []AlertRule) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if rules == nil {
		w.rules = DefaultRules()
		return
	}
	w.rules = rules
}

// Start begins polling. Blocks until ctx is canceled or Stop is called.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	interval := w.interval
	w.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logging.Info("slo: watcher started",
		"slos", len(w.slos),
		"rules", len(w.rules),
		"interval", interval,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// Stop signals the watcher to exit. Safe to call once.
func (w *Watcher) Stop() { close(w.stopCh) }

// Tick runs a single poll across all registered SLOs and rules. Exported so
// tests can drive a single iteration without waiting on the ticker.
func (w *Watcher) Tick(ctx context.Context) { w.tick(ctx) }

func (w *Watcher) tick(ctx context.Context) {
	w.mu.Lock()
	slos := make([]SLO, len(w.slos))
	copy(slos, w.slos)
	rules := w.rules
	w.mu.Unlock()

	for _, slo := range slos {
		for _, rule := range rules {
			long, short, err := w.querier.QueryBurnRate(ctx, slo, rule.LongWindow, rule.ShortWindow)
			if err != nil {
				logging.Warn("slo: query failed",
					"service", slo.Service,
					"rule", rule.Name,
					"err", err,
				)
				continue
			}

			// Multi-window: BOTH windows must exceed the threshold.
			// This prevents flapping on transient spikes.
			if long < rule.Threshold || short < rule.Threshold {
				continue
			}

			// Cooldown.
			key := slo.Service + "|" + rule.Name
			w.mu.Lock()
			last, exists := w.lastFired[key]
			w.mu.Unlock()
			if exists && time.Since(last) < rule.Cooldown {
				continue
			}

			inc := Incident{
				AlertName: "SLOBurnRateHigh",
				Service:   slo.Service,
				Namespace: slo.Namespace,
				Severity:  rule.Severity,
				LongBurn:  long,
				ShortBurn: short,
				Threshold: rule.Threshold,
				RuleName:  rule.Name,
				SLOTarget: slo.Target,
				FiredAt:   time.Now(),
				Reason: fmt.Sprintf(
					"SLO burn rate %.2fx exceeds threshold %.2fx (long=%s, short=%s). Target=%.4f. Estimated error-budget consumption: %.1f%% in %s.",
					math.Max(long, short), rule.Threshold,
					rule.LongWindow, rule.ShortWindow,
					slo.Target,
					math.Max(long, short)*100*float64(rule.LongWindow)/float64(30*24*time.Hour),
					rule.LongWindow,
				),
			}

			w.mu.Lock()
			w.lastFired[key] = time.Now()
			w.mu.Unlock()

			logging.Warn("slo: BURN RATE RULE FIRED",
				"service", inc.Service,
				"namespace", inc.Namespace,
				"rule", inc.RuleName,
				"severity", inc.Severity,
				"long_burn", inc.LongBurn,
				"short_burn", inc.ShortBurn,
				"threshold", inc.Threshold,
			)

			if w.OnIncident != nil {
				w.OnIncident(inc)
			}
		}
	}
}
