//go:build windows

package relayruntime

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

const (
	th32csSnapProcess = 0x00000002
	invalidHandle     = ^uintptr(0)
)

var (
	modKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = modKernel32.NewProc("Process32FirstW")
	procProcess32NextW           = modKernel32.NewProc("Process32NextW")
)

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PcPriClassBase  int32
	Flags           uint32
	ExeFile         [windows.MAX_PATH]uint16
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == windowsStillActive
}

func prepareDetachedProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	execlaunch.Prepare(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS
}

func waitTimeoutMillis(grace time.Duration) uint32 {
	if grace <= 0 {
		return 0
	}
	ms := grace.Milliseconds()
	if ms <= 0 {
		return 1
	}
	if ms > int64(^uint32(0)>>1) {
		return ^uint32(0) >> 1
	}
	return uint32(ms)
}

func snapshotProcessParents() (map[int][]int, error) {
	handle, _, callErr := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if handle == invalidHandle {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return nil, callErr
		}
		return nil, syscall.EINVAL
	}
	snapshot := windows.Handle(handle)
	defer windows.CloseHandle(snapshot)

	entry := processEntry32{Size: uint32(unsafe.Sizeof(processEntry32{}))}
	ret, _, callErr := procProcess32FirstW.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return nil, callErr
		}
		return map[int][]int{}, nil
	}

	relations := map[int][]int{}
	for {
		parent := int(entry.ParentProcessID)
		child := int(entry.ProcessID)
		if child > 0 {
			relations[parent] = append(relations[parent], child)
		}
		ret, _, callErr = procProcess32NextW.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
		if ret != 0 {
			continue
		}
		if callErr == syscall.ERROR_NO_MORE_FILES || callErr == windows.ERROR_NO_MORE_FILES {
			break
		}
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return nil, callErr
		}
		break
	}
	return relations, nil
}
