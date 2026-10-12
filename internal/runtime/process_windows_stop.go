//go:build windows

package relayruntime

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type windowsStopOps struct {
	snapshot  func() (map[int][]int, error)
	open      func(int) (windows.Handle, error)
	exitCode  func(windows.Handle) (uint32, error)
	terminate func(windows.Handle) error
	wait      func(windows.Handle, uint32) (uint32, error)
	close     func(windows.Handle)
}

func terminateProcess(pid int, grace time.Duration) error {
	return stopWindowsProcessTree(pid, os.Getpid(), grace, windowsStopOps{
		snapshot: snapshotProcessParents,
		open: func(pid int) (windows.Handle, error) {
			return windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		},
		exitCode: func(handle windows.Handle) (uint32, error) {
			var code uint32
			err := windows.GetExitCodeProcess(handle, &code)
			return code, err
		},
		terminate: func(handle windows.Handle) error { return windows.TerminateProcess(handle, 1) },
		wait:      windows.WaitForSingleObject,
		close:     func(handle windows.Handle) { _ = windows.CloseHandle(handle) },
	})
}

// Keep handles until the entire stop completes: exited process IDs cannot be
// reused while we rescan for children born during shutdown. Never stop our own
// subtree when an external upgrade controller is stopping its ancestor.
func stopWindowsProcessTree(rootPID, callerPID int, grace time.Duration, ops windowsStopOps) error {
	if rootPID <= 0 {
		return nil
	}
	if rootPID == callerPID {
		return fmt.Errorf("refusing to terminate caller process %d", rootPID)
	}
	handles := map[int]windows.Handle{}
	known := map[int]bool{rootPID: true}
	defer func() {
		for _, handle := range handles {
			ops.close(handle)
		}
	}()
	deadline := time.Now().Add(max(grace, 2*time.Second))
	for {
		relations, err := ops.snapshot()
		if err != nil {
			return fmt.Errorf("snapshot process tree %d: %w", rootPID, err)
		}
		protected := map[int]bool{}
		collectWindowsProcessTree(callerPID, 0, relations, protected)
		ordered := []int{}
		seen := map[int]bool{}
		// Rescan every previously observed parent, including exited parents.
		// Windows retains a child's original parent ID after its parent exits.
		for pid := range known {
			collectWindowsProcessOrder(pid, protected, relations, seen, &ordered)
		}
		for _, pid := range ordered {
			known[pid] = true
			if _, exists := handles[pid]; exists {
				continue
			}
			handle, err := ops.open(pid)
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				continue // Already exited before a handle could be opened.
			}
			if err != nil {
				return fmt.Errorf("open process %d for tree stop: %w", pid, err)
			}
			handles[pid] = handle
		}
		alive := false
		// Stop each parent before its children, so warm-pool supervisors cannot
		// replace the workers we are terminating. Rescan for the final race.
		for i := len(ordered) - 1; i >= 0; i-- {
			pid := ordered[i]
			handle, exists := handles[pid]
			if !exists {
				continue
			}
			code, err := ops.exitCode(handle)
			if err != nil {
				return fmt.Errorf("query process %d during tree stop: %w", pid, err)
			}
			if code != windowsStillActive {
				continue
			}
			alive = true
			if err := ops.terminate(handle); err != nil {
				return fmt.Errorf("terminate process %d: %w", pid, err)
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("process tree %d exceeded stop deadline", rootPID)
			}
			result, err := ops.wait(handle, waitTimeoutMillis(remaining))
			if err != nil || result != uint32(windows.WAIT_OBJECT_0) {
				return fmt.Errorf("wait for process %d exit: result %d, error %v", pid, result, err)
			}
		}
		if !alive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process tree %d exceeded stop deadline", rootPID)
		}
	}
}

func collectWindowsProcessTree(root, skip int, relations map[int][]int, seen map[int]bool) {
	if root <= 0 || root == skip || seen[root] {
		return
	}
	seen[root] = true
	for _, child := range relations[root] {
		collectWindowsProcessTree(child, skip, relations, seen)
	}
}

func collectWindowsProcessOrder(root int, protected map[int]bool, relations map[int][]int, seen map[int]bool, ordered *[]int) {
	if root <= 0 || protected[root] || seen[root] {
		return
	}
	seen[root] = true
	for _, child := range relations[root] {
		collectWindowsProcessOrder(child, protected, relations, seen, ordered)
	}
	*ordered = append(*ordered, root)
}
