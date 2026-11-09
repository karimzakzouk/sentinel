package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/act"
	"github.com/karimzakzouk/sentinel/internal/brain"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
	"github.com/karimzakzouk/sentinel/internal/slot"
	"github.com/karimzakzouk/sentinel/internal/topology"
	"github.com/karimzakzouk/sentinel/internal/verify"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

// defaultChampions are the deterministic engines that fill each slot unless
// the config names another.
var defaultChampions = map[string]string{
	slot.BrainClassify:  brain.NaiveBayes{}.ID(),
	slot.BrainAttribute: brain.LikelihoodRatios{}.ID(),
	slot.BrainLocalize:  brain.EvidenceRanking{}.ID(),
	slot.BrainDecide:    brain.ExpectedLoss{}.ID(),
}

// Registry returns every engine Sentinel ships, by slot.
func Registry() (*slot.Registry, error) {
	r := slot.NewRegistry()
	for _, err := range []error{
		slot.Register(r, slot.BrainClassify, brain.NaiveBayes{}),
		slot.Register(r, slot.BrainAttribute, brain.LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}),
		slot.Register(r, slot.BrainLocalize, brain.EvidenceRanking{Lookback: time.Hour, Typical: 5 * time.Minute}),
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

// servesActing reports whether the cluster serves the custom resources
// acting needs.
func servesActing(client kubernetes.Interface) error {
	list, err := client.Discovery().ServerResourcesForGroupVersion(v1alpha1.GroupVersion.String())
	if err != nil {
		return fmt.Errorf("%s is not served: install the sentinel-crds chart: %w", v1alpha1.GroupVersion, err)
	}
	served := map[string]bool{}
	for _, r := range list.APIResources {
		served[r.Name] = true
	}
	for _, name := range []string{"actionrequests", "sentinelpolicies"} {
		if !served[name] {
			return fmt.Errorf("%s does not serve %s", v1alpha1.GroupVersion, name)
		}
	}
	return nil
}

// newObserver wires the Watcher to the Brain, and the Brain to the actor
// when the configuration names one.
func newObserver(ctx context.Context, log *slog.Logger, cfg *config.Config, client kubernetes.Interface, dyn dynamic.Interface,
	l *ledger.Ledger,
) (*watcher.Watcher, *act.Requests, error) {
	reg, err := Registry()
	if err != nil {
		return nil, nil, err
	}
	rec := slot.LedgerRecorder{Ledger: l, Source: "/brain"}
	classify, err := slot.Build[brain.ClassifyInput, slot.Distribution[string]](reg, slot.BrainClassify, slotConfig(cfg, slot.BrainClassify), rec)
	if err != nil {
		return nil, nil, err
	}
	attribute, err := slot.Build[brain.AttributeInput, slot.Ranking[brain.RankedChange]](reg, slot.BrainAttribute, slotConfig(cfg, slot.BrainAttribute), rec)
	if err != nil {
		return nil, nil, err
	}
	localize, err := slot.Build[brain.LocalizeInput, slot.Ranking[brain.Suspect]](reg, slot.BrainLocalize, slotConfig(cfg, slot.BrainLocalize), rec)
	if err != nil {
		return nil, nil, err
	}
	decide, err := slot.Build[brain.DecideInput, brain.Decision](reg, slot.BrainDecide, slotConfig(cfg, slot.BrainDecide), rec)
	if err != nil {
		return nil, nil, err
	}
	d := cfg.Detection
	detector := watcher.NewCountDetector(d.Window.Duration, d.Tick.Duration, d.FalseAlarmsPerWeek, d.HalfLife.Duration)
	detector.WarmUp = d.WarmUp.Duration
	if log.Enabled(context.Background(), slog.LevelDebug) {
		detector.Trace = func(t watcher.Trace) {
			log.Debug("series evaluated", "workload", t.Workload.String(), "kind", t.Kind, "count", t.Count,
				"expected", t.Expected, "tailProbability", t.TailProbability, "alpha", t.Alpha, "inEpisode", t.InEpisode,
				"historyCount", t.HistoryCount, "historyExposureSeconds", t.HistoryExposure)
		}
	}
	w := &watcher.Watcher{Client: client, Namespaces: cfg.Namespaces, Log: log, Detector: detector}
	if _, err := client.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1"); err == nil {
		w.Metrics, w.MetricsInterval = watcher.APIMetrics{Client: client.CoreV1().RESTClient()}, 30*time.Second
	} else {
		log.Info("resource metrics are not served; metric signals are off", "error", err)
	}
	b := &brain.Brain{Ledger: l, Log: log, Classify: classify, Attribute: attribute, Decide: decide,
		Revisions: w.NewestRevision, Lookback: time.Hour, Gap: d.IncidentGap.Duration, Localize: localize,
		Dependents:   func(ref model.ObjectRef) []model.ObjectRef { return w.Topology().Dependents(ref) },
		Dependencies: func(ref model.ObjectRef) []topology.Reach { return w.Topology().DependenciesWithHops(ref) },
		Deviations:   w.Deviations}
	if n, err := b.Restore(ctx); err != nil {
		return nil, nil, fmt.Errorf("restore open incidents: %w", err)
	} else if n > 0 {
		log.Info("took back incidents left open by the last run", "incidents", n)
	}
	var acting *act.Requests
	if url := os.Getenv("SENTINEL_SLACK_WEBHOOK"); url != "" {
		b.Notify = notify.NewSlack(url, log).Notify
		log.Info("notices go to a Slack webhook")
	}
	if cfg.ActorUser != "" && dyn != nil {
		// Acting is optional: without the custom resources, Sentinel observes.
		requests := &act.Requests{Client: client, Dynamic: dyn, Namespaces: cfg.Namespaces, TTL: 10 * time.Minute, Log: log,
			OnOutcome: func(ctx context.Context, o act.Outcome) {
				if err := b.OnActionOutcome(ctx, brain.ActionOutcome{IncidentID: o.IncidentID, Request: o.Request, Phase: string(o.Phase),
					Message: o.Message, ApprovedBy: o.ApprovedBy}); err != nil {
					log.Error("could not record an action's outcome", "request", o.Request, "error", err)
				}
			}}
		if err := servesActing(client); err != nil {
			log.Warn("acting is off; Sentinel only observes", "reason", err)
		} else if err := requests.Start(ctx); err != nil {
			log.Warn("acting is off; Sentinel only observes", "reason", err)
		} else {
			b.Act = requests
			acting = requests
			log.Info("acting is on where a SentinelPolicy allows it", "namespaces", cfg.Namespaces)
		}
	}
	if cfg.VerifyRollbacks && len(cfg.LabNamespaces) > 0 && dyn != nil {
		lab := &verify.Lab{Dynamic: dyn, Pool: cfg.LabNamespaces, Watched: cfg.Namespaces, Log: log,
			OnVerdict: func(ctx context.Context, v verify.Verdict) {
				if err := b.OnLabVerdict(ctx, brain.LabVerdict{IncidentID: v.IncidentID, Experiment: v.Experiment, Phase: string(v.Phase),
					Message: v.Message, Runs: v.Runs, Fidelity: v.Fidelity}); err != nil {
					log.Error("could not record the Lab's verdict", "experiment", v.Experiment, "error", err)
				}
			}}
		if err := lab.Start(ctx); err != nil {
			log.Warn("the Lab does not verify rollbacks", "reason", err)
		} else {
			b.Lab = lab
			log.Info("the Lab verifies proposed rollbacks", "pool", cfg.LabNamespaces)
		}
	}
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
		w.SampleInterval = cfg.Logs.SampleEvery()
	}
	return w, acting, nil
}
