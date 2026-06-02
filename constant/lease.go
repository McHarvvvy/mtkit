package constant

import "time"

const (
	// LeaseTTL 是租约固定有效期。
	LeaseTTL = 2 * time.Second
	// HeartbeatInterval 是固定心跳间隔，恒为 LeaseTTL 的二分之一。
	HeartbeatInterval = LeaseTTL / 2
)
