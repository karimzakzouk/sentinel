package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestRouter_PoolRemapsModelPerProvider is the run #19 regression test.
// The tier methods (FastText/FrontierText/FastJSON/FrontierJSON) pass the
// ROUTER's model name into the pool; every non-primary pool entry must
// serve the call with ITS OWN model name for that tier. Run #19 shipped
// the pool with a groq primary ("openai/gpt-oss-120b") + gemini overflow
// ("gemini-2.5-flash"): every failover sent groq's model name to Google
// and died with "GenerateContentRequest.model: unexpected model name
// format" — the pool was "active" but the overflow was never callable,
// all 6 incidents failed, 0/9 fixes.
func TestRouter_PoolRemapsModelPerProvider(t *testing.T) {
	var mu sync.Mutex
	var gotOverflow, gotPrimary []string

	// Primary: always 400 (non-transient) so the pool fails over.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m, _ := body["model"].(string)
		mu.Lock()
		gotPrimary = append(gotPrimary, m)
		mu.Unlock()
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}))
	defer primary.Close()

	overflow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m, _ := body["model"].(string)
		mu.Lock()
		gotOverflow = append(gotOverflow, m)
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer overflow.Close()

	r := New("http://unused", "key", "primary-fast", "primary-frontier")
	r.SetRetryConfig(0, time.Millisecond)
	r.SetPool(NewPool([]ProviderConfig{
		{ID: "primary", ProviderType: "custom", BaseURL: primary.URL, APIKey: "k1",
			FastModel: "primary-fast", FrontierModel: "primary-frontier", Priority: 1},
		{ID: "overflow", ProviderType: "custom", BaseURL: overflow.URL, APIKey: "k2",
			FastModel: "overflow-fast-model", FrontierModel: "overflow-frontier-model", Priority: 2},
	}))

	if _, err := r.FrontierText(context.Background(), "sys", "user", 50); err != nil {
		t.Fatalf("frontier call should fail over and succeed: %v", err)
	}
	if _, err := r.FastText(context.Background(), "sys", "user", 50); err != nil {
		t.Fatalf("fast call should fail over and succeed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("primary received:  %v", gotPrimary)
	t.Logf("overflow received: %v", gotOverflow)
	if len(gotOverflow) == 0 {
		t.Fatal("pool never reached the overflow provider")
	}
	sawFrontier, sawFast := false, false
	for _, m := range gotOverflow {
		switch m {
		case "overflow-frontier-model":
			sawFrontier = true
		case "overflow-fast-model":
			sawFast = true
		default:
			t.Errorf("overflow provider received %q — per-provider model remap is broken (run #19 bug: router-level model leaked to the overflow)", m)
		}
	}
	if !sawFrontier {
		t.Error("frontier-tier call did not use the overflow's frontier model")
	}
	if !sawFast {
		t.Error("fast-tier call did not use the overflow's fast model")
	}
}

func TestPoolModelForRouterModel(t *testing.T) {
	cases := []struct {
		name                              string
		cfgFast, cfgFrontier              string
		routerFast, routerFrontier, model string
		want                              string
	}{
		{"run #19: groq frontier remaps to gemini overflow",
			"gemini-2.5-flash", "gemini-2.5-flash",
			"openai/gpt-oss-20b", "openai/gpt-oss-120b", "openai/gpt-oss-120b",
			"gemini-2.5-flash"},
		{"run #19: groq fast remaps to gemini overflow",
			"gemini-2.5-flash", "gemini-2.5-flash",
			"openai/gpt-oss-20b", "openai/gpt-oss-120b", "openai/gpt-oss-20b",
			"gemini-2.5-flash"},
		{"fast tier remaps", "gf", "gff", "pf", "pff", "pf", "gf"},
		{"frontier tier remaps", "gf", "gff", "pf", "pff", "pff", "gff"},
		{"entry without per-tier models keeps router model",
			"", "", "pf", "pff", "pff", "pff"},
		{"explicit non-tier model preserved verbatim",
			"gf", "gff", "pf", "pff", "some-explicit-model", "some-explicit-model"},
		{"fast tier, entry only defines frontier", "", "gff", "pf", "pff", "pf", "gff"},
		{"frontier tier, entry only defines fast", "gf", "", "pf", "pff", "pff", "gf"},
		{"ambiguous tier (routerFast == routerFrontier) prefers entry fast",
			"gf", "gff", "same", "same", "same", "gf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &ProviderConfig{FastModel: c.cfgFast, FrontierModel: c.cfgFrontier}
			got := poolModelForRouterModel(cfg, c.routerFast, c.routerFrontier, c.model)
			if got != c.want {
				t.Errorf("poolModelForRouterModel(...) = %q, want %q", got, c.want)
			}
		})
	}
}
