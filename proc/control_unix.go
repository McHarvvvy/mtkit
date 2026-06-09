//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

func PrepareCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return nil
}

func AfterStart(cmd *exec.Cmd) error {
	return nil
}

// SignalGraceful 向 SUT 发送 SIGTERM，请求其优雅退出。
func SignalGraceful(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return syscall.ESRCH
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}

func TerminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
		if err = syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}

func ReleaseProcess(cmd *exec.Cmd) {}
