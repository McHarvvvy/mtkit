package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
)

// OpenAppLogFile 打开 app.log 文件并确保父目录存在。
func OpenAppLogFile(runtimePath string) (*os.File, error) {
	return openLogFile(AppLogPath(runtimePath))
}

// OpenDaemonLogFile 打开 daemon.log 文件并确保父目录存在。
func OpenDaemonLogFile(runtimePath string) (*os.File, error) {
	return openLogFile(DaemonLogPath(runtimePath))
}

// AttachCmdOutput 将进程标准输出与错误输出都重定向到同一文件。
func AttachCmdOutput(cmd *exec.Cmd, output *os.File) {
	if cmd == nil || output == nil {
		return
	}
	cmd.Stdout = output
	cmd.Stderr = output
}

func openLogFile(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}
