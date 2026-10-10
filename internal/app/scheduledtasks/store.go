package scheduledtasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	mu sync.Mutex
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("schedule store path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	for _, q := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON",
		`CREATE TABLE IF NOT EXISTS schedule_tasks (id TEXT PRIMARY KEY, owner TEXT NOT NULL, enabled INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, next_due INTEGER NOT NULL, payload TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS schedule_tasks_due ON schedule_tasks(enabled,deleted,next_due)`,
		`CREATE TABLE IF NOT EXISTS schedule_runs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES schedule_tasks(id), owner TEXT NOT NULL, status TEXT NOT NULL, notified INTEGER NOT NULL, scheduled_at INTEGER NOT NULL, payload TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS schedule_one_active_run ON schedule_runs(task_id) WHERE status IN ('pending','running')`,
		`CREATE INDEX IF NOT EXISTS schedule_runs_outbox ON schedule_runs(notified,status,scheduled_at)`,
		`CREATE INDEX IF NOT EXISTS schedule_runs_history ON schedule_runs(owner,task_id,scheduled_at)`,
	} {
		if _, err = db.Exec(q); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.db.Close() }

func (s *Store) Create(t Task, now time.Time) (Task, error) {
	if err := Validate(t, now); err != nil {
		return Task{}, err
	}
	var err error
	t.ID, err = randomID("task_")
	if err != nil {
		return Task{}, err
	}
	t.CreatedAt = now
	t.UpdatedAt = now
	t.NextRunAt = firstDue(t, now)
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	_, err = s.db.Exec(`INSERT INTO schedule_tasks(id,owner,enabled,next_due,payload) VALUES(?,?,?,?,?)`, t.ID, t.Owner, t.Enabled, nanos(t.NextRunAt), raw)
	return t, err
}

func (s *Store) Get(owner, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getTask(s.db, owner, id)
}

type queryRower interface{ QueryRow(string, ...any) *sql.Row }

func getTask(q queryRower, owner, id string) (Task, error) {
	var raw []byte
	if err := q.QueryRow(`SELECT payload FROM schedule_tasks WHERE owner=? AND id=? AND deleted=0`, owner, id).Scan(&raw); err != nil {
		return Task{}, notFound(err)
	}
	var t Task
	err := json.Unmarshal(raw, &t)
	return t, err
}

