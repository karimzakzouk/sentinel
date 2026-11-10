package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/act"
	"github.com/karimzakzouk/sentinel/internal/brain"
	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/fixes"
	labpkg "github.com/karimzakzouk/sentinel/internal/lab"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/nodeagent"
	"github.com/karimzakzouk/sentinel/internal/notify"
	"github.com/karimzakzouk/sentinel/internal/redact"
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
	l *ledger.Ledger, captured *capture.Store, nodeLogs *nodeagent.Store,
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
	if cfg.Ledger.Path != "" {
		path := filepath.Join(filepath.Dir(cfg.Ledger.Path), "baselines.json")
		if n, err := restoreBaselines(detector, path, time.Now()); err != nil {
			log.Warn("could not take back the learned baselines; they are learned again", "path", path, "error", err)
		} else if n > 0 {
			log.Info("took back learned baselines", "series", n, "path", path)
		}
		go keepBaselines(ctx, detector, path, 5*time.Minute, log)
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
		Deviations:   w.Deviations, MemoryUse: w.MemoryUse, CPUUse: w.CPUUse}
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
					Message: o.Message, ApprovedBy: o.ApprovedBy, BudgetSpent: o.BudgetSpent}); err != nil {
					log.Error("could not record an action's outcome", "request", o.Request, "error", err)
				}
			}}
		if err := servesActing(client); err != nil {
			log.Warn("acting is off; Sentinel only observes", "reason", err)
		} else if err := requests.Start(ctx); err != nil {
			log.Warn("acting is off; Sentinel only observes", "reason", err)
		} else {
			b.Act = requests
			b.MergePolicy = func(ns string) (model.AutonomyLevel, bool) {
				return model.AutonomyLevel(requests.Autonomy(ns, brain.ActionFixMerge)), requests.Earned(ns, brain.ActionFixMerge)
			}
			acting = requests
			log.Info("acting is on where a SentinelPolicy allows it", "namespaces", cfg.Namespaces)
		}
	}
	if len(cfg.LabNamespaces) > 0 && dyn != nil {
		lab := &verify.Lab{Dynamic: dyn, Pool: cfg.LabNamespaces, Watched: cfg.Namespaces, Log: log,
			OnVerdict: func(ctx context.Context, v verify.Verdict) {
				if err := b.OnLabVerdict(ctx, brain.LabVerdict{IncidentID: v.IncidentID, Experiment: v.Experiment, Phase: string(v.Phase),
					Message: v.Message, Runs: v.Runs, Fidelity: v.Fidelity}); err != nil {
					log.Error("could not record the Lab's verdict", "experiment", v.Experiment, "error", err)
				}
			}}
		if captured != nil {
			recent := func(target model.ObjectRef) []capture.Request {
				return captured.Recent(target, time.Now().Add(-24*time.Hour))
			}
			lab.Replay = func(target model.ObjectRef) []v1alpha1.ReplayRequest {
				var out []v1alpha1.ReplayRequest
				for _, r := range capture.Replayable(recent(target), 20) {
					out = append(out, v1alpha1.ReplayRequest{Path: r.Path, Port: int32(r.Port)}) //nolint:gosec // a port the agent saw, below 65536
				}
				return out
			}
			lab.Fakes = func(target model.ObjectRef) []v1alpha1.FakeCall {
				var out []v1alpha1.FakeCall
				for _, c := range capture.Calls(recent(target), 50) {
					out = append(out, v1alpha1.FakeCall{
						Host: c.Host, Method: c.Method, Path: c.Path,
						Port: int32(c.Port), Status: int32(c.Status), //nolint:gosec // port and status the agent saw
						ResponseBody: c.ResponseBody,
					})
				}
				return out
			}
			lab.Writes = func(target model.ObjectRef) []v1alpha1.WriteReplay {
				return labpkg.FromBodies(capture.Bodies(recent(target), 20), 20)
			}
			lab.Seed = func(target model.ObjectRef) []v1alpha1.SeedBody {
				return labpkg.FromSeedSamples(capture.SeedSamples(recent(target), 20), 20)
			}
		}
		switch err := lab.Start(ctx); {
		case err != nil:
			log.Warn("the Lab verifies nothing", "reason", err)
		case cfg.VerifyRollbacks:
			b.Lab, b.ImageLab = lab, lab
			b.ValueLab = valueLab(lab)
			log.Info("the Lab verifies proposed rollbacks and fixes to code", "pool", cfg.LabNamespaces)
		default:
			b.ImageLab, b.ValueLab = lab, valueLab(lab)
			log.Info("the Lab races fixes to code", "pool", cfg.LabNamespaces)
		}
	}
	if cfg.FixerNamespace != "" && dyn != nil {
		f := &fixes.Fixes{Dynamic: dyn, Namespace: cfg.FixerNamespace, Log: log,
			OnUpdate: func(ctx context.Context, u fixes.Update) {
				s := u.Status
				var code *model.FixCode
				if c := s.Code; c != nil {
					code = &model.FixCode{PullRequest: c.PullRequest, Fixer: c.Fixer, Attempt: int(c.Attempt), Function: c.Function, Change: c.Change, Image: c.Image,
						Verdict: c.Verdict, Message: c.Message}
				}
				if err := b.OnFixUpdate(ctx, brain.FixUpdate{IncidentID: u.IncidentID, Fix: u.Name, Phase: string(s.Phase), Message: s.Message,
					Owner: s.Owner, Resume: s.Resume, Repository: s.Repository, PullRequest: s.PullRequest, MergeCommit: s.MergeCommit,
					Change: s.Change, Code: code}); err != nil {
					log.Error("could not record the Fixer's step", "fix", u.Name, "error", err)
				}
			}}
		if err := f.Start(ctx); err != nil {
			log.Warn("the Fixer does not make rollbacks hold at their source", "reason", err)
		} else {
			b.Fixer, b.CodeFixWithoutRollback = f, cfg.CodeFixWithoutRollback
			log.Info("the Fixer makes verified rollbacks hold at their source", "namespace", cfg.FixerNamespace)
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
	// The node agent's OnBatch hook turns tailed lines into log evidence for
	// open incidents. It does not feed the error-rate detector (ReasonSample
	// does), so API sampling and the agent do not double-count. Cgroup
	// samples stay on the batch for a later fact hook.
	if nodeLogs != nil {
		redactor := redact.New()
		nodeLogs.OnBatch = func(batch nodeagent.Batch) {
			type key struct{ ns, pod, container string }
			grouped := map[key][]nodeagent.Line{}
			for _, line := range batch.Lines {
				k := key{line.Namespace, line.Pod, line.Container}
				grouped[k] = append(grouped[k], line)
			}
			for k, lines := range grouped {
				workload, rev, ok := w.ResolvePod(k.ns, k.pod)
				if !ok {
					continue
				}
				var raw strings.Builder
				var from, to time.Time
				for _, line := range lines {
					if from.IsZero() || line.At.Before(from) {
						from = line.At
					}
					if line.At.After(to) {
						to = line.At
					}
					ts := line.At.UTC().Format(time.RFC3339Nano)
					raw.WriteString(ts)
					raw.WriteByte(' ')
					raw.WriteString(line.Text)
					raw.WriteByte('\n')
				}
				sum := logs.Summarize([]byte(raw.String()), redactor)
				ev := watcher.LogEvidence{
					Workload: workload, Revision: rev, Pod: k.pod, Container: k.container,
					Reason: "node-agent", CollectedAt: time.Now().UTC(), Bytes: raw.Len(), Summary: sum,
				}
				if !from.IsZero() && !to.IsZero() {
					ev.Exposure = to.Sub(from).Seconds()
				}
				if w.Logs != nil && w.Logs.OnEvidence != nil {
					w.Logs.OnEvidence(ctx, ev)
				} else if err := b.OnEvidence(ctx, ev); err != nil {
					log.Error("could not record node-agent log evidence", "workload", workload.String(), "pod", k.pod, "error", err)
				}
			}
		}
	}
	return w, acting, nil
}

// valueLab asks the Lab to race the failing revision with learned memory
// and CPU limits in place.
func valueLab(lab *verify.Lab) func(context.Context, model.ID, model.ObjectRef, string, map[string]string, map[string]string, map[string]string) (string, bool, error) {
	return func(ctx context.Context, incident model.ID, target model.ObjectRef, revision string, memory, cpu, disk map[string]string) (string, bool, error) {
		var overrides []v1alpha1.ValueOverride
		for _, container := range slices.Sorted(maps.Keys(memory)) {
			overrides = append(overrides, v1alpha1.ValueOverride{Container: container, Field: "limits.memory", Value: memory[container]})
		}
		for _, container := range slices.Sorted(maps.Keys(cpu)) {
			overrides = append(overrides, v1alpha1.ValueOverride{Container: container, Field: "limits.cpu", Value: cpu[container]})
		}
		for _, container := range slices.Sorted(maps.Keys(disk)) {
			overrides = append(overrides, v1alpha1.ValueOverride{Container: container, Field: "limits.ephemeral-storage", Value: disk[container]})
		}
		return lab.VerifyValues(ctx, incident, target, revision, overrides)
	}
}

// restoreBaselines takes back the learned baselines a previous run kept.
func restoreBaselines(d *watcher.CountDetector, path string, now time.Time) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // beside the ledger the operator configured
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var baselines []watcher.Baseline
	if err := json.Unmarshal(raw, &baselines); err != nil {
		return 0, err
	}
	return d.Restore(baselines, now), nil
}

// keepBaselines writes the learned baselines every interval and once more
// as the controller stops, so a restart need not learn them again.
func keepBaselines(ctx context.Context, d *watcher.CountDetector, path string, interval time.Duration, log *slog.Logger) {
	write := func() {
		raw, err := json.Marshal(d.Baselines(time.Now()))
		if err == nil {
			tmp := path + ".tmp"
			if err = os.WriteFile(tmp, raw, 0o600); err == nil {
				err = os.Rename(tmp, path)
			}
		}
		if err != nil {
			log.Warn("could not keep the learned baselines", "path", path, "error", err)
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			write()
			return
		case <-t.C:
			write()
		}
	}
}
