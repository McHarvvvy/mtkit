package runtime

import "time"

// RuntimeInfo 表示 daemon 发现文件内容。
type RuntimeInfo struct {
	Addr      string    `json:"addr"`
	Token     string    `json:"token"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}
