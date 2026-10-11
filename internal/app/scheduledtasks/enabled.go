package scheduledtasks

import "time"

// SetEnabled pauses future dispatch, cancelling reserved but undispatched runs.
// An active run is never interrupted. Resuming starts a new schedule from now.
func (s *Store) SetEnabled(owner, id string, enabled bool, now time.Time) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	t, err := getTask(tx, owner, id)
	if err != nil {
		return Task{}, err
	}
	if enabled {
		if err = Validate(t, now); err != nil {
			return Task{}, err
		}
		t.NextRunAt = firstDue(t, now)
	}
	t.Enabled = enabled
	t.UpdatedAt = now
	if err = writeTask(tx, t, false); err != nil {
		return Task{}, err
	}
	if !enabled {
		runs, err := queryRuns(tx, `WHERE owner=? AND task_id=? AND status='pending'`, owner, id)
		if err != nil {
			return Task{}, err
		}
		for _, r := range runs {
			r.Status = StatusCancelled
			r.CompletedAt = now
			r.Error = "task paused before dispatch"
			r.Notified = false
			if err = writeRun(tx, r); err != nil {
				return Task{}, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}
