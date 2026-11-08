package watcher

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/stats"
)

// Detection is the start or the end of a significant run of facts for one
// workload and fact kind.
type Detection struct {
	ID       model.ID
	Workload model.ObjectRef
	Kind     FactKind
	// Count is the number of facts in the current window.
	Count int
	// TailProbability is the chance of a count at least this high under the
	// workload's normal behavior.
	TailProbability float64
	Alpha           float64
	Onset           time.Time
	At              time.Time
	Ended           bool
	// Revisions counts the window's facts per pod revision.
	Revisions map[string]int
	// Sample is one fact's detail. It is untrusted text.
	Sample string
}

// CountDetector turns facts into detections. Each (workload, fact kind) is a
// series with a decayed history of counts and exposure. At every tick, the
// count in the sliding window is compared with the Gamma–Poisson predictive
// distribution given the series' history and a prior pooled across workloads.
// History is frozen while a series is in an episode, so an incident never
// teaches the baseline that incidents are normal.
type CountDetector struct {
	Window time.Duration
	Tick   time.Duration
	// FalseAlarmsPerWeek is the budget per workload per week. It is split
	// evenly over fact kinds and ticks to give each test's significance level.
	FalseAlarmsPerWeek float64
	HalfLife           time.Duration
	// Hyper is the hyperprior over per-second fact rates.
	Hyper stats.Gamma
	// Trace, when set, is called for every series with facts in its window,
	// so "why did it not alert?" can be answered from the log.
	Trace func(t Trace)

	mu        sync.Mutex
	workloads map[string]*workloadSeries
}

// Trace is one series' evaluation at one tick.
type Trace struct {
	Workload        model.ObjectRef
	Kind            FactKind
	Count           int
	TailProbability float64
	Alpha           float64
	InEpisode       bool
	HistoryCount    float64
	HistoryExposure float64
}

type workloadSeries struct {
	ref    model.ObjectRef
	series map[FactKind]*series
}

type bucket struct {
	count  int
	first  time.Time
	revs   map[string]int
	sample string
}

type series struct {
	buckets            []bucket
	cur                int
	histCount, histExp float64
	episode            *Detection
}

// NewCountDetector returns a detector with the given sampling settings.
func NewCountDetector(window, tick time.Duration, falseAlarmsPerWeek float64, halfLife time.Duration) *CountDetector {
	week := 7 * 24 * time.Hour
	return &CountDetector{
		Window: window, Tick: tick, FalseAlarmsPerWeek: falseAlarmsPerWeek, HalfLife: halfLife,
		// A weak hyperprior: about one fact per kind per week, worth half an event of evidence.
		Hyper:     stats.Gamma{Shape: 0.5, Rate: 0.5 * week.Seconds()},
		workloads: map[string]*workloadSeries{},
	}
}

// Alpha is the per-series, per-tick significance level the budget implies.
func (d *CountDetector) Alpha() float64 {
	ticksPerWeek := (7 * 24 * time.Hour).Seconds() / d.Tick.Seconds()
	return d.FalseAlarmsPerWeek / (ticksPerWeek * float64(len(FactKinds)))
}

// Register starts the exposure clock of a workload, so that quiet time counts
// as evidence of a low rate even before any fact arrives.
func (d *CountDetector) Register(ref model.ObjectRef) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.register(ref)
}

func (d *CountDetector) register(ref model.ObjectRef) *workloadSeries {
	key := ref.String()
	if w, ok := d.workloads[key]; ok {
		return w
	}
	n := max(1, int(d.Window/d.Tick))
	w := &workloadSeries{ref: ref, series: map[FactKind]*series{}}
	for _, k := range FactKinds {
		w.series[k] = &series{buckets: make([]bucket, n)}
	}
	d.workloads[key] = w
	return w
}

// Forget drops a deleted workload.
func (d *CountDetector) Forget(ref model.ObjectRef) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.workloads, ref.String())
}

// Observe records facts in the current tick.
func (d *CountDetector) Observe(facts ...Fact) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range facts {
		s := d.register(f.Workload).series[f.Kind]
		if s == nil {
			continue
		}
		b := &s.buckets[s.cur]
		if b.count == 0 || f.At.Before(b.first) {
			b.first = f.At
		}
		b.count++
		if b.revs == nil {
			b.revs = map[string]int{}
		}
		b.revs[f.Revision]++
		if b.sample == "" {
			b.sample = f.Detail
		}
	}
}

// Advance evaluates every series at the end of a tick and moves the window.
// It returns the episodes that started or ended.
func (d *CountDetector) Advance(now time.Time) []Detection {
	d.mu.Lock()
	defer d.mu.Unlock()
	alpha, window, tick := d.Alpha(), d.Window.Seconds(), d.Tick.Seconds()
	decay := math.Exp(-math.Ln2 * tick / d.HalfLife.Seconds())

	priors := map[FactKind]stats.Gamma{}
	for _, k := range FactKinds {
		var hist []stats.Series
		for _, w := range d.workloads {
			s := w.series[k]
			hist = append(hist, stats.Series{Count: s.histCount, Exposure: s.histExp})
		}
		priors[k] = stats.PooledPrior(hist, d.Hyper)
	}

	keys := make([]string, 0, len(d.workloads))
	for k := range d.workloads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Detection
	for _, key := range keys {
		w := d.workloads[key]
		for _, kind := range FactKinds {
			s := w.series[kind]
			count, onset, revs, sample := s.window()
			if count == 0 {
				if s.episode != nil {
					end := *s.episode
					end.Ended, end.At, end.Count = true, now, 0
					s.episode = nil
					out = append(out, end)
				}
				continue
			}
			p := priors[kind].Posterior(s.histCount, s.histExp).PredictiveTail(count, window)
			if d.Trace != nil {
				d.Trace(Trace{Workload: w.ref, Kind: kind, Count: count, TailProbability: p, Alpha: alpha,
					InEpisode: s.episode != nil, HistoryCount: s.histCount, HistoryExposure: s.histExp})
			}
			if s.episode == nil && p < alpha {
				det := Detection{ID: model.NewID(), Workload: w.ref, Kind: kind, Count: count, TailProbability: p,
					Alpha: alpha, Onset: onset, At: now, Revisions: revs, Sample: sample}
				s.episode = &det
				out = append(out, det)
			}
		}
		// While any series of the workload is in an episode, none of its series
		// learns: an OOM's crash loops must not teach the crash-loop baseline
		// that crash loops are normal for this workload.
		learning := true
		for _, s := range w.series {
			if s.episode != nil {
				learning = false
			}
		}
		for _, s := range w.series {
			next := (s.cur + 1) % len(s.buckets)
			if learning {
				s.histCount = s.histCount*decay + float64(s.buckets[next].count)
				s.histExp = s.histExp*decay + tick
			}
			s.buckets[next] = bucket{}
			s.cur = next
		}
	}
	return out
}

func (s *series) window() (int, time.Time, map[string]int, string) {
	var count int
	var onset time.Time
	revs := map[string]int{}
	sample := ""
	for _, b := range s.buckets {
		if b.count == 0 {
			continue
		}
		count += b.count
		if onset.IsZero() || b.first.Before(onset) {
			onset = b.first
		}
		for r, n := range b.revs {
			revs[r] += n
		}
		if sample == "" {
			sample = b.sample
		}
	}
	return count, onset, revs, sample
}
