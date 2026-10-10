// Package scheduledtasks persists local, chat-owned schedules and immutable run snapshots.
package scheduledtasks

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Schedules keep IANA time zones even on minimal target systems.
)

const (
	ModeExisting      = "existing"
	ModeNewEachRun    = "new_each_run"
	KindInterval      = "interval"
	KindDaily         = "daily"
	KindOnce          = "once"
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
	StatusCancelled   = "cancelled"
)

var ErrNotFound = errors.New("scheduled task or run not found")

type Task struct {
	ID, Owner, GatewayID, SurfaceID, ChatID, ActorUserID string
	Name, Prompt, Workspace, CWD, ThreadID               string
	Mode, Kind, TimeZone, DailyTime                      string
	IntervalSeconds                                      int
	At, NextRunAt, CreatedAt, UpdatedAt                  time.Time
	Enabled                                              bool
}

type Run struct {
	ID, TaskID, Owner, Status                            string
	InstanceID, ThreadID, TurnID, CommandID, QueueItemID string
	Result, Error                                        string
	ScheduledAt, StartedAt, CompletedAt                  time.Time
	Task                                                 Task
	Notified                                             bool
}

func Validate(t Task, now time.Time) error {
	for _, field := range []struct{ name, value string }{
		{"owner", t.Owner}, {"gateway_id", t.GatewayID}, {"surface_id", t.SurfaceID},
		{"chat_id", t.ChatID}, {"actor_user_id", t.ActorUserID}, {"name", t.Name},
		{"prompt", t.Prompt}, {"workspace", t.Workspace},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	switch t.Mode {
	case ModeExisting:
		if strings.TrimSpace(t.ThreadID) == "" {
			return errors.New("existing mode requires thread_id")
		}
	case ModeNewEachRun:
		if strings.TrimSpace(t.ThreadID) != "" {
			return errors.New("new_each_run must not specify thread_id")
		}
	default:
		return errors.New("mode must be existing or new_each_run")
	}
	if strings.TrimSpace(t.TimeZone) == "" || t.TimeZone == "Local" {
		return errors.New("an IANA time_zone is required")
	}
	if _, err := time.LoadLocation(t.TimeZone); err != nil {
		return fmt.Errorf("invalid time_zone: %w", err)
	}
	switch t.Kind {
	case KindInterval:
		if t.IntervalSeconds < 10 || t.IntervalSeconds > 31536000 {
			return errors.New("interval_seconds must be between 10 and 31536000")
		}
	case KindDaily:
		v, err := time.Parse("15:04", t.DailyTime)
		if err != nil || len(t.DailyTime) != 5 || v.Format("15:04") != t.DailyTime {
			return errors.New("daily_time must be HH:MM")
		}
	case KindOnce:
		if t.At.IsZero() || !t.At.After(now) {
			return errors.New("at must be in the future")
		}
	default:
		return errors.New("kind must be interval, daily or once")
	}
	return nil
}

func firstDue(t Task, now time.Time) time.Time {
	switch t.Kind {
	case KindInterval:
		return now.Add(time.Duration(t.IntervalSeconds) * time.Second)
	case KindOnce:
		return t.At
	default:
		return nextDaily(t, now)
	}
}

func afterDue(t Task, now time.Time) time.Time {
	if t.Kind == KindOnce {
		return time.Time{}
	}
	if t.Kind == KindDaily {
		return nextDaily(t, now)
	}
	step := time.Duration(t.IntervalSeconds) * time.Second
	if t.NextRunAt.After(now) {
		return t.NextRunAt
	}
	return t.NextRunAt.Add((now.Sub(t.NextRunAt)/step + 1) * step)
}

func nextDaily(t Task, after time.Time) time.Time {
	loc, _ := time.LoadLocation(t.TimeZone)
	v, _ := time.Parse("15:04", t.DailyTime)
	local := after.In(loc)
	for day := 0; ; day++ {
		d := time.Date(local.Year(), local.Month(), local.Day()+day, v.Hour(), v.Minute(), 0, 0, loc)
		// A spring-forward gap has no such local clock time. Skip that date
		// instead of executing at the earlier time normalized by time.Date.
		if d.Hour() != v.Hour() || d.Minute() != v.Minute() {
			continue
		}
		if d.After(after) {
			return d
		}
	}
}

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func terminal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusInterrupted || status == StatusCancelled
}
