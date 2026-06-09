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
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// 子进程独立进程组，使 Stop 时的 CTRL_BREAK 只命中它、不波及父 daemon。
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	return nil
}

// SignalGraceful 向 SUT 进程组发送 CTRL_BREAK，请求其优雅退出。
//
// 进程组 ID = 子进程 PID（PrepareCommand 设了 CREATE_NEW_PROCESS_GROUP），故事件只命中
// SUT 子树。子进程经 signal.Notify(os.Interrupt) 收到该事件。无 console 等环境会返回
// error，由调用方（proc.Stop）落到强制终止兜底。
func SignalGraceful(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
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
