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

func TestTheBrainLocalizesWithOtherIncidentsEvidence(t *testing.T) {
	b, l := newBrain(t)
	rec := slot.LedgerRecorder{Ledger: l, Source: "/test"}
	b.Localize, _ = slot.New[LocalizeInput, slot.Ranking[Suspect]](slot.BrainLocalize, ranker, nil, rec)
	b.Dependencies = func(ref model.ObjectRef) []topology.Reach {
		if ref == cart {
			return []topology.Reach{{Workload: redis, Hops: 1}}
		}
		return nil
	}
	if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeScale, Object: redis, Fields: []string{"replicas:1->0"},
		At: onset.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []watcher.Detection{
		{ID: model.NewID(), Workload: redis, Kind: watcher.FactNoEndpoints, Count: 3, Onset: onset, At: onset},
		{ID: model.NewID(), Workload: cart, Kind: watcher.FactNewError, Count: 30, Onset: onset.Add(time.Minute), At: onset.Add(time.Minute)},
	} {
		if err := b.OnDetection(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := slot.Records(ctx, l, slot.BrainLocalize)
	if err != nil || len(recs) != 2 {
		t.Fatalf("localize records = %d, %v", len(recs), err)
	}
	var cartRanking slot.Ranking[Suspect]
	if err := unmarshal(recs[1].Output, &cartRanking); err != nil {
		t.Fatal(err)
	}
	if top := cartRanking.Items[0].Item; top.Workload != redis {
		t.Fatalf("the cart incident's top suspect is %s, want redis-cart", top.Workload.Name)
	}
	if incidents, _ := ledger.List[model.Incident](ctx, l, model.KindIncident, ""); len(incidents) != 2 {
		t.Fatalf("incidents = %d", len(incidents))
	}
}
