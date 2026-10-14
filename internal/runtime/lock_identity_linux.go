//go:build linux

package relayruntime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// PID alone can identify an unrelated process after a reboot or PID reuse.
// Keep kernel identities as opaque strings so clock adjustments do not affect
// ownership checks for newly written locks.
func lockProcessIdentity(pid int) (bootID, processStart string) {
	if raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		bootID = strings.TrimSpace(string(raw))
	}
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		processStart = lockProcessStart(string(raw))
	}
	return
}

func lockProcessStart(stat string) string {
	// comm (field 2) can contain spaces and parentheses. The remaining fields
	// start after its final closing parenthesis; starttime is field 22.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return ""
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return ""
	}
	return fields[19]
}

func lockOwnerReplaced(record lockRecord) bool {
	bootID, processStart := lockProcessIdentity(record.PID)
	if record.BootID != "" && bootID != "" && record.BootID != bootID {
		return true
	}
	if record.ProcessStart != "" && processStart != "" && record.ProcessStart != processStart {
		return true
	}
	// Legacy locks have only PID and creation time. Reclaim them only with
	// positive evidence that they predate this boot; missing proc metadata
	// must never turn a live owner into a stale one.
	if record.BootID != "" || record.CreatedAt.IsZero() {
		return false
	}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		return err == nil && record.CreatedAt.Before(time.Unix(seconds, 0))
	}
	return false
}
