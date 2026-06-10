package protocol

import mtconstant "github.com/McHarvvvy/mtkit/constant"

// HeaderToken 是鉴权请求头。值的单一来源在 constant 包，lease 与 faked control 共用同一头。
const HeaderToken = mtconstant.HeaderToken

const (
	// PathAcquire 是租约申请接口。
	PathAcquire = "/v1/acquire"
	// PathHeartbeat 是租约心跳接口。
	PathHeartbeat = "/v1/heartbeat"
	// PathRelease 是租约释放接口。
	PathRelease = "/v1/release"
	// PathState 是守护进程状态接口。
	PathState = "/v1/state"
)
