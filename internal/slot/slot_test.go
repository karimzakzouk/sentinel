package slot

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/ledger"
)

type memRecorder struct {
	mu      sync.Mutex
	records []Record
	fail    error
}

func (m *memRecorder) Record(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.records = append(m.records, r)
	return nil
}

func (m *memRecorder) byRole(role Role) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.records {
		if r.Role == role {
			out = append(out, r)
		}
	}
	return out
}

type classIn struct {
	Restarts int `json:"restarts"`
}

type dist = Distribution[string]

func fixed(id string, d dist) Engine[classIn, dist] {
	return Func[classIn, dist]{Name: id, Fn: func(context.Context, classIn) (dist, error) { return d, nil }}
}

func TestChampionIsUsedAndShadowsAreRecorded(t *testing.T) {
	rec := &memRecorder{}
	champ := fixed("rules", dist{{"oom", 0.9}, {"bad-deploy", 0.1}})
	shadow := fixed("model", dist{{"bad-deploy", 1}})
	s, err := New("brain.classify", champ, []Engine[classIn, dist]{shadow}, rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Decide(context.Background(), classIn{Restarts: 5})
	if err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if got.Top().Value != "oom" {
		t.Fatalf("Decide returned %v, want the champion's answer", got)
	}
	champs, shadows := rec.byRole(RoleChampion), rec.byRole(RoleShadow)
	if len(champs) != 1 || len(shadows) != 1 {
		t.Fatalf("records: %d champion, %d shadow; want 1 and 1", len(champs), len(shadows))
	}
	if champs[0].Call != shadows[0].Call || champs[0].InputSHA256 != shadows[0].InputSHA256 {
		t.Fatal("champion and shadow records are not linked to the same call and input")
	}
	if shadows[0].Engine != "model" || string(shadows[0].Output) == "" {
		t.Fatalf("shadow record incomplete: %+v", shadows[0])
	}
}

func TestShadowFailuresNeverAffectTheDecision(t *testing.T) {
	rec := &memRecorder{}
	failing := Func[classIn, dist]{Name: "broken", Fn: func(context.Context, classIn) (dist, error) {
		return nil, errors.New("model unavailable")
	}}
	panicking := Func[classIn, dist]{Name: "crashy", Fn: func(context.Context, classIn) (dist, error) {
		panic("boom")
	}}
	s, _ := New("brain.classify", fixed("rules", dist{{"oom", 1}}), []Engine[classIn, dist]{failing, panicking}, rec)
	if _, err := s.Decide(context.Background(), classIn{}); err != nil {
		t.Fatalf("shadow failure leaked into the decision: %v", err)
	}
	s.Wait()
	for _, r := range rec.byRole(RoleShadow) {
		if r.Error == "" {
			t.Errorf("shadow %s failed but its record has no error", r.Engine)
		}
	}
}

func TestContractViolationIsRefused(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("brain.classify", fixed("sloppy", dist{{"oom", 0.3}, {"bad-deploy", 0.3}}), nil, rec)
	_, err := s.Decide(context.Background(), classIn{})
	if !errors.Is(err, ErrContract) {
		t.Fatalf("error = %v, want ErrContract", err)
	}
	if rs := rec.byRole(RoleChampion); len(rs) != 1 || rs[0].Error == "" {
		t.Fatal("the refused decision was not recorded with its error")
	}
}

func TestChampionPanicIsRefusedNotFatal(t *testing.T) {
	s, _ := New("brain.classify", Func[classIn, dist]{Name: "crashy", Fn: func(context.Context, classIn) (dist, error) {
		panic("boom")
	}}, nil, &memRecorder{})
	if _, err := s.Decide(context.Background(), classIn{}); err == nil {
		t.Fatal("a panicking champion produced a decision")
	}
}

func TestUnrecordedDecisionIsRefused(t *testing.T) {
	rec := &memRecorder{fail: errors.New("disk full")}
	s, _ := New("brain.classify", fixed("rules", dist{{"oom", 1}}), nil, rec)
	if _, err := s.Decide(context.Background(), classIn{}); err == nil {
		t.Fatal("Decide returned a decision that could not be recorded")
	}
}

func TestShadowsGetTheirOwnCopyOfTheInput(t *testing.T) {
	type in struct{ Labels map[string]string }
	mutate := func(name string) Engine[in, dist] {
		return Func[in, dist]{Name: name, Fn: func(_ context.Context, v in) (dist, error) {
			for i := range 100 {
				v.Labels[name] = string(rune('a' + i%26))
			}
			return dist{{"x", 1}}, nil
		}}
	}
	s, _ := New("x", mutate("champion"), []Engine[in, dist]{mutate("shadow")}, &memRecorder{})
	// Run with -race: a shared map would be reported as a data race.
	if _, err := s.Decide(context.Background(), in{Labels: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
}

func TestNewRequiresARecorderAndChampion(t *testing.T) {
	if _, err := New[classIn, dist]("x", nil, nil, &memRecorder{}); err == nil {
		t.Error("New accepted a nil champion")
	}
	if _, err := New("x", fixed("rules", dist{{"a", 1}}), nil, nil); err == nil {
		t.Error("New accepted a nil recorder")
	}
}

func TestRegistryBuild(t *testing.T) {
	r := NewRegistry()
	if err := Register(r, "brain.classify", fixed("rules", dist{{"a", 1}})); err != nil {
		t.Fatal(err)
	}
	if err := Register(r, "brain.classify", fixed("model", dist{{"a", 1}})); err != nil {
		t.Fatal(err)
	}
	if err := Register(r, "brain.classify", fixed("rules", dist{{"a", 1}})); err == nil {
		t.Error("duplicate engine ID accepted")
	}
	wrongType := Func[string, float64]{Name: "scalar", Fn: func(context.Context, string) (float64, error) { return 0, nil }}
	if err := Register(r, "brain.classify", wrongType); err != nil {
		t.Fatal(err)
	}

	rec := &memRecorder{}
	if _, err := Build[classIn, dist](r, "brain.classify", Config{Champion: "rules", Shadows: []string{"model"}}, rec); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]Config{
		"no champion":       {},
		"unknown engine":    {Champion: "missing"},
		"champion shadowed": {Champion: "rules", Shadows: []string{"rules"}},
		"wrong types":       {Champion: "scalar"},
		"unknown shadow":    {Champion: "rules", Shadows: []string{"missing"}},
	}
	for name, cfg := range bad {
		if _, err := Build[classIn, dist](r, "brain.classify", cfg, rec); err == nil {
			t.Errorf("%s: Build succeeded", name)
		}
	}
	if got := r.Engines("brain.classify"); len(got) != 3 || got[0] != "model" {
		t.Errorf("Engines = %v", got)
	}
}

func TestReplayAgainstTheLedger(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	v1 := Func[classIn, dist]{Name: "v1", Fn: func(_ context.Context, in classIn) (dist, error) {
		if in.Restarts > 3 {
			return dist{{"crashloop", 1}}, nil
		}
		return dist{{"healthy", 1}}, nil
	}}
	s, _ := New("brain.classify", v1, nil, LedgerRecorder{Ledger: l, Source: "/test"})
	for _, n := range []int{0, 5, 10} {
		if _, err := s.Decide(ctx, classIn{Restarts: n}); err != nil {
			t.Fatal(err)
		}
	}

	records, err := Records(ctx, l, "brain.classify")
	if err != nil || len(records) != 3 {
		t.Fatalf("Records = %d, %v", len(records), err)
	}
	for _, c := range Replay(ctx, v1, records) {
		if !c.Match {
			t.Errorf("replaying the same engine did not match: %+v", c)
		}
	}
	stricter := Func[classIn, dist]{Name: "v2", Fn: func(_ context.Context, in classIn) (dist, error) {
		if in.Restarts > 7 {
			return dist{{"crashloop", 1}}, nil
		}
		return dist{{"healthy", 1}}, nil
	}}
	mismatches := 0
	for _, c := range Replay(ctx, stricter, records) {
		if !c.Match {
			mismatches++
		}
	}
	if mismatches != 1 {
		t.Fatalf("v2 disagreed with v1 on %d records, want 1", mismatches)
	}
}
