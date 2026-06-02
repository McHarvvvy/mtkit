//go:build windows

package sut

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var sutJobStore sync.Map // map[int]windows.Handle

func prepareSUTCommand(cmd *exec.Cmd) error {
	return nil
}

func afterSUTStart(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("sut process is nil")
	}
	// Windows 下使用 JobObject 兜底管理子进程链，
	// 避免仅 Kill 主进程后遗留后台子进程。
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
	sutJobStore.Store(cmd.Process.Pid, job)
	return nil
}

func terminateSUTProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// 先终止 JobObject，确保其下全部进程统一退出。
	if jh, ok := sutJobStore.LoadAndDelete(pid); ok {
		if job, ok2 := jh.(windows.Handle); ok2 {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}
	// 再用 taskkill /T 做系统级兜底，覆盖非 JobObject 场景。
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	if err := cmd.Process.Kill(); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
	}
	return nil
}

func releaseSUTProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if jh, ok := sutJobStore.LoadAndDelete(cmd.Process.Pid); ok {
		if job, ok2 := jh.(windows.Handle); ok2 {
			_ = windows.CloseHandle(job)
		}
	}
}