func (s *Store) List(owner string) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT payload FROM schedule_tasks WHERE owner=? AND deleted=0 ORDER BY id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Task{}
	for rows.Next() {
		var raw []byte
		var t Task
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

func (s *Store) Update(owner, id string, t Task, now time.Time) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	old, err := getTask(tx, owner, id)
	if err != nil {
		return Task{}, err
	}
	t.ID = old.ID
	t.Owner = old.Owner
	t.GatewayID = old.GatewayID
	t.SurfaceID = old.SurfaceID
	t.ChatID = old.ChatID
	t.ActorUserID = old.ActorUserID
	t.CreatedAt = old.CreatedAt
	if err = Validate(t, now); err != nil {
		return Task{}, err
	}
	t.UpdatedAt = now
	t.NextRunAt = firstDue(t, now)
	if err = writeTask(tx, t, false); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) Delete(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := getTask(tx, owner, id)
	if err != nil {
		return err
	}
	t.Enabled = false
	t.UpdatedAt = time.Now()
	t.NextRunAt = time.Time{}
	if err = writeTask(tx, t, true); err != nil {
		return err
	}
	runs, err := queryRuns(tx, `WHERE owner=? AND task_id=? AND status='pending'`, owner, id)
	if err != nil {
		return err
	}
	for _, r := range runs {
		r.Status = StatusCancelled
		r.CompletedAt = t.UpdatedAt
		r.Error = "task deleted before dispatch"
		r.Notified = false
		if err = writeRun(tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func writeTask(tx *sql.Tx, t Task, deleted bool) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE schedule_tasks SET enabled=?,deleted=?,next_due=?,payload=? WHERE id=? AND owner=?`, t.Enabled, deleted, nanos(t.NextRunAt), raw, t.ID, t.Owner)
	return affected(res, err)
}

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) ReserveDue(now time.Time, limit int) ([]Run, error) {
	if limit <= 0 {
		return []Run{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT t.payload FROM schedule_tasks t WHERE t.enabled=1 AND t.deleted=0 AND t.next_due>0 AND t.next_due<=? AND NOT EXISTS(SELECT 1 FROM schedule_runs r WHERE r.task_id=t.id AND r.status IN ('pending','running')) ORDER BY t.next_due,t.id LIMIT ?`, nanos(now), limit)
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	for rows.Next() {
		var raw []byte
		var t Task
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	runs := []Run{}
	for _, t := range tasks {
		id, e := randomID("run_")
		if e != nil {
			return nil, e
		}
		r := Run{ID: id, TaskID: t.ID, Owner: t.Owner, Status: StatusPending, ScheduledAt: t.NextRunAt, Task: t}
		raw, e := json.Marshal(r)
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(`INSERT INTO schedule_runs(id,task_id,owner,status,notified,scheduled_at,payload) VALUES(?,?,?,?,?,?,?)`, r.ID, r.TaskID, r.Owner, r.Status, r.Notified, nanos(r.ScheduledAt), raw); e != nil {
			return nil, e
		}
		t.NextRunAt = afterDue(t, now)
		t.UpdatedAt = now
		if t.Kind == KindOnce {
			t.Enabled = false
		}
		if e = writeTask(tx, t, false); e != nil {
			return nil, e
		}
		runs = append(runs, r)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) PendingRuns() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryRuns(s.db, `WHERE status='pending'`)
}
func (s *Store) UnnotifiedRuns() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryRuns(s.db, `WHERE notified=0 AND status IN ('succeeded','failed','interrupted','cancelled')`)
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func queryRuns(q queryer, where string, args ...any) ([]Run, error) {
	rows, err := q.Query(`SELECT payload FROM schedule_runs `+where+` ORDER BY scheduled_at,id`, args...)
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

func (s *Store) GetRun(id string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getRun(s.db, id)
}
func getRun(q queryRower, id string) (Run, error) {
	var raw []byte
	if err := q.QueryRow(`SELECT payload FROM schedule_runs WHERE id=?`, id).Scan(&raw); err != nil {
		return Run{}, notFound(err)
	}
	var r Run
	err := json.Unmarshal(raw, &r)
	return r, err
}

func (s *Store) SaveRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow(`SELECT payload FROM schedule_runs WHERE id=? AND owner=? AND task_id=?`, r.ID, r.Owner, r.TaskID).Scan(&raw); err != nil {
		return notFound(err)
	}
	var old Run
	if err = json.Unmarshal(raw, &old); err != nil {
		return err
	}
	if !terminal(r.Status) && r.Status != StatusPending && r.Status != StatusRunning {
		return fmt.Errorf("invalid run status %q", r.Status)
	}
	if terminal(old.Status) && old.Status != r.Status {
		return errors.New("terminal run cannot change status")
	}
	if old.Status == StatusRunning && r.Status == StatusPending {
		return errors.New("running run cannot become pending")
	}
	r.Task = old.Task
	r.ScheduledAt = old.ScheduledAt
	if err = writeRun(tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

func writeRun(tx *sql.Tx, r Run) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE schedule_runs SET status=?,notified=?,payload=? WHERE id=? AND owner=? AND task_id=?`, r.Status, r.Notified, raw, r.ID, r.Owner, r.TaskID)
	return affected(res, err)
}

func (s *Store) Recover(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	runs, err := queryRuns(tx, `WHERE status='running'`)
	if err != nil {
		return err
	}
	for _, r := range runs {
		r.Status = StatusInterrupted
		r.Error = "daemon restarted: execution outcome unknown; this run will not be automatically replayed"
		r.CompletedAt = now
		r.Notified = false
		if err = writeRun(tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}
