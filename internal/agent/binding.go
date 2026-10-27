package agent

import (
	"context"
	"errors"
	"fmt"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
)

// A backend switch is not a migration. Keep old Undo attached to its original
// implementation, and conservatively reject switching journals with history.
func CheckDataplaneBinding(ctx context.Context, s store.Store, want string, r *backend.Registry) error {
	var old string
	e := s.Load(ctx, "dataplane-binding", &old)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return e
	}
	var running model.Running
	if e = s.Load(ctx, "running", &running); e != nil && !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if old == "" && len(running.Operations) > 0 {
		old = "linux"
	}
	if old != "" && old != want {
		return fmt.Errorf("journal belongs to %s, requested %s; use a new node/state namespace or an explicit migration", old, want)
	}
	keys, e := s.Keys(ctx, "task/")
	if e != nil {
		return e
	}
	for _, key := range keys {
		var rec model.Record
		if e = s.Load(ctx, key, &rec); e != nil {
			return e
		}
		if rec.Task == nil {
			return fmt.Errorf("invalid saved task %s", key)
		}
		if model.Terminal(rec.Task.State) && rec.Task.State != "ROLLBACK_FAILED" {
			continue
		}
		for _, step := range rec.Plan {
			if r.Driver(step.Backend) == nil {
				return fmt.Errorf("task %s needs disabled backend %s", rec.Task.Id, step.Backend)
			}
		}
	}
	return s.Atomic(ctx, map[string]any{"dataplane-binding": want})
}
