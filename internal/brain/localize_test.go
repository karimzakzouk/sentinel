package brain

import (
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
	"github.com/karimzakzouk/sentinel/internal/topology"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

var (
	cart    = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "cartservice"}
	redis   = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "redis-cart"}
	catalog = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "productcatalogservice"}
)

var ranker = EvidenceRanking{Lookback: time.Hour, Typical: 5 * time.Minute}

// The cart service shows the errors; Redis, which it depends on, lost its
// endpoints after a scale change. The cause is upstream.
func TestLocalizationLooksUpstream(t *testing.T) {
	in := LocalizeInput{Workload: cart, Candidates: []LocalizeCandidate{
		{Workload: cart, Anomalous: []string{"log.new-error"}, ChangedBefore: -1},
		{Workload: redis, Hops: 1, Anomalous: []string{"service.no-endpoints"}, ChangedBefore: 30},
		{Workload: catalog, Hops: 1, ChangedBefore: -1},
	}}
	r, err := ranker.Decide(ctx, in)
	if err != nil || r.Validate() != nil {
		t.Fatalf("ranking = %+v, %v", r, err)
	}
	if r.Items[0].Item.Workload != redis || r.Items[len(r.Items)-1].Item.Workload != catalog {
		t.Fatalf("order = %v, %v, %v", r.Items[0].Item.Workload.Name, r.Items[1].Item.Workload.Name, r.Items[2].Item.Workload.Name)
	}
	if set := r.Set(); len(set) == 0 || set[0].Workload != redis {
		t.Fatalf("confidence set = %+v", set)
	}
}

// With no dependency of its own in trouble, the workload where the symptoms
// are is the cause.
func TestLocalizationStaysWhenDependenciesAreHealthy(t *testing.T) {
	in := LocalizeInput{Workload: cart, Candidates: []LocalizeCandidate{
		{Workload: cart, Anomalous: []string{"pod.image-pull-error"}, ChangedBefore: 60},
		{Workload: redis, Hops: 1, ChangedBefore: -1},
	}}
	r, _ := ranker.Decide(ctx, in)
	if r.Items[0].Item.Workload != cart || r.Items[0].Item.P < 0.9 {
		t.Fatalf("top = %+v", r.Items[0].Item)
	}
}

// A dependency whose resource use left its usual range is a likelier cause
// than one that looks the same as always.
func TestResourceDeviationsCount(t *testing.T) {
	in := LocalizeInput{Workload: cart, Candidates: []LocalizeCandidate{
		{Workload: cart, Anomalous: []string{"log.new-error"}, ChangedBefore: -1},
		{Workload: redis, Hops: 1, ChangedBefore: -1, Deviations: []watcher.Deviation{{Resource: "memory", Score: 9}}},
		{Workload: catalog, Hops: 1, ChangedBefore: -1, Deviations: []watcher.Deviation{{Resource: "cpu", Score: 1}}},
	}}
	r, _ := ranker.Decide(ctx, in)
	byName := map[string]float64{}
	for _, it := range r.Items {
		byName[it.Item.Workload.Name] = it.Item.P
	}
	if byName["redis-cart"] <= byName["productcatalogservice"] {
		t.Fatalf("probabilities = %v", byName)
	}
}

// newGroupingBrain is a Brain whose cart service depends on Redis.
func newGroupingBrain(t *testing.T) (*Brain, *ledger.Ledger) {
	t.Helper()
	b, l := newBrain(t)
	rec := slot.LedgerRecorder{Ledger: l, Source: "/test"}
	b.Localize, _ = slot.New[LocalizeInput, slot.Ranking[Suspect]](slot.BrainLocalize, ranker, nil, rec)
	b.Dependencies = func(ref model.ObjectRef) []topology.Reach {
		if ref == cart {
			return []topology.Reach{{Workload: redis, Hops: 1}}
		}
		return nil
	}
	b.Dependents = func(ref model.ObjectRef) []model.ObjectRef {
		if ref == redis {
			return []model.ObjectRef{cart}
		}
		return nil
	}
	if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeScale, Object: redis, Fields: []string{"replicas:1->0"},
		At: onset.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return b, l
}

var (
	redisGone  = watcher.Detection{ID: model.NewID(), Workload: redis, Kind: watcher.FactNoEndpoints, Count: 3, Onset: onset, At: onset}
	cartErrors = watcher.Detection{ID: model.NewID(), Workload: cart, Kind: watcher.FactNewError, Count: 30,
		Onset: onset.Add(time.Minute), At: onset.Add(time.Minute)}
)

