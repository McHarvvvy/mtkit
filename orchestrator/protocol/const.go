package protocol

const (
	// HeaderToken 是鉴权请求头。
	HeaderToken = "X-Mtkit-Token"

	// PathAcquire 是租约申请接口。
	PathAcquire = "/v1/acquire"
	// PathHeartbeat 是租约心跳接口。
	PathHeartbeat = "/v1/heartbeat"
	// PathRelease 是租约释放接口。
	PathRelease = "/v1/release"
	// PathState 是守护进程状态接口。
	PathState = "/v1/state"
)
