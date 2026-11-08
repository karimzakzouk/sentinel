package slot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Comparison is the result of replaying one recorded decision.
type Comparison struct {
	Call     string          `json:"call"`
	Recorded json.RawMessage `json:"recorded,omitempty"`
	Replayed json.RawMessage `json:"replayed,omitempty"`
	Match    bool            `json:"match"`
	Error    string          `json:"error,omitempty"`
}

// Replay re-runs an engine on the inputs of recorded champion decisions and
// compares its outputs with what the champion returned at the time. This is
// how a new engine is measured on real history before it is promoted.
func Replay[In, Out any](ctx context.Context, e Engine[In, Out], records []Record) []Comparison {
	var out []Comparison
	for _, r := range records {
		if r.Role != RoleChampion {
			continue
		}
		c := Comparison{Call: r.Call, Recorded: r.Output}
		var in In
		if err := json.Unmarshal(r.Input, &in); err != nil {
			c.Error = fmt.Sprintf("decode recorded input: %v", err)
			out = append(out, c)
			continue
		}
		res, err := e.Decide(ctx, in)
		if err != nil {
			c.Error = err.Error()
			out = append(out, c)
			continue
		}
		b, err := json.Marshal(res)
		if err != nil {
			c.Error = fmt.Sprintf("encode replayed output: %v", err)
			out = append(out, c)
			continue
		}
		c.Replayed = b
		c.Match = sameJSON(r.Output, b)
		out = append(out, c)
	}
	return out
}

func sameJSON(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
