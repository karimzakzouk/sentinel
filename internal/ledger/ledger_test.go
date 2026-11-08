package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func openTemp(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func incidentDraft(inc model.Incident, verb string) Draft {
	return RecordDraft("/test", model.KindIncident, verb, inc.ID, inc.ID, inc)
}

func TestAppendChainsHashesAndVerifies(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)

	inc := model.Incident{ID: model.NewID(), State: model.IncidentOpen, Title: "cart OOM"}
	events, err := l.Append(ctx, incidentDraft(inc, "opened"), Draft{Type: "sentinel.note", Source: "/test", Data: "hello"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected sequence numbers: %+v", events)
	}
	if events[0].PrevHash != GenesisHash || events[1].PrevHash != events[0].Hash {
		t.Fatal("events are not linked into a chain")
	}
	if n, err := l.Verify(ctx); err != nil || n != 2 {
		t.Fatalf("Verify = %d, %v; want 2, nil", n, err)
	}
	seq, hash, err := l.Head(ctx)
	if err != nil || seq != 2 || hash != events[1].Hash {
		t.Fatalf("Head = %d %s %v", seq, hash, err)
	}
}

func TestProjectionKeepsLatestState(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)

	inc := model.Incident{ID: model.NewID(), State: model.IncidentOpen, Title: "bad rollout"}
	sig := model.Signal{ID: model.NewID(), Type: "pod.crashloop", TailProbability: 0.001, Trust: model.TrustObserved}
	if _, err := l.Append(ctx,
		incidentDraft(inc, "opened"),
		RecordDraft("/test", model.KindSignal, "observed", sig.ID, inc.ID, sig),
	); err != nil {
		t.Fatal(err)
	}
	inc.State = model.IncidentMitigating
	if _, err := l.Append(ctx, incidentDraft(inc, "state-changed")); err != nil {
		t.Fatal(err)
	}

	var got model.Incident
	found, err := l.Get(ctx, model.KindIncident, inc.ID, &got)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	if got.State != model.IncidentMitigating {
		t.Fatalf("state = %s, want %s", got.State, model.IncidentMitigating)
	}
	signals, err := List[model.Signal](ctx, l, model.KindSignal, inc.ID)
	if err != nil || len(signals) != 1 || signals[0].ID != sig.ID {
		t.Fatalf("List signals = %+v, %v", signals, err)
	}
	if found, _ := l.Get(ctx, model.KindIncident, model.NewID(), &got); found {
		t.Fatal("Get found a record that was never written")
	}
}

func TestAnIncidentsHistoryAndTheLatestOfAType(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	a, b := model.Incident{ID: model.NewID(), Title: "a"}, model.Incident{ID: model.NewID(), Title: "b"}
	sig := model.Signal{ID: model.NewID(), Type: "pod.crash-loop"}
	if _, err := l.Append(ctx, incidentDraft(a, "opened"), incidentDraft(b, "opened"),
		RecordDraft("/test", model.KindSignal, "observed", sig.ID, a.ID, sig),
		Draft{Type: "sentinel.capability.reported", Source: "/test", Data: 1}); err != nil {
		t.Fatal(err)
	}
	a.State = model.IncidentResolved
	if _, err := l.Append(ctx, incidentDraft(a, "resolved"), Draft{Type: "sentinel.capability.reported", Source: "/test", Data: 2}); err != nil {
		t.Fatal(err)
	}
	history, err := l.IncidentEvents(ctx, a.ID)
	if err != nil || len(history) != 3 || history[0].Type != "sentinel.incident.opened" ||
		history[1].Type != "sentinel.signal.observed" || history[2].Type != "sentinel.incident.resolved" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	latest, err := l.Latest(ctx, "sentinel.capability.reported", 1)
	if err != nil || len(latest) != 1 || string(latest[0].Data) != "2" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}

func TestRestartContinuesTheChain(t *testing.T) {
	ctx := context.Background()
	l, path := openTemp(t)
	if _, err := l.Append(ctx, Draft{Type: "sentinel.note", Source: "/test", Data: 1}); err != nil {
		t.Fatal(err)
	}
	_, before, _ := l.Head(ctx)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	events, err := reopened.Append(ctx, Draft{Type: "sentinel.note", Source: "/test", Data: 2})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Seq != 2 || events[0].PrevHash != before {
		t.Fatalf("after restart got seq %d prev %s; want 2 and %s", events[0].Seq, events[0].PrevHash, before)
	}
	if _, err := reopened.Verify(ctx); err != nil {
		t.Fatalf("Verify after restart: %v", err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	if _, err := l.Append(ctx, Draft{Type: "sentinel.note", Source: "/test", Data: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `UPDATE events SET data = '"y"' WHERE seq = 1`); err == nil {
		t.Fatal("UPDATE on events succeeded")
	}
	if _, err := l.db.ExecContext(ctx, `DELETE FROM events`); err == nil {
		t.Fatal("DELETE on events succeeded")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	for i := range 3 {
		if _, err := l.Append(ctx, Draft{Type: "sentinel.note", Source: "/test", Data: i}); err != nil {
			t.Fatal(err)
		}
	}
	// Someone with write access to the file can drop the triggers; the hash
	// chain still exposes the edit.
	if _, err := l.db.ExecContext(ctx, `DROP TRIGGER events_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `UPDATE events SET data = '99' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	n, err := l.Verify(ctx)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("Verify error = %v, want ErrTampered", err)
	}
	if n != 1 {
		t.Fatalf("Verify checked %d events before the break, want 1", n)
	}
}

func TestRebuildRestoresProjections(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	inc := model.Incident{ID: model.NewID(), State: model.IncidentOpen}
	if _, err := l.Append(ctx, incidentDraft(inc, "opened")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `DELETE FROM entities`); err != nil {
		t.Fatal(err)
	}
	if err := l.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	var got model.Incident
	if found, err := l.Get(ctx, model.KindIncident, inc.ID, &got); err != nil || !found {
		t.Fatalf("after Rebuild Get = %v, %v", found, err)
	}
}

func TestConcurrentAppendsStayContiguous(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 10 {
				if _, err := l.Append(ctx, Draft{Type: "sentinel.note", Source: "/test", Data: w*100 + i}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if n, err := l.Verify(ctx); err != nil || n != 80 {
		t.Fatalf("Verify = %d, %v; want 80, nil", n, err)
	}
}

func TestAppendIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	_, err := l.Append(ctx,
		Draft{Type: "sentinel.note", Source: "/test", Data: "ok"},
		Draft{Type: "sentinel.note", Source: "/test", Data: func() {}},
	)
	if err == nil {
		t.Fatal("Append accepted unmarshalable data")
	}
	if seq, _, _ := l.Head(ctx); seq != 0 {
		t.Fatalf("a failed Append left %d events behind", seq)
	}
}

func TestCloudEventEnvelope(t *testing.T) {
	e := Event{Seq: 7, ID: "id", Type: "sentinel.incident.opened", Source: "/watcher", Subject: "s",
		Time: time.Unix(0, 0), EntityKind: model.KindIncident, EntityID: "i", IncidentID: "i", Data: []byte(`{}`)}
	ce := e.CloudEvent()
	for _, k := range []string{"specversion", "id", "source", "type", "time", "sequence", "hash", "prevhash", "entitykind", "incidentid"} {
		if _, ok := ce[k]; !ok {
			t.Errorf("CloudEvent is missing %q", k)
		}
	}
	if ce["specversion"] != "1.0" || ce["sequence"] != "7" {
		t.Errorf("unexpected envelope: %v", ce)
	}
}
