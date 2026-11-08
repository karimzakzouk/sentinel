package cli

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/brain"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

// defaultChampions are the deterministic engines that fill each slot unless
// the config names another.
var defaultChampions = map[string]string{
	slot.BrainClassify:  brain.NaiveBayes{}.ID(),
	slot.BrainAttribute: brain.LikelihoodRatios{}.ID(),
	slot.BrainDecide:    brain.ExpectedLoss{}.ID(),
}

// Registry returns every engine Sentinel ships, by slot.
func Registry() (*slot.Registry, error) {
	r := slot.NewRegistry()
	for _, err := range []error{
		slot.Register(r, slot.BrainClassify, brain.NaiveBayes{}),
		slot.Register(r, slot.BrainAttribute, brain.LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}),
		slot.Register(r, slot.BrainDecide, brain.ExpectedLoss{Impact: 60, Escalation: 10, Slower: 15, Disruption: 5, PReal: 0.95}),
	} {
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func slotConfig(cfg *config.Config, name string) slot.Config {
	if c, ok := cfg.Slots[name]; ok {
		return c
	}
	return slot.Config{Champion: defaultChampions[name]}
}

// newObserver wires the Watcher to the Brain in observe mode.
func newObserver(log *slog.Logger, cfg *config.Config, client kubernetes.Interface, l *ledger.Ledger) (*watcher.Watcher, error) {
	reg, err := Registry()
	if err != nil {
		return nil, err
	}
	rec := slot.LedgerRecorder{Ledger: l, Source: "/brain"}
	classify, err := slot.Build[brain.ClassifyInput, slot.Distribution[string]](reg, slot.BrainClassify, slotConfig(cfg, slot.BrainClassify), rec)
	if err != nil {
		return nil, err
	}
	attribute, err := slot.Build[brain.AttributeInput, slot.Ranking[brain.RankedChange]](reg, slot.BrainAttribute, slotConfig(cfg, slot.BrainAttribute), rec)
	if err != nil {
		return nil, err
	}
	decide, err := slot.Build[brain.DecideInput, brain.Decision](reg, slot.BrainDecide, slotConfig(cfg, slot.BrainDecide), rec)
	if err != nil {
		return nil, err
	}
	d := cfg.Detection
	detector := watcher.NewCountDetector(d.Window.Duration, d.Tick.Duration, d.FalseAlarmsPerWeek, d.HalfLife.Duration)
	if log.Enabled(context.Background(), slog.LevelDebug) {
		detector.Trace = func(t watcher.Trace) {
			log.Debug("series evaluated", "workload", t.Workload.String(), "kind", t.Kind, "count", t.Count,
				"tailProbability", t.TailProbability, "alpha", t.Alpha, "inEpisode", t.InEpisode,
				"historyCount", t.HistoryCount, "historyExposureSeconds", t.HistoryExposure)
		}
	}
	w := &watcher.Watcher{Client: client, Namespaces: cfg.Namespaces, Log: log, Detector: detector}
	b := &brain.Brain{Ledger: l, Log: log, Classify: classify, Attribute: attribute, Decide: decide,
		Revisions: w.NewestRevision, Lookback: time.Hour, Gap: d.IncidentGap.Duration,
		Dependents: func(ref model.ObjectRef) []model.ObjectRef { return w.Topology().Dependents(ref) }}
	w.OnTick = func(ctx context.Context) {
		if err := b.Sweep(ctx); err != nil {
			log.Error("could not resolve quiet incidents", "error", err)
		}
	}
	w.OnChange = func(ctx context.Context, c watcher.Change) {
		if err := b.OnChange(ctx, c); err != nil {
			log.Error("could not record a change", "object", c.Object.String(), "error", err)
		}
	}
	w.OnDetection = func(ctx context.Context, det watcher.Detection) {
		if err := b.OnDetection(ctx, det); err != nil {
			log.Error("could not record a detection", "workload", det.Workload.String(), "error", err)
		}
	}
	if cfg.Logs.On() {
		w.Logs = watcher.NewLogCollector(client, log, func(ctx context.Context, ev watcher.LogEvidence) {
			if err := b.OnEvidence(ctx, ev); err != nil {
				log.Error("could not record log evidence", "workload", ev.Workload.String(), "pod", ev.Pod, "error", err)
			}
		})
	}
	return w, nil
}
