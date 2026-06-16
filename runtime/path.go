package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// baseDirName 是 mtkit 默认产物根目录名；home 与 temp 两处共用此常量，避免命名漂移。
const baseDirName = ".mtkit"

// DefaultRuntimePath 返回按 project 隔离的 runtime 文件路径。
func DefaultRuntimePath(project string) string {
	project = sanitizeProject(project)
	return filepath.Join(os.TempDir(), baseDirName, project, DefaultRuntimeFileName)
}

// DefaultConfigPath 返回 mtkit 默认配置文件路径。
func DefaultConfigPath(project string) string {
	project = sanitizeProject(project)
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), baseDirName, project, DefaultConfigFileName)
	}
	return filepath.Join(home, baseDirName, project, DefaultConfigFileName)
}

// DefaultRuntimeFileName 是 runtime 文件的默认文件名（runtime_file 未配置时使用）。
const DefaultRuntimeFileName = "mtkit.runtime.json"

// DefaultConfigFileName 是 mtkit 配置文件的默认文件名。
const DefaultConfigFileName = "mtkit.config.json"

// ResolveRuntimeFile 把配置的 runtime_file 解析为最终路径：
//   - file 为空 → <runtimeDir>/mtkit.runtime.json（默认）
//   - file 是绝对路径 → 原样使用
//   - file 是相对路径 / 纯文件名 → 落在 runtimeDir 下
func ResolveRuntimeFile(runtimeDir, file string) string {
	runtimeDir = strings.TrimSpace(runtimeDir)
	file = strings.TrimSpace(file)
	if file == "" {
		file = DefaultRuntimeFileName
	}
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(runtimeDir, file)
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
