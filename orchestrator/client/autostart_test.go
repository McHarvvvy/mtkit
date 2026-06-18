package client

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// pruneRunnerDir 是 daemon re-exec 副本（mt_*）的清理逻辑，仅 Windows 会产出这些副本，
// 但清理本身与平台无关，故这些用例在任意 OS 上都跑。
//
// writeRunner 写一个副本文件并把 mtime 钉成显式值：文件系统创建顺序与 mtime 精度都不可靠，
// 唯有显式 Chtimes 才能让"按 mtime 取最新 keep 个"的断言确定性成立。
func writeRunner(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write runner %s: %v", name, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes runner %s: %v", name, err)
	}
	return path
}

func remainingNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestPruneRunnerDir_KeepsNewestAndDropsOlder 验证副本数超过上限时，
// 只保留 mtime 最新的 keep 个，更旧的被删除。
func TestPruneRunnerDir_KeepsNewestAndDropsOlder(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-1 * time.Hour)
	// 写 5 个副本，mtime 间隔 1 分钟递增；mt_5 最新、mt_1 最旧。
	for i := 1; i <= 5; i++ {
		writeRunner(t, dir, "mt_"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
	}

	pruneRunnerDir(dir, 2)

	got := remainingNames(t, dir)
	want := []string{"mt_4", "mt_5"} // sort.Strings 后字典序
	if len(got) != len(want) {
		t.Fatalf("expected %d kept, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kept set mismatch: want %v, got %v", want, got)
		}
	}
}

// TestPruneRunnerDir_NoopWhenWithinLimit 验证副本数未超上限时一个都不删。
func TestPruneRunnerDir_NoopWhenWithinLimit(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	writeRunner(t, dir, "mt_a", base)
	writeRunner(t, dir, "mt_b", base.Add(time.Minute))

	pruneRunnerDir(dir, 5)

	if got := remainingNames(t, dir); len(got) != 2 {
		t.Fatalf("expected both kept, got %v", got)
	}
}

// TestPruneRunnerDir_IgnoresNonRunnerEntries 验证清理只作用于 mt_ 前缀文件，
// 不碰其它文件与子目录——避免误删同目录下的无关产物。
func TestPruneRunnerDir_IgnoresNonRunnerEntries(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-1 * time.Hour)
	// 3 个 mt_ 副本 + 1 个无关文件 + 1 个子目录（名字也带 mt_ 前缀，验证不误删目录）。
	for i := 1; i <= 3; i++ {
		writeRunner(t, dir, "mt_"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
	}
	if err := os.WriteFile(filepath.Join(dir, "keepme.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "mt_subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	pruneRunnerDir(dir, 1)

	got := remainingNames(t, dir)
	// 期望：最新副本 mt_3 + 无关文件 + 子目录都在；mt_1 / mt_2 被删。
	want := []string{"keepme.txt", "mt_3", "mt_subdir"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

// TestPruneRunnerDir_MissingDir 验证目录不存在时安全静默返回，不 panic、不报错。
func TestPruneRunnerDir_MissingDir(t *testing.T) {
	pruneRunnerDir(filepath.Join(t.TempDir(), "does-not-exist"), 5)
}
