// Package slot implements Sentinel's swappable decision points.
//
// A slot is one typed decision, such as "which fault class is this incident"
// or "which object is the root cause". One champion engine fills the slot and
// its output is used. Challenger engines run in shadow on a copy of the same
// input: their outputs are recorded next to the champion's for comparison and
// replay, and are never acted on. Every call is recorded; a decision that
// cannot be recorded is refused.
package slot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Engine fills a slot.
type Engine[In, Out any] interface {
	ID() string
	Decide(ctx context.Context, in In) (Out, error)
}

// Func adapts a function into an Engine.
type Func[In, Out any] struct {
	Name string
	Fn   func(context.Context, In) (Out, error)
}

func (f Func[In, Out]) ID() string { return f.Name }

func (f Func[In, Out]) Decide(ctx context.Context, in In) (Out, error) { return f.Fn(ctx, in) }

// Validator is implemented by outputs that can check their own contract.
type Validator interface {
	Validate() error
}

// ErrContract wraps an output that violates its slot's contract.
var ErrContract = errors.New("engine output violates the slot contract")

// Role says whether a recorded call was used or ran in shadow.
type Role string

const (
	RoleChampion Role = "champion"
	RoleShadow   Role = "shadow"
)

// Record is the trace of one engine call.
type Record struct {
	Slot string `json:"slot"`
	// Call is shared by the champion and shadow records of one decision.
	Call        string          `json:"call"`
	Engine      string          `json:"engine"`
	Role        Role            `json:"role"`
	InputSHA256 string          `json:"inputSha256"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output,omitempty"`
	Error       string          `json:"error,omitempty"`
	LatencyNS   int64           `json:"latencyNs"`
	At          time.Time       `json:"at"`
}

// Recorder stores records, normally in the Ledger.
type Recorder interface {
	Record(ctx context.Context, r Record) error
}

// Slot is a decision point with one champion and any number of shadows.
type Slot[In, Out any] struct {
	name          string
	champion      Engine[In, Out]
	shadows       []Engine[In, Out]
	rec           Recorder
	shadowTimeout time.Duration
	now           func() time.Time
	wg            sync.WaitGroup
}

// Option configures a Slot.
type Option func(*options)

type options struct {
	shadowTimeout time.Duration
	now           func() time.Time
}

// WithShadowTimeout bounds how long a shadow engine may run.
func WithShadowTimeout(d time.Duration) Option {
	return func(o *options) { o.shadowTimeout = d }
}

// WithClock replaces the clock used for timestamps and latency.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// New builds a slot. A recorder is mandatory.
func New[In, Out any](name string, champion Engine[In, Out], shadows []Engine[In, Out], rec Recorder, opts ...Option) (*Slot[In, Out], error) {
	if name == "" {
		return nil, errors.New("slot name is empty")
	}
	if champion == nil {
		return nil, fmt.Errorf("slot %s has no champion engine", name)
	}
	if rec == nil {
		return nil, fmt.Errorf("slot %s has no recorder; unrecorded decisions are not allowed", name)
	}
	o := options{shadowTimeout: 30 * time.Second, now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return &Slot[In, Out]{
		name: name, champion: champion, shadows: shadows, rec: rec,
		shadowTimeout: o.shadowTimeout, now: o.now,
	}, nil
}

// Name returns the slot's name.
func (s *Slot[In, Out]) Name() string { return s.name }

// Champion returns the ID of the engine whose output is used.
func (s *Slot[In, Out]) Champion() string { return s.champion.ID() }

// Decide runs the champion and returns its output. Shadows run concurrently
// on their own copy of the input and never affect the result. The champion's
// output is returned only after its record is stored.
func (s *Slot[In, Out]) Decide(ctx context.Context, in In) (Out, error) {
	var zero Out
	input, err := json.Marshal(in)
	if err != nil {
		return zero, fmt.Errorf("slot %s: input cannot be recorded: %w", s.name, err)
	}
	sum := sha256.Sum256(input)
	base := Record{Slot: s.name, Call: string(model.NewID()), InputSHA256: hex.EncodeToString(sum[:]), Input: input}

	for _, sh := range s.shadows {
		s.wg.Go(func() { s.runShadow(context.WithoutCancel(ctx), sh, base) })
	}

	start := s.now()
	out, err := call(ctx, s.champion, in)
	rec := s.finish(base, s.champion.ID(), RoleChampion, start, out, err)
	if recErr := s.rec.Record(ctx, rec); recErr != nil {
		return zero, fmt.Errorf("slot %s: decision not recorded, refusing to use it: %w", s.name, recErr)
	}
	if err != nil {
		return zero, fmt.Errorf("slot %s engine %s: %w", s.name, s.champion.ID(), err)
	}
	return out, nil
}

func (s *Slot[In, Out]) runShadow(ctx context.Context, e Engine[In, Out], base Record) {
	ctx, cancel := context.WithTimeout(ctx, s.shadowTimeout)
	defer cancel()
	start := s.now()
	var out Out
	var in In
	err := json.Unmarshal(base.Input, &in)
	if err == nil {
		out, err = call(ctx, e, in)
	}
	// A shadow that cannot be recorded is simply lost; it must never fail the decision.
	_ = s.rec.Record(ctx, s.finish(base, e.ID(), RoleShadow, start, out, err))
}

// call runs an engine, turning a panic into an error and checking the
// output's contract.
func call[In, Out any](ctx context.Context, e Engine[In, Out], in In) (out Out, err error) {
	defer func() {
		if p := recover(); p != nil {
			var zero Out
			out, err = zero, fmt.Errorf("engine %s panicked: %v", e.ID(), p)
		}
	}()
	out, err = e.Decide(ctx, in)
	if err != nil {
		return out, err
	}
	if v, ok := any(out).(Validator); ok {
		if verr := v.Validate(); verr != nil {
			return out, fmt.Errorf("%w: %w", ErrContract, verr)
		}
	}
	return out, nil
}

func (s *Slot[In, Out]) finish(base Record, engine string, role Role, start time.Time, out Out, err error) Record {
	r := base
	r.Engine, r.Role, r.At = engine, role, start
	r.LatencyNS = s.now().Sub(start).Nanoseconds()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if b, mErr := json.Marshal(out); mErr == nil {
		r.Output = b
	} else {
		r.Error = "output cannot be recorded: " + mErr.Error()
	}
	return r
}

// Wait blocks until every shadow started so far has finished.
func (s *Slot[In, Out]) Wait() { s.wg.Wait() }
