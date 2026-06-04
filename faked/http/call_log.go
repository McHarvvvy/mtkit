package http

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CallEvent 是 HTTP 调用日志（JSONL）的单行形态。
// 与 gRPC 不同：包含 Method / Path / Status 字段。
type CallEvent struct {
	Timestamp  time.Time `json:"ts"`
	Endpoint   string    `json:"endpoint"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	RouteKey   string    `json:"route_key"`
	RuleID     string    `json:"rule_id,omitempty"`
	Matched    bool      `json:"matched"`
	ActionKind string    `json:"action_kind,omitempty"`
	Status     int       `json:"status,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// FileCallLogger 把 CallEvent 以 JSONL 写到磁盘。
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

// Log 写入一行 JSONL；失败时静默丢弃。
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

// noopLogger 丢弃所有事件。
type noopLogger struct{}

func (noopLogger) Log(CallEvent) {}

// NoopCallLogger 是单例 noop logger。
var NoopCallLogger CallLogger = noopLogger{}
