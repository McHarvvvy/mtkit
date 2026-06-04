package protocol

import "time"

// AcquireReq 表示租约申请请求。
type AcquireReq struct {
	Project string `json:"project"`
	PID     int    `json:"pid"`
	RunID   string `json:"run_id,omitempty"`
}

// AcquireResp 表示租约申请响应。
type AcquireResp struct {
	LeaseID    string    `json:"lease_id"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// HeartbeatReq 表示租约心跳请求。
type HeartbeatReq struct {
	LeaseID string `json:"lease_id"`
}

// ReleaseReq 表示租约释放请求。
type ReleaseReq struct {
	LeaseID  string `json:"lease_id"`
	ExitCode int    `json:"exit_code"`
}

// OKResp 表示通用成功响应。
type OKResp struct {
	OK bool `json:"ok"`
}

// StateResp 表示守护进程状态响应。
type StateResp struct {
	InfraReady     bool      `json:"infra_ready"`
	ActiveLeases   int       `json:"active_leases"`
	Containers     []string  `json:"containers"`
	StartedAt      time.Time `json:"started_at"`
	IdleTTLSecond  int       `json:"idle_ttl_second"`
	AppReady       bool      `json:"app_ready"`
	AppPID         int       `json:"app_pid"`
	AppStartedAt   time.Time `json:"app_started_at"`
	AppRestarts    int       `json:"app_restarts"`
	AppLastError   string    `json:"app_last_error,omitempty"`
	FakedReady     bool      `json:"faked_ready"`
	FakedPID       int       `json:"faked_pid"`
	FakedStartedAt time.Time `json:"faked_started_at"`
	FakedRestarts  int       `json:"faked_restarts"`
	FakedLastError string    `json:"faked_last_error,omitempty"`
}