func detect(t *testing.T, b *Brain, ds ...watcher.Detection) {
	t.Helper()
	for _, d := range ds {
		if err := b.OnDetection(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
}

func incidents(t *testing.T, l *ledger.Ledger) []model.Incident {
	t.Helper()
	got, err := ledger.List[model.Incident](ctx, l, model.KindIncident, "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The cart service's errors come after Redis lost its endpoints: they join
// Redis's incident instead of opening their own.
func TestASymptomJoinsItsCausesIncident(t *testing.T) {
	b, l := newGroupingBrain(t)
	detect(t, b, redisGone, cartErrors)
	got := incidents(t, l)
	if len(got) != 1 || len(got[0].Objects) != 2 || got[0].Objects[1] != cart {
		t.Fatalf("incidents = %+v", got)
	}
	recs, _ := slot.Records(ctx, l, slot.BrainLocalize)
	var joinRanking slot.Ranking[Suspect]
	if len(recs) != 2 || unmarshal(recs[1].Output, &joinRanking) != nil || joinRanking.Items[0].Item.Workload != redis {
		t.Fatalf("the join was not decided by ranking redis-cart first: %d records", len(recs))
	}
	// The symptom's facts do not change the cause's class.
	if top := topClass(t, l); top.FaultClass != ClassDependencyUnavailable {
		t.Fatalf("class = %s", top.FaultClass)
	}
}

// The cart service's errors come first; when Redis's own trouble shows, the
// cart incident folds into Redis's.
func TestAnEarlierSymptomIncidentMergesIntoItsCause(t *testing.T) {
	b, l := newGroupingBrain(t)
	early := cartErrors
	early.Onset, early.At = onset.Add(-30*time.Second), onset.Add(-30*time.Second)
	detect(t, b, early, redisGone)
	var merged, cause *model.Incident
	for _, inc := range incidents(t, l) {
		switch {
		case inc.MergedInto != "":
			merged = &inc
		default:
			cause = &inc
		}
	}
	if merged == nil || cause == nil || merged.MergedInto != cause.ID || merged.State != model.IncidentClosed || len(cause.Objects) != 2 {
		t.Fatalf("merged = %+v, cause = %+v", merged, cause)
	}
	// Later symptoms go straight to the cause's incident.
	again := cartErrors
	again.ID = model.NewID()
	detect(t, b, again)
	if n := len(incidents(t, l)); n != 2 {
		t.Fatalf("incidents = %d after a later symptom, want the same two", n)
	}
	// It resolves only when every workload's episodes have ended.
	for _, d := range []watcher.Detection{redisGone, early} {
		d.Ended = true
		detect(t, b, d)
	}
	b.Now = func() time.Time { return onset.Add(time.Hour) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.route[cart.String()]; got == nil || got.rec.ID != cause.ID {
		t.Fatal("the cause resolved while a symptom's episode was still active")
	}
	again.Ended = true
	detect(t, b, again)
	b.Now = func() time.Time { return onset.Add(2 * time.Hour) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if b.route[cart.String()] != nil || b.route[redis.String()] != nil {
		t.Fatal("the incident did not resolve once every episode ended")
	}
}

// A dependent with a bad change of its own has its own incident, even while
// its dependency is in trouble.
func TestAnIndependentFaultKeepsItsOwnIncident(t *testing.T) {
	b, l := newGroupingBrain(t)
	if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: cart, Fields: []string{"image:server"},
		At: onset}); err != nil {
		t.Fatal(err)
	}
	bad := watcher.Detection{ID: model.NewID(), Workload: cart, Kind: watcher.FactImagePullError, Count: 10,
		Onset: onset.Add(30 * time.Second), At: onset.Add(30 * time.Second)}
	detect(t, b, redisGone, bad)
	if got := incidents(t, l); len(got) != 2 {
		t.Fatalf("incidents = %d, want two independent ones", len(got))
	}
}

// No dependency can make a workload's image fail to pull, even one whose
// own image fails to pull after a change of its own, and whatever the
// ranking says: the dependent's incident stays its own, both ways round.
func TestAnOwnFaultNeverJoinsADependencysIncident(t *testing.T) {
	for _, first := range []string{"dependency", "dependent"} {
		t.Run(first+" first", func(t *testing.T) {
			b, l := newGroupingBrain(t)
			if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: redis,
				Fields: []string{"image:redis"}, At: onset.Add(-30 * time.Second)}); err != nil {
				t.Fatal(err)
			}
			redisBad := watcher.Detection{ID: model.NewID(), Workload: redis, Kind: watcher.FactImagePullError, Count: 10,
				Onset: onset, At: onset}
			cartBad := watcher.Detection{ID: model.NewID(), Workload: cart, Kind: watcher.FactNoEndpoints, Count: 3,
				Window: map[watcher.FactKind]int{watcher.FactImagePullError: 4}, Onset: onset.Add(time.Minute), At: onset.Add(time.Minute)}
			if first == "dependent" {
				cartBad.Onset, cartBad.At = onset.Add(-time.Minute), onset.Add(-time.Minute)
				detect(t, b, cartBad, redisBad)
			} else {
				detect(t, b, redisBad, cartBad)
			}
			got := incidents(t, l)
			if len(got) != 2 || got[0].State != model.IncidentOpen || got[1].State != model.IncidentOpen {
				t.Fatalf("incidents = %+v, want two open ones", got)
			}
		})
	}
}
