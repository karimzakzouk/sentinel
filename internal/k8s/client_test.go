package k8s

import (
	"context"
	"testing"
)

// TestPatchCRStatusSkipFakeNames verifies that PatchCRStatus silently
// returns nil for fake CR names from auto/manual/slack triggers.
func TestPatchCRStatusSkipFakeNames(t *testing.T) {
	client := &Client{}
	for _, name := range []string{"", "auto-detected", "manual", "slack"} {
		if err := client.PatchCRStatus(context.TODO(), name, "default", "resolved"); err != nil {
			t.Errorf("PatchCRStatus(%q) = %v, want nil", name, err)
		}
	}
}

func TestParseStackTrace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "python trace",
			in: `Traceback (most recent call last):
  File "app/main.py", line 11, in get_config
    return os.environ["DATABASE_URL"]
KeyError: 'DATABASE_URL'`,
			want: []string{"app/main.py"},
		},
		{
			name: "go panic",
			in: `panic: runtime error: index out of range [5] with length 3
goroutine 1 [running]:
main.handler(...)
        /src/handler.go:42 +0x123`,
			want: []string{"src/handler.go"},
		},
		{
			name: "stdlib filtered",
			in: `File "/usr/local/lib/python3.11/site-packages/flask/app.py", line 500
File "src/main.py", line 10`,
			want: []string{"src/main.py"},
		},
		{
			name: "java class file (path-style only)",
			in:   `at com/acme/foo/Bar.java:42`,
			want: []string{"com/acme/foo/Bar.java"},
		},
		{
			name: "empty",
			in:   "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseStackTrace(tc.in)
			if len(got) != len(tc.want) {
				t.Errorf("ParseStackTrace(%q) = %v, want %v", tc.in, got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ParseStackTrace(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIsValidLogContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   \n  ", false},
		{"containerd error only", "unable to retrieve container logs for containerd://abc\n", false},
		{"real log", "Traceback...\nKeyError: 'X'\n", true},
		{"mixed", "unable to retrieve container logs for containerd://x\nreal log line\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isValidLogContent(c.in); got != c.want {
				t.Errorf("isValidLogContent(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]string{"b": "1", "a": "2", "c": "3"}
	got := SortedKeys(m)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("SortedKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHasAssetExt(t *testing.T) {
	if !hasAssetExt("config.yaml") {
		t.Error("config.yaml should be asset")
	}
	if hasAssetExt("main.go") {
		t.Error("main.go should not be asset")
	}
}

func TestIsStdlibPath(t *testing.T) {
	if !isStdlibPath("/usr/local/lib/python3.11/site-packages/flask/app.py") {
		t.Error("site-packages path should be stdlib")
	}
	if isStdlibPath("src/main.py") {
		t.Error("src/main.py should not be stdlib")
	}
}
