//go:build windows

package agentsetup

// A tested MCP server is usually a shim (npx.cmd, uvx) that starts Node or
// Python, which starts the real server. Killing only the shim would leave
// those running, so the server goes into a kill-on-close Job Object.

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	treesMu sync.Mutex
	trees   = map[*exec.Cmd]windows.Handle{}
)

// hideWindow keeps console servers from flashing a window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func containTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	treesMu.Lock()
	trees[cmd] = job
	treesMu.Unlock()
}

func killTree(cmd *exec.Cmd) {
	treesMu.Lock()
	job, ok := trees[cmd]
	delete(trees, cmd)
	treesMu.Unlock()
	if ok {
		_ = windows.CloseHandle(job)
	}
}
