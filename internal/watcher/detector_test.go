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

func TestAlphaComesFromTheBudget(t *testing.T) {
	d := newTestDetector()
	ticksPerWeek := 7 * 24 * 3600 / 15.0
	want := 1 / (ticksPerWeek * float64(len(FactKinds)))
	if got := d.Alpha(); got < want*0.999 || got > want*1.001 {
		t.Fatalf("alpha = %g, want %g", got, want)
	}
}
