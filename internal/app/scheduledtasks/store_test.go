package scheduledtasks

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testTask() Task {
	return Task{Owner: "gateway/chat/user", GatewayID: "gateway", SurfaceID: "surface", ChatID: "chat", ActorUserID: "user", Name: "check", Prompt: "Read the device status", Workspace: "/workspace", ThreadID: "thread-1", Mode: ModeExisting, Kind: KindInterval, TimeZone: "Asia/Shanghai", IntervalSeconds: 60, Enabled: true}
}
func testNow() time.Time { return time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC) }
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "schedules.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func createTestTask(t *testing.T, s *Store, task Task) Task {
	t.Helper()
	v, err := s.Create(task, testNow())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func reserveOne(t *testing.T, s *Store, at time.Time) Run {
	t.Helper()
	r, err := s.ReserveDue(at, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 1 {
		t.Fatalf("wanted one run, got %d", len(r))
	}
	return r[0]
}

func TestStorePersistsTaskAndPendingRunAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.sqlite")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	task := createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(time.Minute))
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(task.Owner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != task.ID || !got.NextRunAt.Equal(testNow().Add(2*time.Minute)) {
		t.Fatalf("unexpected stored task: %+v", got)
	}
	runs, err := s.PendingRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != r.ID || runs[0].Task.Prompt != task.Prompt {
		t.Fatalf("pending snapshot lost: %+v", runs)
	}
}

func TestOwnerIsolationAndImmutableAddress(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	if _, err := s.Get("another", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get another owner: %v", err)
	}
	if _, err := s.Update("another", task.ID, task, testNow()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update another owner: %v", err)
	}
	if err := s.Delete("another", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete another owner: %v", err)
	}
	list, err := s.List("another")
	if err != nil || len(list) != 0 {
		t.Fatalf("list leaked %+v %v", list, err)
	}
	r := reserveOne(t, s, testNow().Add(time.Minute))
	r.Owner = "another"
	if err = s.SaveRun(r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SaveRun another owner: %v", err)
	}
	task.ID = "fake"
	task.Owner = "another"
	task.GatewayID = "other-gateway"
	task.SurfaceID = "other-surface"
	task.ChatID = "other-chat"
	task.ActorUserID = "other-actor"
	task.Prompt = "updated"
	got, err := s.Update("gateway/chat/user", r.TaskID, task, testNow().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "gateway/chat/user" || got.ID != r.TaskID || got.GatewayID != "gateway" || got.ChatID != "chat" || got.SurfaceID != "surface" || got.ActorUserID != "user" {
		t.Fatalf("address mutated: %+v", got)
	}
}

func TestReserveMergesMissedRoundsAndDoesNotOverlap(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(10*time.Minute+20*time.Second))
	if !r.ScheduledAt.Equal(task.NextRunAt) {
		t.Fatal("lost scheduled time")
	}
	got, err := s.Get(task.Owner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRunAt.Equal(testNow().Add(11 * time.Minute)) {
		t.Fatalf("missed rounds not merged: %s", got.NextRunAt)
	}
	for _, status := range []string{StatusPending, StatusRunning} {
		r.Status = status
		r.InstanceID = "instance"
		r.CommandID = "command"
		r.QueueItemID = "queue"
		if err = s.SaveRun(r); err != nil {
			t.Fatal(err)
		}
		runs, err := s.ReserveDue(testNow().Add(30*time.Minute), 10)
		if err != nil || len(runs) != 0 {
			t.Fatalf("overlap in %s: %+v %v", status, runs, err)
		}
	}
	r.Status = StatusSucceeded
	r.CompletedAt = testNow().Add(30 * time.Minute)
	if err = s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	_ = reserveOne(t, s, testNow().Add(30*time.Minute))
	got, err = s.Get(task.Owner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRunAt.Equal(testNow().Add(31 * time.Minute)) {
		t.Fatalf("catchup should merge again: %s", got.NextRunAt)
	}
}

func TestRunSnapshotSurvivesTaskUpdateAndSave(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(time.Minute))
	task.Prompt = "new prompt"
	task.ThreadID = "different-thread"
	task.IntervalSeconds = 120
	updated, err := s.Update(task.Owner, task.ID, task, testNow().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.NextRunAt.Equal(testNow().Add(3 * time.Minute)) {
		t.Fatalf("update did not recompute next time")
	}
	r.Task = updated
	r.ScheduledAt = testNow().Add(time.Hour)
	r.Status = StatusRunning
	r.InstanceID = "i"
	r.CommandID = "c"
	r.QueueItemID = "q"
	r.ThreadID = "runtime-thread"
	if err = s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.Prompt != "Read the device status" || got.Task.ThreadID != "thread-1" || !got.ScheduledAt.Equal(testNow().Add(time.Minute)) || got.QueueItemID != "q" {
		t.Fatalf("run snapshot not immutable: %+v", got)
	}
}

func TestRecoverKeepsPendingAndDoesNotReplayUnknownRunning(t *testing.T) {
	s := testStore(t)
	createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(time.Minute))
	r.Status = StatusRunning
	r.StartedAt = testNow().Add(time.Minute)
	r.InstanceID = "i"
	r.CommandID = "c"
	r.QueueItemID = "q"
	r.TurnID = "turn"
	if err := s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	second := testTask()
	second.Name = "second"
	second.Kind = KindOnce
	second.At = testNow().Add(time.Minute)
	second = createTestTask(t, s, second)
	pending := reserveOne(t, s, testNow().Add(time.Minute))
	if err := s.Recover(testNow().Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusInterrupted || got.Error == "" || got.TurnID != "turn" || got.Notified || got.CompletedAt.IsZero() {
		t.Fatalf("running did not recover: %+v", got)
	}
	runs, err := s.PendingRuns()
	if err != nil || len(runs) != 1 || runs[0].ID != pending.ID || runs[0].TaskID != second.ID {
		t.Fatalf("pending replay incorrect: %+v %v", runs, err)
	}
	notices, err := s.UnnotifiedRuns()
	if err != nil || len(notices) != 1 || notices[0].ID != r.ID {
		t.Fatalf("unknown execution should notify: %+v %v", notices, err)
	}
	got.Notified = true
	if err = s.SaveRun(got); err != nil {
		t.Fatal(err)
	}
	notices, err = s.UnnotifiedRuns()
	if err != nil || len(notices) != 0 {
		t.Fatalf("notification marker lost: %+v %v", notices, err)
	}
	got.Status = StatusPending
	if err = s.SaveRun(got); err == nil {
		t.Fatal("replayed terminal run")
	}
}

func TestDeleteCancelsOnlyPendingAndKeepsRunning(t *testing.T) {
	s := testStore(t)
	a := createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(time.Minute))
	if err := s.Delete(a.Owner, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(a.Owner, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted task visible: %v", err)
	}
	got, err := s.GetRun(r.ID)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("pending not cancelled: %+v %v", got, err)
	}
	b := createTestTask(t, s, testTask())
	running := reserveOne(t, s, testNow().Add(time.Minute))
	running.Status = StatusRunning
	running.InstanceID = "i"
	running.CommandID = "c"
	running.QueueItemID = "q"
	if err = s.SaveRun(running); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(b.Owner, b.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRun(running.ID)
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("delete changed running: %+v %v", got, err)
	}
	if due, err := s.ReserveDue(testNow().Add(time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatalf("deleted job reserved: %+v %v", due, err)
	}
}

func TestConcurrentReservationsProduceOneRun(t *testing.T) {
	s := testStore(t)
	createTestTask(t, s, testTask())
	var wg sync.WaitGroup
	results := make(chan []Run, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ReserveDue(testNow().Add(time.Minute), 10)
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for runs := range results {
		count += len(runs)
	}
	if count != 1 {
		t.Fatalf("duplicate concurrent reservations: %d", count)
	}
}

func TestFailedReservationDoesNotAdvanceNextDue(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	if _, err := s.db.Exec(`CREATE TRIGGER reject_run BEFORE INSERT ON schedule_runs BEGIN SELECT RAISE(ABORT,'disk write simulation'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDue(testNow().Add(time.Minute), 10); err == nil {
		t.Fatal("expected reservation failure")
	}
	got, err := s.Get(task.Owner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRunAt.Equal(task.NextRunAt) {
		t.Fatal("failed reservation advanced next due")
	}
	if pending, err := s.PendingRuns(); err != nil || len(pending) != 0 {
		t.Fatalf("failed transaction retained run: %+v %v", pending, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_run`); err != nil {
		t.Fatal(err)
	}
	_ = reserveOne(t, s, testNow().Add(time.Minute))
}

func TestDailyTimeZoneAndOnce(t *testing.T) {
	s := testStore(t)
	task := testTask()
	task.Kind = KindDaily
	task.DailyTime = "10:00"
	got := createTestTask(t, s, task)
	if !got.NextRunAt.Equal(testNow().Add(24 * time.Hour)) {
		t.Fatalf("equal daily time must mean next local day: %s", got.NextRunAt)
	}
	zoneTask := testTask()
	zoneTask.Kind = KindDaily
	zoneTask.TimeZone = "America/New_York"
	zoneTask.DailyTime = "09:00"
	created, err := s.Create(zoneTask, time.Date(2026, 3, 7, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !created.NextRunAt.Equal(time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("DST local schedule wrong: %s", created.NextRunAt)
	}
	zoneTask.DailyTime = "02:30"
	created, err = s.Create(zoneTask, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !created.NextRunAt.Equal(time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("nonexistent DST time should skip its date: %s", created.NextRunAt)
	}
	s = testStore(t)
	once := testTask()
	once.Kind = KindOnce
	once.At = testNow().Add(time.Minute)
	once = createTestTask(t, s, once)
	r := reserveOne(t, s, testNow().Add(time.Minute))
	if r.TaskID != once.ID {
		t.Fatal("wrong once run")
	}
	got, err = s.Get(once.Owner, once.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || !got.NextRunAt.IsZero() {
		t.Fatal("once task not disabled")
	}
	r.Status = StatusSucceeded
	if err = s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ReserveDue(testNow().Add(time.Hour), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("once replayed: %+v %v", runs, err)
	}
}

func TestValidationRejectsInvalidOrUnsafeTask(t *testing.T) {
	now := testNow()
	checks := []func(*Task){
		func(t *Task) { t.Owner = "" }, func(t *Task) { t.GatewayID = "" }, func(t *Task) { t.SurfaceID = "" }, func(t *Task) { t.ChatID = "" }, func(t *Task) { t.ActorUserID = "" }, func(t *Task) { t.Prompt = "  " }, func(t *Task) { t.Name = "" }, func(t *Task) { t.Workspace = "" }, func(t *Task) { t.ThreadID = "" }, func(t *Task) { t.IntervalSeconds = 9 }, func(t *Task) { t.Mode = "other" }, func(t *Task) { t.TimeZone = "Local" }, func(t *Task) { t.TimeZone = "bad/zone" }, func(t *Task) { t.Kind = "other" }, func(t *Task) { t.Kind = KindDaily; t.DailyTime = "9:00" }, func(t *Task) { t.Kind = KindDaily; t.DailyTime = "24:00" }, func(t *Task) { t.Kind = KindOnce; t.At = now }, func(t *Task) { t.Mode = ModeNewEachRun },
	}
	for i, change := range checks {
		task := testTask()
		change(&task)
		if err := Validate(task, now); err == nil {
			t.Fatalf("invalid variant %d accepted", i)
		}
	}
	task := testTask()
	task.Mode = ModeNewEachRun
	task.ThreadID = ""
	if err := Validate(task, now); err != nil {
		t.Fatalf("valid new_each_run rejected: %v", err)
	}
}

func TestSetEnabledCancelsPendingButKeepsRunningAndAllowsPastOncePause(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	pending := reserveOne(t, s, testNow().Add(time.Minute))
	paused, err := s.SetEnabled(task.Owner, task.ID, false, testNow().Add(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if paused.Enabled {
		t.Fatal("pause did not disable")
	}
	got, err := s.GetRun(pending.ID)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("pending not cancelled: %+v %v", got, err)
	}
	resumed, err := s.SetEnabled(task.Owner, task.ID, true, testNow().Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.NextRunAt.Equal(testNow().Add(3 * time.Minute)) {
		t.Fatal("resume did not recompute next due")
	}
	r := reserveOne(t, s, resumed.NextRunAt)
	r.Status = StatusRunning
	r.InstanceID = "i"
	r.CommandID = "c"
	r.QueueItemID = "q"
	if err = s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(task.Owner, task.ID, false, testNow().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRun(r.ID)
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("pause interrupted running: %+v %v", got, err)
	}
	once := testTask()
	once.Kind = KindOnce
	once.At = testNow().Add(time.Minute)
	once = createTestTask(t, s, once)
	_ = reserveOne(t, s, once.At)
	if _, err = s.SetEnabled(once.Owner, once.ID, false, testNow().Add(time.Hour)); err != nil {
		t.Fatal("past once pause must remain permitted", err)
	}
	if _, err = s.SetEnabled(once.Owner, once.ID, true, testNow().Add(time.Hour)); err == nil {
		t.Fatal("past once re-enabled")
	}
}

func TestOutstandingAndOwnerScopedHistory(t *testing.T) {
	s := testStore(t)
	task := createTestTask(t, s, testTask())
	r := reserveOne(t, s, testNow().Add(time.Minute))
	for _, status := range []string{StatusPending, StatusRunning} {
		r.Status = status
		if err := s.SaveRun(r); err != nil {
			t.Fatal(err)
		}
		runs, err := s.OutstandingRuns()
		if err != nil || len(runs) != 1 || runs[0].Status != status {
			t.Fatalf("missing %s run: %+v %v", status, runs, err)
		}
	}
	r.Status = StatusSucceeded
	if err := s.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	latest := reserveOne(t, s, testNow().Add(2*time.Minute))
	history, err := s.RunHistory(task.Owner, task.ID, 1)
	if err != nil || len(history) != 1 || history[0].ID != latest.ID {
		t.Fatalf("history not newest first: %+v %v", history, err)
	}
	history, err = s.RunHistory("other", task.ID, 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("history leaked other owner: %+v %v", history, err)
	}
}
