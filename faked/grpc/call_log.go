package grpc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CallEvent 是 gRPC 调用日志（JSONL）的单行形态。
// 字段顺序与设计文档 §9.1 一致：未命中时 RuleID / ActionKind 留空（omitempty），
// 命中时 Error 留空。
type CallEvent struct {
	Timestamp  time.Time `json:"ts"`
	Endpoint   string    `json:"endpoint"`
	FullMethod string    `json:"full_method"`
	RouteKey   string    `json:"route_key"`
	RuleID     string    `json:"rule_id,omitempty"`
	Matched    bool      `json:"matched"`
	ActionKind string    `json:"action_kind,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// FileCallLogger 把 CallEvent 以 JSONL 写到磁盘。
// 启动时 truncate 文件：每次 faked 启动视为一次干净的测试 session。
type FileCallLogger struct {
	mu   sync.Mutex
	file *os.File
}

// NewFileCallLogger 创建文件日志；上层目录会被自动创建。
func NewFileCallLogger(path string) (*FileCallLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ensure call log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open call log file: %w", err)
	}
	return &FileCallLogger{file: f}, nil
}

// Log 写入一行 JSONL；失败时静默丢弃事件，不阻塞业务路径。
// 调用日志是排查工具，不是断言通路；日志写盘失败不应影响 SUT 请求处理。
func (l *FileCallLogger) Log(ev CallEvent) {
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(line, '\n'))
}

// Close 关闭底层文件。daemon 退出时调用。
func (l *FileCallLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// noopLogger 丢弃所有事件，用于 path 为空或测试场景。
type noopLogger struct{}

func (noopLogger) Log(CallEvent) {}

// NoopCallLogger 是单例 noop logger，方便注入。
var NoopCallLogger CallLogger = noopLogger{}
