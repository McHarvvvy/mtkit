//go:build !windows

package sut

import (
	"os/exec"
	"syscall"
)

func prepareSUTCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// 关键决策：SUT 放到独立进程组，后续可通过负 pgid 一次性终止整棵子进程树。
	cmd.SysProcAttr.Setpgid = true
	return nil
}

func afterSUTStart(cmd *exec.Cmd) error {
	return nil
}

func terminateSUTProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// 优先按进程组杀死，确保 shell/runner 拉起的孙进程不会泄漏。
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
		if err = syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	// 进程组不可用时回退到主进程强杀。
	return cmd.Process.Kill()
}

func releaseSUTProcess(cmd *exec.Cmd) {}
