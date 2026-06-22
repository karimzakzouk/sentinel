package llm

import (
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "plain object",
			in:   `{"foo": "bar", "n": 42}`,
			want: map[string]any{"foo": "bar", "n": float64(42)},
		},
		{
			name: "markdown fenced",
			in:   "Here you go:\n```json\n{\"foo\": \"bar\"}\n```\n",
			want: map[string]any{"foo": "bar"},
		},
		{
			name: "prose around",
			in:   "Sure! Here's the JSON:\n{\"foo\": \"bar\"}\nHope this helps!",
			want: map[string]any{"foo": "bar"},
		},
		{
			name: "empty",
			in:   "",
			want: map[string]any{},
		},
		{
			name: "garbage",
			in:   "this is not JSON at all",
			want: map[string]any{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractJSON(c.in)
			if len(got) != len(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
				return
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("key %q: got %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestRepairTruncated(t *testing.T) {
	// Truncated mid-string
	in := `{"hypotheses": [{"statement": "the config is`
	out := repairTruncated(in)
	if !strings.HasSuffix(out, `"}]}`) {
		t.Errorf("repaired output should close string+array+object, got: %q", out)
	}
	if v, ok := tryParse(out); !ok {
		t.Errorf("repaired output should be valid JSON, got: %q", out)
	} else {
		_ = v
	}
}

func TestExtractJSONList(t *testing.T) {
	// LLMs sometimes return a list instead of an object — extractJSON returns {}.
	// The agents handle this by checking the type and extracting the first element.
	in := `[{"statement": "foo"}]`
	got := extractJSON(in)
	// We expect empty dict (not a list-as-map)
	if len(got) != 0 {
		t.Errorf("list input should return empty map, got %v", got)
	}
}
