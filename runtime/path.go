package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultRuntimePath 返回按 project 隔离的 runtime 文件路径。
func DefaultRuntimePath(project string) string {
	project = sanitizeProject(project)
	return filepath.Join(os.TempDir(), "mt", project, "runtime.json")
}

// ArtifactDir 返回 mtkit 运行中间产物目录。
func ArtifactDir(runtimePath string) string {
	runtimePath = strings.TrimSpace(runtimePath)
	if runtimePath == "" {
		runtimePath = DefaultRuntimePath("default")
	}
	return filepath.Dir(runtimePath)
}

// AppLogPath 返回 app 日志默认路径。
func AppLogPath(runtimePath string) string {
	return filepath.Join(ArtifactDir(runtimePath), "app.log")
}

// DaemonLogPath 返回 daemon 日志默认路径。
func DaemonLogPath(runtimePath string) string {
	return filepath.Join(ArtifactDir(runtimePath), "daemon.log")
}

// RunnerDir 返回 daemon runner 目录。
func RunnerDir(runtimePath string) string {
	return filepath.Join(ArtifactDir(runtimePath), "runner")
}

func sanitizeProject(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return "default"
	}
	project = strings.ReplaceAll(project, "\\", "_")
	project = strings.ReplaceAll(project, "/", "_")
	project = strings.ReplaceAll(project, ":", "_")
	project = strings.ReplaceAll(project, " ", "_")
	if project == "" {
		return "default"
	}
	return project
}
