package scheduledtasks

import "encoding/json"

// OutstandingRuns is a daemon-only reconciliation view, not a public user API.
func (s *Store) OutstandingRuns() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryRuns(s.db, `WHERE status IN ('pending','running')`)
}

// RunHistory returns recent evidence from one user's task, newest first.
func (s *Store) RunHistory(owner, taskID string, limit int) ([]Run, error) {
	if limit <= 0 {
		return []Run{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT payload FROM schedule_runs WHERE owner=? AND task_id=? ORDER BY scheduled_at DESC,id DESC LIMIT ?`, owner, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		var raw []byte
		var r Run
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}
