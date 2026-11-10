package watcher

import (
	"maps"
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
	// Window counts every fact kind of the workload in the window when the
	// episode started, including kinds not significant on their own: one OOM
	// kill among crash loops says what kind of crash it is.
	Window map[FactKind]int
}

// CountDetector turns facts into detections. Each (workload, fact kind) is a
// series with a decayed history of counts and exposure. At every tick, the
// count in the sliding window is compared with the Gamma–Poisson predictive
// distribution given the series' history and a prior pooled across workloads.
// While any series of a workload is in an episode, all of that workload's
// history is frozen, so an incident never teaches the baseline that its
// symptoms are normal.
type CountDetector struct {
	Window time.Duration
	Tick   time.Duration
	// FalseAlarmsPerWeek is the budget per workload per week. It is split
	// evenly over fact kinds and ticks to give each test's significance level.
	FalseAlarmsPerWeek float64
	HalfLife           time.Duration
	// MinRatio is the effect an episode needs besides significance: a window
	// count of at least MinRatio times the expected one. With much history
	// the predictive is near Poisson, and bursty series such as error logs
	// would otherwise alarm on small, significant changes. An episode ends
	// when its window is empty or back under three quarters of that.
	MinRatio float64
	// WarmUp is the history a learned series needs before it may alarm.
	WarmUp time.Duration
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
	Expected        float64
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
	// exposure is the time observed, for learned series.
	exposure float64
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
		Window: window, Tick: tick, FalseAlarmsPerWeek: falseAlarmsPerWeek, HalfLife: halfLife, MinRatio: 2,
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

// Forget drops a deleted workload and returns the ends of its open episodes,
// so its incident can resolve.
func (d *CountDetector) Forget(ref model.ObjectRef, now time.Time) []Detection {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workloads[ref.String()]
	delete(d.workloads, ref.String())
	if w == nil {
		return nil
	}
	var ended []Detection
	for _, kind := range FactKinds {
		if s := w.series[kind]; s != nil && s.episode != nil {
			end := *s.episode
			end.Ended, end.At, end.Count = true, now, 0
			ended = append(ended, end)
		}
	}
	return ended
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

// Expose records time observed on a learned series in the current tick: the
// seconds of logs a sample read, whether or not they held errors. A learned
// series' window and history count this exposure, not the clock, so samples
// the budget delays or skips change how much is known, never the rate.
func (d *CountDetector) Expose(ref model.ObjectRef, kind FactKind, seconds float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.register(ref).series[kind]; s != nil && learnedKinds[kind] && seconds > 0 {
		s.buckets[s.cur].exposure += seconds
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
		if learnedKinds[k] {
			priors[k] = learnedPrior
			continue
		}
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
		windows := make(map[FactKind]windowSummary, len(FactKinds))
		counts := map[FactKind]int{}
		for _, kind := range FactKinds {
			ws := w.series[kind].window()
			windows[kind] = ws
			if ws.count > 0 {
				counts[kind] = ws.count
			}
		}
		for _, kind := range FactKinds {
			s, ws := w.series[kind], windows[kind]
			learned := learnedKinds[kind]
			outlived := s.episode != nil && learned && now.Sub(s.episode.At) >= learnedEpisodeLimit
			span := window
			if learned {
				span = ws.exposure
				if span == 0 && !outlived {
					// Nothing was read: no evidence either way.
					continue
				}
			}
			post := priors[kind].Posterior(s.histCount, s.histExp)
			expected := post.Mean() * span
			if ws.count == 0 || s.episode != nil && float64(ws.count) < 0.75*d.MinRatio*expected || outlived {
				if s.episode != nil {
					end := *s.episode
					end.Ended, end.At, end.Count = true, now, 0
					s.episode = nil
					out = append(out, end)
				}
				if outlived {
					s.histCount, s.histExp = 0, 0
				}
				continue
			}
			p := post.PredictiveTail(ws.count, span)
			if d.Trace != nil {
				d.Trace(Trace{Workload: w.ref, Kind: kind, Count: ws.count, TailProbability: p, Alpha: alpha, Expected: expected,
					InEpisode: s.episode != nil, HistoryCount: s.histCount, HistoryExposure: s.histExp})
			}
			warm := !learned || s.histExp >= max(d.WarmUp, d.Window).Seconds()
			if s.episode == nil && p < alpha && float64(ws.count) >= d.MinRatio*expected && warm {
				det := Detection{ID: model.NewID(), Workload: w.ref, Kind: kind, Count: ws.count, TailProbability: p,
					Alpha: alpha, Onset: ws.onset, At: now, Revisions: ws.revs, Sample: ws.sample, Window: maps.Clone(counts)}
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
		for kind, s := range w.series {
			next := (s.cur + 1) % len(s.buckets)
			if learning {
				exposure := tick
				if learnedKinds[kind] {
					exposure = s.buckets[next].exposure
				}
				s.histCount = s.histCount*decay + float64(s.buckets[next].count)
				s.histExp = s.histExp*decay + exposure
			}
			s.buckets[next] = bucket{}
			s.cur = next
		}
	}
	return out
}

// learnedKinds are series whose normal level is the workload's own, not the
// zero the pooled prior expects. They take a prior worth seconds of
// exposure, learn for WarmUp before they may alarm, and an episode of theirs
// ends after learnedEpisodeLimit, when the series learns afresh: a level
// that holds that long is the workload's new normal, or an incident already
// on record that alarming again would not help.
var learnedKinds = map[FactKind]bool{FactLogErrors: true}

var learnedPrior = stats.Gamma{Shape: 0.5, Rate: 10}

const learnedEpisodeLimit = time.Hour

type windowSummary struct {
	count    int
	exposure float64
	onset    time.Time
	revs     map[string]int
	sample   string
}

func (s *series) window() windowSummary {
	ws := windowSummary{revs: map[string]int{}}
	for _, b := range s.buckets {
		ws.exposure += b.exposure
		if b.count == 0 {
			continue
		}
		ws.count += b.count
		if ws.onset.IsZero() || b.first.Before(ws.onset) {
			ws.onset = b.first
		}
		for r, n := range b.revs {
			ws.revs[r] += n
		}
		if ws.sample == "" {
			ws.sample = b.sample
		}
	}
	return ws
}

// Baseline is one series' learned history: its decayed fact count and
// exposure, as of At.
type Baseline struct {
	Workload string    `json:"workload"`
	Kind     FactKind  `json:"kind"`
	Count    float64   `json:"count"`
	Exposure float64   `json:"exposure"`
	At       time.Time `json:"at"`
}

// Baselines are the series' learned histories, for keeping across a
// restart: series in an episode keep their frozen history.
func (d *CountDetector) Baselines(now time.Time) []Baseline {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Baseline
	for key, w := range d.workloads {
		for kind, s := range w.series {
			if s.histExp > 0 {
				out = append(out, Baseline{Workload: key, Kind: kind, Count: s.histCount, Exposure: s.histExp, At: now})
			}
		}
	}
	return out
}

// Restore takes back learned histories after a restart, decayed for the time
// Sentinel was down, so a workload's usual rate need not be learned again
// before it may alarm. A workload not yet seen is registered with it.
func (d *CountDetector) Restore(baselines []Baseline, now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, b := range baselines {
		ref, err := model.ParseObjectRef(b.Workload)
		if err != nil || b.Exposure <= 0 || b.Count < 0 {
			continue
		}
		w := d.register(ref)
		s := w.series[b.Kind]
		if s == nil {
			continue
		}
		decay := 1.0
		if d.HalfLife > 0 && now.After(b.At) {
			decay = math.Pow(0.5, now.Sub(b.At).Seconds()/d.HalfLife.Seconds())
		}
		s.histCount, s.histExp = b.Count*decay, b.Exposure*decay
		n++
	}
	return n
}
