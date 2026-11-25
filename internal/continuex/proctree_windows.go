//go:build windows

package continuex

// Agent CLIs often start helpers of their own (OpenCode runs a server, npm
// shims run Node, which runs the native binary). Killing only the process we
// started leaves those running: they keep the project folder locked and use
// memory after the chat is closed. Each live session's process is placed in
// a Job Object configured to kill every process in it when the job closes.

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	treesMu sync.Mutex
	trees   = map[*exec.Cmd]windows.Handle{}
)

// containTree puts a started command (and anything it spawns later) into a
// kill-on-close job. Best effort: failure leaves normal single-process kill.
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

// killTree terminates every process left in the command's job. Safe to call
// more than once and for commands that were never contained.
func killTree(cmd *exec.Cmd) {
	treesMu.Lock()
	job, ok := trees[cmd]
	delete(trees, cmd)
	treesMu.Unlock()
	if ok {
		_ = windows.CloseHandle(job)
	}
}
