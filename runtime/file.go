package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Read 读取并解析 runtime 文件。
func Read(path string) (RuntimeInfo, error) {
	if path == "" {
		path = DefaultRuntimePath("default")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RuntimeInfo{}, err
	}
	var info RuntimeInfo
	if err = json.Unmarshal(b, &info); err != nil {
		return RuntimeInfo{}, err
	}
	if info.Addr == "" || info.Token == "" {
		return RuntimeInfo{}, errors.New("invalid runtime info")
	}
	return info, nil
}

// Wait 在超时时间内等待 runtime 文件可用。
func Wait(path string, timeout time.Duration) (RuntimeInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := Read(path)
		if err == nil {
			return info, nil
		}
		if time.Now().After(deadline) {
			return RuntimeInfo{}, err
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// Write 以原子方式写入 runtime 文件。
func Write(path string, info RuntimeInfo) error {
	if path == "" {
		path = DefaultRuntimePath("default")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove 删除 runtime 文件。
func Remove(path string) {
	if path == "" {
		path = DefaultRuntimePath("default")
	}
	_ = os.Remove(path)
}
