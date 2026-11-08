package logs

import (
	"fmt"
	"testing"
)

func TestMinerGroupsMessagesThatDifferInValues(t *testing.T) {
	m := NewMiner()
	for _, msg := range []string{
		"connected to 10.0.0.1:6379 in 12ms",
		"connected to 10.0.0.7:6379 in 3ms",
		"user alice logged in",
		"user bob logged in",
		"user carol logged in",
		"cache miss for key alice",
		"payment failed: card declined",
	} {
		m.Add(Mask(msg))
	}
	want := map[string]int{
		"connected to <ip> in <duration>": 2,
		"user <*> logged in":              3,
		"cache miss for key alice":        1,
		"payment failed: card declined":   1,
	}
	got := map[string]int{}
	for _, tpl := range m.Templates() {
		got[tpl.String()] = tpl.Count
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("templates = %v\nwant %v", got, want)
	}
}

func TestTemplateIDsAreStableAsTemplatesGeneralize(t *testing.T) {
	m := NewMiner()
	first := m.Add("job alpha finished")
	second := m.Add("job beta finished")
	if first != second || first.ID != 1 || first.String() != "job <*> finished" {
		t.Fatalf("first %+v, second %+v", first, second)
	}
}

func TestMinerMemoryIsBounded(t *testing.T) {
	m := NewMiner()
	m.MaxTemplates = 10
	for i := range 100 {
		m.Add(fmt.Sprintf("unique%c%c message", 'a'+i%26, 'a'+i/26))
	}
	if n := len(m.Templates()); n != 10 {
		t.Fatalf("templates = %d, want the bound of 10", n)
	}
	if o := m.Add("another unique message entirely"); o.ID != 0 || o.Count == 0 {
		t.Fatalf("past the bound: %+v, want the overflow template", o)
	}
}

func TestEmptyMessages(t *testing.T) {
	m := NewMiner()
	if a, b := m.Add(""), m.Add("   "); a != b || a.Count != 2 {
		t.Fatalf("empty messages: %+v, %+v", a, b)
	}
}
