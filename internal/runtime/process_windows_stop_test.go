//go:build windows

package relayruntime

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type windowsStopFixture struct {
	parents    map[int][]int
	alive      map[int]bool
	terminated []int
	closed     []int
	onStop     func(int)
}

func (f *windowsStopFixture) ops() windowsStopOps {
	return windowsStopOps{
		snapshot: func() (map[int][]int, error) { return f.parents, nil },
		open: func(pid int) (windows.Handle, error) {
			if _, exists := f.alive[pid]; !exists {
				return 0, windows.ERROR_INVALID_PARAMETER
			}
			return windows.Handle(pid), nil
		},
		exitCode: func(h windows.Handle) (uint32, error) {
			if f.alive[int(h)] {
				return windowsStillActive, nil
			}
			return 0, nil
		},
		terminate: func(h windows.Handle) error {
			pid := int(h)
			f.terminated = append(f.terminated, pid)
			f.alive[pid] = false
			if f.onStop != nil {
				f.onStop(pid)
			}
			return nil
		},
		wait:  func(windows.Handle, uint32) (uint32, error) { return uint32(windows.WAIT_OBJECT_0), nil },
		close: func(h windows.Handle) { f.closed = append(f.closed, int(h)) },
	}
}

func TestWindowsTreeStopPreservesCallerAndItsChildren(t *testing.T) {
	f := &windowsStopFixture{
		parents: map[int][]int{100: {101, 200}, 101: {102}, 200: {201}},
		alive:   map[int]bool{100: true, 101: true, 102: true, 200: true, 201: true},
	}
	if err := stopWindowsProcessTree(100, 200, time.Second, f.ops()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.terminated, []int{100, 101, 102}) {
		t.Fatalf("stopped = %v; want parent first, excluding controller subtree", f.terminated)
	}
	if !f.alive[200] || !f.alive[201] || len(f.closed) != 3 {
		t.Fatalf("caller protection / handle cleanup: alive=%v, closed=%v", f.alive, f.closed)
	}
}

func TestWindowsTreeStopRescansExitRaceThroughRecordedDeadParents(t *testing.T) {
	f := &windowsStopFixture{
		parents: map[int][]int{100: {101}},
		alive:   map[int]bool{100: true, 101: true},
	}
	f.onStop = func(pid int) {
		if pid == 100 {
			// A pool worker spawned just after the initial snapshot. Its parent
			// will exit before the next snapshot and disappear from the root list.
			f.parents[101] = []int{103}
			f.alive[103] = true
		}
		if pid == 101 {
			delete(f.parents, 100)
		}
	}
	if err := stopWindowsProcessTree(100, 900, time.Second, f.ops()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.terminated, []int{100, 101, 103}) || f.alive[103] {
		t.Fatalf("racing child retained: stopped=%v alive=%v", f.terminated, f.alive)
	}
}

func TestWindowsTreeStopCleansOrphansAfterRootAlreadyExited(t *testing.T) {
	f := &windowsStopFixture{parents: map[int][]int{100: {101}}, alive: map[int]bool{101: true}}
	if err := stopWindowsProcessTree(100, 900, time.Second, f.ops()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.terminated, []int{101}) {
		t.Fatalf("orphan not stopped: %v", f.terminated)
	}
}

func TestWindowsTreeStopFailsClosedOnChildErrorsAndClosesHandles(t *testing.T) {
	for _, stage := range []string{"open", "query", "terminate", "wait"} {
		t.Run(stage, func(t *testing.T) {
			f := &windowsStopFixture{parents: map[int][]int{100: {101}}, alive: map[int]bool{100: true, 101: true}}
			ops := f.ops()
			sentinel := errors.New("access refused")
			switch stage {
			case "open":
				original := ops.open
				ops.open = func(pid int) (windows.Handle, error) {
					if pid == 100 {
						return 0, sentinel
					}
					return original(pid)
				}
			case "query":
				original := ops.exitCode
				ops.exitCode = func(h windows.Handle) (uint32, error) {
					if h == 101 {
						return 0, sentinel
					}
					return original(h)
				}
			case "terminate":
				original := ops.terminate
				ops.terminate = func(h windows.Handle) error {
					if h == 101 {
						return sentinel
					}
					return original(h)
				}
			case "wait":
				original := ops.wait
				ops.wait = func(h windows.Handle, ms uint32) (uint32, error) {
					if h == 101 {
						return uint32(windows.WAIT_TIMEOUT), nil
					}
					return original(h, ms)
				}
			}
			err := stopWindowsProcessTree(100, 900, time.Second, ops)
			if err == nil {
				t.Fatal("live process error was silently ignored")
			}
			wantClosed := 2
			if stage == "open" {
				wantClosed = 1
			}
			if len(f.closed) != wantClosed {
				t.Fatalf("leaked handles: %v", f.closed)
			}
		})
	}
}

func TestWindowsTreeStopRejectsCallerAndSnapshotFailure(t *testing.T) {
	f := &windowsStopFixture{}
	if err := stopWindowsProcessTree(100, 100, time.Second, f.ops()); err == nil || !strings.Contains(err.Error(), "caller") {
		t.Fatalf("self-stop = %v", err)
	}
	ops := f.ops()
	ops.snapshot = func() (map[int][]int, error) { return nil, errors.New("snapshot refused") }
	if err := stopWindowsProcessTree(100, 900, time.Second, ops); err == nil || len(f.terminated) != 0 {
		t.Fatalf("unsafe root-only fallback: %v, %v", err, f.terminated)
	}
}

func TestWindowsTreeStopChildOpenDeniedFailsBeforeMutation(t *testing.T) {
	f := &windowsStopFixture{parents: map[int][]int{100: {101}, 101: {102}}, alive: map[int]bool{100: true, 101: true, 102: true}}
	ops := f.ops()
	original := ops.open
	ops.open = func(pid int) (windows.Handle, error) {
		if pid == 101 {
			return 0, windows.ERROR_ACCESS_DENIED
		}
		return original(pid)
	}
	if err := stopWindowsProcessTree(100, 900, time.Second, ops); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("child permission error = %v", err)
	}
	if len(f.terminated) != 0 || !reflect.DeepEqual(f.closed, []int{102}) {
		t.Fatalf("failed preflight mutated or leaked: stopped=%v closed=%v", f.terminated, f.closed)
	}
}
