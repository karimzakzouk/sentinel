package executors

import "testing"

func TestPlanFormatting(t *testing.T) {
	cases := []struct {
		name     string
		files    []string
		markers  map[string]bool
		wantTool string
		wantNil  bool
	}{
		{
			name: "go files always gofmt", files: []string{"cmd/x/main.go"},
			markers: map[string]bool{}, wantTool: "gofmt",
		},
		{
			name: "rust files always cargo fmt", files: []string{"src/lib.rs"},
			markers: map[string]bool{}, wantTool: "cargo-fmt",
		},
		{
			name: "python + black marker", files: []string{"app/x.py"},
			markers: map[string]bool{"black": true}, wantTool: "black",
		},
		{
			name: "python + ruff marker wins over black ordering", files: []string{"app/x.py"},
			markers: map[string]bool{"ruff": true}, wantTool: "ruff",
		},
		{
			name: "python without config -> no formatting", files: []string{"app/x.py"},
			markers: map[string]bool{}, wantNil: true,
		},
		{
			name: "js + prettier marker", files: []string{"src/app.ts"},
			markers: map[string]bool{"prettier": true}, wantTool: "prettier",
		},
		{
			name: "js without prettier -> no formatting", files: []string{"src/app.ts"},
			markers: map[string]bool{}, wantNil: true,
		},
		{
			name: "yaml-only change -> no formatting", files: []string{"k8s/deploy.yaml"},
			markers: map[string]bool{"prettier": true, "black": true}, wantNil: true,
		},
		{
			name: "empty -> nil", files: nil,
			markers: map[string]bool{}, wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanFormatting(tc.files, tc.markers)
			if tc.wantNil {
				if plan != nil {
					t.Fatalf("expected nil plan, got %+v", plan)
				}
				return
			}
			if plan == nil {
				t.Fatalf("expected plan with tool %s, got nil", tc.wantTool)
			}
			if plan.Tool != tc.wantTool {
				t.Fatalf("tool = %s, want %s", plan.Tool, tc.wantTool)
			}
			if plan.Image == "" || plan.Command == "" {
				t.Fatalf("plan missing image/command: %+v", plan)
			}
		})
	}
}

func TestPlanFormattingGoCommandQuotesFiles(t *testing.T) {
	plan := PlanFormatting([]string{"a.go", "b/c.go"}, map[string]bool{})
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.Command != "gofmt -w 'a.go' 'b/c.go'" {
		t.Errorf("unexpected command: %q", plan.Command)
	}
}

func TestFormatterConfigMarkers(t *testing.T) {
	fetcher := &fakeRepoFetcher{files: map[string]string{
		"package.json":   `{"name":"x","prettier":{"semi":false}}`,
		"pyproject.toml": `[tool.black]\nline-length = 88\n`,
		"ruff.toml":      "line-length = 88\n",
	}}
	markers := FormatterConfigMarkers(nil, fetcher, "org/repo", "main")
	if !markers["prettier"] {
		t.Errorf("prettier marker missing: %+v", markers)
	}
	if !markers["black"] {
		t.Errorf("black marker missing: %+v", markers)
	}
	if !markers["ruff"] {
		t.Errorf("ruff marker missing: %+v", markers)
	}

	// A repo with NO formatter config gets NO markers — nothing imposed.
	bare := FormatterConfigMarkers(nil, &fakeRepoFetcher{files: map[string]string{
		"package.json": `{"name":"x","scripts":{"test":"jest"}}`,
	}}, "org/repo", "main")
	if len(bare) != 0 {
		t.Errorf("bare repo should have zero markers, got %+v", bare)
	}
}

func TestShellQuoteAllEscapesQuotes(t *testing.T) {
	// Single quotes in paths are escaped; normal paths pass through.
	got := shellQuoteAll([]string{"a.go", "it's.go"})
	if got != "'a.go' 'it'\\''s.go'" {
		t.Errorf("unexpected quoting: %q", got)
	}
}

func TestHasAnySuffix(t *testing.T) {
	if !hasAnySuffix("x.tsx", ".ts", ".tsx") {
		t.Error("expected .tsx match")
	}
	if hasAnySuffix("x.tsx", ".go") {
		t.Error("unexpected .go match")
	}
}
