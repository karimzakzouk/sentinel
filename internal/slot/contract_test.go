package slot

import (
	"math"
	"testing"
)

func TestDistributionValidate(t *testing.T) {
	valid := Distribution[string]{{"oom", 0.7}, {"bad-deploy", 0.2}, {"node", 0.1}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid distribution rejected: %v", err)
	}
	if top := valid.Top(); top.Value != "oom" {
		t.Fatalf("Top = %v", top)
	}
	if p := valid.P("node"); p != 0.1 {
		t.Fatalf("P(node) = %v", p)
	}
	invalid := map[string]Distribution[string]{
		"empty":     {},
		"negative":  {{"a", -0.1}, {"b", 1.1}},
		"nan":       {{"a", math.NaN()}},
		"short":     {{"a", 0.5}},
		"duplicate": {{"a", 0.5}, {"a", 0.5}},
	}
	for name, d := range invalid {
		if d.Validate() == nil {
			t.Errorf("%s distribution accepted", name)
		}
	}
}

func TestRankingValidate(t *testing.T) {
	r := Ranking[string]{Coverage: 0.9, Items: []Ranked[string]{
		{"cart", 3, true}, {"redis", 2, true}, {"frontend", 1, false},
	}}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid ranking rejected: %v", err)
	}
	if set := r.Set(); len(set) != 2 || set[1] != "redis" {
		t.Fatalf("Set = %v", set)
	}
	invalid := map[string]Ranking[string]{
		"unsorted":   {Coverage: 0.9, Items: []Ranked[string]{{"a", 1, true}, {"b", 2, false}}},
		"gap in set": {Coverage: 0.9, Items: []Ranked[string]{{"a", 2, true}, {"b", 1, false}, {"c", 0, true}}},
		"empty set":  {Coverage: 0.9, Items: []Ranked[string]{{"a", 2, false}}},
		"coverage":   {Coverage: 0, Items: []Ranked[string]{{"a", 2, true}}},
		"infinite":   {Coverage: 0.9, Items: []Ranked[string]{{"a", math.Inf(1), true}}},
	}
	for name, rk := range invalid {
		if rk.Validate() == nil {
			t.Errorf("%s ranking accepted", name)
		}
	}
	if (Ranking[string]{}).Validate() != nil {
		t.Error("an empty ranking should be valid: there may be no candidates")
	}
}

func TestEstimateValidate(t *testing.T) {
	if err := (Estimate{Value: 0.2, Lower: 0.1, Upper: 0.3, Level: 0.95}).Validate(); err != nil {
		t.Fatalf("valid estimate rejected: %v", err)
	}
	invalid := []Estimate{
		{Value: 0.5, Lower: 0.1, Upper: 0.3, Level: 0.95},
		{Value: 0.2, Lower: 0.1, Upper: 0.3, Level: 1},
		{Value: math.NaN(), Lower: 0.1, Upper: 0.3, Level: 0.95},
	}
	for _, e := range invalid {
		if e.Validate() == nil {
			t.Errorf("invalid estimate accepted: %+v", e)
		}
	}
}
