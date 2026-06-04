//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var jobStore sync.Map // map[int]windows.Handle

func PrepareCommand(cmd *exec.Cmd) error {
	return nil
}

func AfterStart(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(ph)
	if err = windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	jobStore.Store(cmd.Process.Pid, job)
	return nil
}

func TerminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if jh, ok := jobStore.LoadAndDelete(pid); ok {
		if job, ok2 := jh.(windows.Handle); ok2 {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	if err := cmd.Process.Kill(); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
	}
	return nil
}

func ReleaseProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if jh, ok := jobStore.LoadAndDelete(cmd.Process.Pid); ok {
		if job, ok2 := jh.(windows.Handle); ok2 {
			_ = windows.CloseHandle(job)
		}
	}
}
