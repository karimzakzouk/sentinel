package slot

import (
	"fmt"
	"slices"
	"sort"
	"sync"
)

// Registry holds every engine available for every slot.
type Registry struct {
	mu      sync.RWMutex
	engines map[string]map[string]any
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{engines: map[string]map[string]any{}}
}

// Register adds an engine to a slot. Engine IDs are unique per slot.
func Register[In, Out any](r *Registry, slotName string, e Engine[In, Out]) error {
	if slotName == "" || e == nil || e.ID() == "" {
		return fmt.Errorf("register: slot name, engine and engine ID are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.engines[slotName] == nil {
		r.engines[slotName] = map[string]any{}
	}
	if _, dup := r.engines[slotName][e.ID()]; dup {
		return fmt.Errorf("slot %s already has an engine %q", slotName, e.ID())
	}
	r.engines[slotName][e.ID()] = e
	return nil
}

// Engines lists the engine IDs registered for a slot, sorted.
func (r *Registry) Engines(slotName string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.engines[slotName]))
	for id := range r.engines[slotName] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Config selects the engines for one slot.
type Config struct {
	Champion string   `json:"champion"`
	Shadows  []string `json:"shadows,omitempty"`
}

// Build assembles a slot from the registry. It refuses to guess: the champion
// must be named, and every named engine must exist with the slot's types.
func Build[In, Out any](r *Registry, slotName string, cfg Config, rec Recorder, opts ...Option) (*Slot[In, Out], error) {
	if cfg.Champion == "" {
		return nil, fmt.Errorf("slot %s: no champion configured (available: %v)", slotName, r.Engines(slotName))
	}
	if slices.Contains(cfg.Shadows, cfg.Champion) {
		return nil, fmt.Errorf("slot %s: engine %q cannot be both champion and shadow", slotName, cfg.Champion)
	}
	champion, err := lookup[In, Out](r, slotName, cfg.Champion)
	if err != nil {
		return nil, err
	}
	shadows := make([]Engine[In, Out], 0, len(cfg.Shadows))
	for _, id := range cfg.Shadows {
		e, err := lookup[In, Out](r, slotName, id)
		if err != nil {
			return nil, err
		}
		shadows = append(shadows, e)
	}
	return New(slotName, champion, shadows, rec, opts...)
}

func lookup[In, Out any](r *Registry, slotName, id string) (Engine[In, Out], error) {
	r.mu.RLock()
	raw, ok := r.engines[slotName][id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("slot %s: unknown engine %q (available: %v)", slotName, id, r.Engines(slotName))
	}
	e, ok := raw.(Engine[In, Out])
	if !ok {
		return nil, fmt.Errorf("slot %s: engine %q has the wrong input or output type", slotName, id)
	}
	return e, nil
}
