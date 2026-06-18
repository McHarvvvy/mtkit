package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

func (c *Client) autoStartDaemon(ctx context.Context) error {
	// 关键决策：并发执行 go test -count=1 ./... 时会有多个测试进程同时尝试拉起 daemon。
	// 这里通过 runtime 文件旁路锁确保同一时刻只有一个进程真正执行 Start，其它进程只等待 runtime 就绪并复用。
	lockPath := startLockPath(c.cfg.RuntimePath)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		lockFile, owner, err := tryAcquireStartLock(lockPath)
		if err != nil {
			return err
		}
		// 抢到锁
		if owner {
			defer releaseStartLock(lockFile)
			if info, readErr := mtruntime.Read(c.cfg.RuntimePath); readErr == nil {
				if waitRuntimeAlive(ctx, c, info, 3*time.Second) {
					return nil
				}
				// 持锁且确认 runtime 已失效后再移除，避免误删健康 daemon 的发现文件。
				_ = os.Remove(c.cfg.RuntimePath)
			}
			cmd, buildErr := c.buildDaemonCommand(ctx)
			if buildErr != nil {
				return buildErr
			}
			logFile, attachErr := attachDaemonOutput(cmd, c.cfg.RuntimePath)
			if attachErr != nil {
				return attachErr
			}
			if err = cmd.Start(); err != nil {
				_ = logFile.Close()
				if _, waitErr := mtruntime.Wait(c.cfg.RuntimePath, 3*time.Second); waitErr == nil {
					return nil
				}
				return err
			}
			go func() {
				defer logFile.Close()
				_ = cmd.Wait()
			}()
			info, err := mtruntime.Wait(c.cfg.RuntimePath, 10*time.Second)
			if err != nil {
				return err
			}
			if waitRuntimeAlive(ctx, c, info, 5*time.Second) {
				return nil
			}
			return fmt.Errorf("daemon runtime created but service not reachable")
		}
		// 没抢到锁，等待 runtime 就绪
		if _, runtimeErr := mtruntime.Wait(c.cfg.RuntimePath, 1500*time.Millisecond); runtimeErr == nil {
			return nil
		}
		// 关键决策：只有在锁文件明显陈旧且 runtime 不存在时才抢占，
		// 避免正常慢启动期间误删锁导致并发启动多个 daemon。
		if isStaleStartLock(lockPath, c.cfg.RuntimePath, 90*time.Second) {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait daemon start lock timeout: %s", lockPath)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func waitRuntimeAlive(ctx context.Context, c *Client, info mtruntime.RuntimeInfo, timeout time.Duration) bool {
	// 关键决策：runtime 文件写入与服务可接入并非同一时刻，
	// 这里增加短时间探活等待，降低“已写 runtime 但端口尚未可用”误判。
	deadline := time.Now().Add(timeout)
	for {
		if c.runtimeAlive(ctx, info) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func startLockPath(runtimePath string) string {
	return runtimePath + ".start.lock"
}

func tryAcquireStartLock(lockPath string) (*os.File, bool, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, false, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = io.WriteString(f, fmt.Sprintf("pid=%d\ntime=%d\n", os.Getpid(), time.Now().Unix()))
		return f, true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func releaseStartLock(lockFile *os.File) {
	if lockFile == nil {
		return
	}
	path := lockFile.Name()
	_ = lockFile.Close()
	_ = os.Remove(path)
}

func isStaleStartLock(lockPath, runtimePath string, staleAfter time.Duration) bool {
	// 关键决策：仅在“锁超时 + runtime 不存在”时认定为陈旧锁，
	// 避免正常慢启动期间误抢锁导致多 daemon 并发启动。
	stat, err := os.Stat(lockPath)
	if err != nil {
		return false
	}
	if time.Since(stat.ModTime()) < staleAfter {
		return false
	}
	if _, err = mtruntime.Read(runtimePath); err == nil {
		return false
	}
	return true
}

func (c *Client) buildDaemonCommand(ctx context.Context) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	// 关键决策：默认不依赖源码目录推导，而是重启当前测试二进制进入 daemon 模式，
	// 避免 runtime.Caller/工作目录在不同编译参数和执行器下不稳定。
	runner, err := prepareDaemonExecutable(exe, c.cfg.RuntimePath)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, runner, "-test.run=^$")
	cmd.Env = mergeEnv(os.Environ(), map[string]string{
		mtconstant.EnvMTKitRunMode: mtconstant.MTModeDaemon,
	})
	return cmd, nil
}

func prepareDaemonExecutable(exe, runtimePath string) (string, error) {
	if runtime.GOOS != "windows" {
		return exe, nil
	}

	// 关键决策：Windows 上直接复用 go test 临时二进制会触发文件锁，导致测试进程结束时无法删除可执行文件。
	// 因此先复制一份 runner 再启动，确保父进程可正常回收临时文件。
	runnerDir := mtruntime.RunnerDir(runtimePath)
	if err := os.MkdirAll(runnerDir, 0o755); err != nil {
		return "", fmt.Errorf("create daemon runner dir: %w", err)
	}
	runner := filepath.Join(runnerDir, fmt.Sprintf("mt_%d%s", time.Now().UnixNano(), filepath.Ext(exe)))
	if err := copyFile(exe, runner); err != nil {
		return "", fmt.Errorf("prepare daemon runner executable: %w", err)
	}
	pruneRunnerDir(runnerDir, maxRunnerCopies)
	return runner, nil
}

// maxRunnerCopies 是 daemon re-exec 副本的保留上限：超出的旧副本在每次新建后被清理，防止无限累积。
const maxRunnerCopies = 5

// pruneRunnerDir 保留 runnerDir 下最新的 keep 个 mt_* 副本、删除更旧的。
//
// 仅 Windows 产出这些副本（见 prepareDaemonExecutable）。当前运行中的 daemon 二进制是刚复制出的最新一个、
// 必在保留集内，且 Windows 文件锁也会阻止删除占用中的副本——故清理实际只作用于已退出 daemon 的陈旧副本。
// 删除失败（仍被占用 / 竞争）一律忽略，绝不阻断启动流程。
func pruneRunnerDir(runnerDir string, keep int) {
	entries, err := os.ReadDir(runnerDir)
	if err != nil {
		return
	}
	type runnerFile struct {
		path string
		mod  time.Time
	}
	files := make([]runnerFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "mt_") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		files = append(files, runnerFile{filepath.Join(runnerDir, e.Name()), info.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[keep:] {
		_ = os.Remove(f.path)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := os.FileMode(0o755)
	if fi, statErr := in.Stat(); statErr == nil {
		mode = fi.Mode()
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func mergeEnv(base []string, override map[string]string) []string {
	m := make(map[string]string, len(base)+len(override))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		m[parts[0]] = parts[1]
	}
	for k, v := range override {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func attachDaemonOutput(cmd *exec.Cmd, runtimePath string) (*os.File, error) {
	logFile, err := mtruntime.OpenDaemonLogFile(runtimePath)
	if err != nil {
		return nil, err
	}
	mtruntime.AttachCmdOutput(cmd, logFile)
	return logFile, nil
}
