package watcher

import (
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func newTestDetector() *CountDetector {
	return NewCountDetector(5*time.Minute, 15*time.Second, 1, 7*24*time.Hour)
}

func run(d *CountDetector, from time.Time, ticks int, each func(i int, now time.Time) []Fact) []Detection {
	var out []Detection
	now := from
	for i := range ticks {
		if each != nil {
			d.Observe(each(i, now)...)
		}
		now = now.Add(d.Tick)
		out = append(out, d.Advance(now)...)
	}
	return out
}

func TestQuietWorkloadsRaiseNothing(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	if got := run(d, t0, 2000, nil); len(got) != 0 {
		t.Fatalf("detections without facts: %+v", got)
	}
}

func TestStuckImagePullIsDetectedOnceAndEnds(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	run(d, t0, 240, nil) // an hour of quiet history
	start := t0.Add(time.Hour)
	stuck := func(_ int, now time.Time) []Fact {
		return []Fact{{Kind: FactImagePullError, Workload: web, Pod: "web-2", Revision: "new", At: now, Detail: "ImagePullBackOff"}}
	}
	got := run(d, start, 20, stuck)
	if len(got) != 1 || got[0].Kind != FactImagePullError || got[0].Ended {
		t.Fatalf("want one started detection, got %+v", got)
	}
	if got[0].Revisions["new"] == 0 || got[0].TailProbability >= got[0].Alpha {
		t.Fatalf("detection = %+v", got[0])
	}
	ends := run(d, start.Add(20*d.Tick), 30, nil)
	if len(ends) != 1 || !ends[0].Ended || ends[0].ID != got[0].ID {
		t.Fatalf("want the episode to end once the facts stop, got %+v", ends)
	}
}

func TestADetectionCarriesTheWholeWindow(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	run(d, t0, 240, nil)
	oomThenCrashLoop := func(i int, now time.Time) []Fact {
		facts := []Fact{{Kind: FactCrashLoop, Workload: web, At: now}}
		if i == 0 {
			facts = append(facts, Fact{Kind: FactOOMKilled, Workload: web, At: now})
		}
		return facts
	}
	var started []Detection
	for _, det := range run(d, t0.Add(time.Hour), 4, oomThenCrashLoop) {
		if !det.Ended {
			started = append(started, det)
		}
	}
	if len(started) != 1 || started[0].Kind != FactCrashLoop || started[0].Window[FactOOMKilled] != 1 {
		t.Fatalf("detections = %+v, want one crash-loop detection whose window holds the OOM kill", started)
	}
}

func TestOneRestartInAQuietWorkloadIsNotAnIncident(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	run(d, t0, 240, nil)
	once := func(i int, now time.Time) []Fact {
		if i == 0 {
			return []Fact{{Kind: FactRestarted, Workload: web, At: now}}
		}
		return nil
	}
	if got := run(d, t0.Add(time.Hour), 40, once); len(got) != 0 {
		t.Fatalf("a single restart raised %+v", got)
	}
}

func TestTraceExplainsWhyASeriesDidNotAlert(t *testing.T) {
	d := newTestDetector()
	var traces []Trace
	d.Trace = func(tr Trace) { traces = append(traces, tr) }
	d.Register(web)
	run(d, t0, 240, nil)
	if len(traces) != 0 {
		t.Fatalf("series without facts were traced: %+v", traces)
	}
	d.Observe(Fact{Kind: FactRestarted, Workload: web, At: t0.Add(time.Hour)})
	if got := d.Advance(t0.Add(time.Hour + d.Tick)); len(got) != 0 {
		t.Fatalf("a single restart raised %+v", got)
	}
	if len(traces) != 1 {
		t.Fatalf("traces = %+v, want one", traces)
	}
	tr := traces[0]
	if tr.Workload != web || tr.Kind != FactRestarted || tr.Count != 1 || tr.InEpisode ||
		tr.TailProbability < tr.Alpha || tr.HistoryExposure == 0 {
		t.Fatalf("trace = %+v, want one restart above alpha with history", tr)
	}
}

func TestAWorkloadThatAlwaysRestartsLearnsItsOwnBaseline(t *testing.T) {
	d := newTestDetector()
	job := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "batch", Name: "flaky"}
	d.Register(job)
	restartEveryMinute := func(i int, now time.Time) []Fact {
		if i%4 == 0 {
			return []Fact{{Kind: FactRestarted, Workload: job, At: now}}
		}
		return nil
	}
	// The first window can alarm before there is any history; after a day of
	// the same behavior, it must not.
	run(d, t0, 24*240, restartEveryMinute)
	if got := run(d, t0.Add(24*time.Hour), 240, restartEveryMinute); len(got) != 0 {
		t.Fatalf("routine restarts still raise detections after a day: %+v", got)
	}
}

// Symptoms that accompany an incident must not become part of the baseline:
// after an OOM incident with crash loops, a later crash loop is still news.
func TestAnIncidentDoesNotTeachItsSymptomsAreNormal(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	run(d, t0, 240, nil)
	// Six hours of OOM kills, with one crash-loop fact per window: never
	// significant on its own, so it would leak into the baseline unnoticed.
	oomWithSparseCrashLoops := func(i int, now time.Time) []Fact {
		var facts []Fact
		if i%4 == 0 {
			facts = append(facts, Fact{Kind: FactOOMKilled, Workload: web, At: now})
		}
		if i%20 == 0 {
			facts = append(facts, Fact{Kind: FactCrashLoop, Workload: web, At: now})
		}
		return facts
	}
	run(d, t0.Add(time.Hour), 6*240, oomWithSparseCrashLoops)
	run(d, t0.Add(7*time.Hour), 120, nil)
	crashLoop := func(_ int, now time.Time) []Fact { return []Fact{{Kind: FactCrashLoop, Workload: web, At: now}} }
	var started []Detection
	for _, det := range run(d, t0.Add(8*time.Hour), 3, crashLoop) {
		if !det.Ended && det.Kind == FactCrashLoop {
			started = append(started, det)
		}
	}
	if len(started) != 1 {
		t.Fatal("a new crash loop after an OOM incident was not detected within three ticks")
	}
}

// A deleted workload's incident must be able to resolve.
func TestForgettingAWorkloadEndsItsEpisodes(t *testing.T) {
	d := newTestDetector()
	d.Register(web)
	run(d, t0, 240, nil)
	stuck := func(_ int, now time.Time) []Fact {
		return []Fact{{Kind: FactImagePullError, Workload: web, At: now}}
	}
	started := run(d, t0.Add(time.Hour), 10, stuck)
	if len(started) != 1 {
		t.Fatalf("detections = %+v", started)
	}
	ended := d.Forget(web, t0.Add(2*time.Hour))
	if len(ended) != 1 || !ended[0].Ended || ended[0].ID != started[0].ID {
		t.Fatalf("Forget = %+v, want the episode's end", ended)
	}
	if again := d.Forget(web, t0.Add(2*time.Hour)); len(again) != 0 {
		t.Fatalf("a second Forget = %+v", again)
	}
}

func TestAlphaComesFromTheBudget(t *testing.T) {
	d := newTestDetector()
	ticksPerWeek := 7 * 24 * 3600 / 15.0
	want := 1 / (ticksPerWeek * float64(len(FactKinds)))
	if got := d.Alpha(); got < want*0.999 || got > want*1.001 {
		t.Fatalf("alpha = %g, want %g", got, want)
	}
}
