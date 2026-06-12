package protocol

// AllocReq 请求一个半开 token 区块 [start, end)。
type AllocReq struct {
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

// AllocResp 是发号成功响应，返回半开区间 [start, end)。
type AllocResp struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Ledger 是 provision 账本的磁盘格式：已成功 Apply 的资源 Key 列表。
// daemon 启动时写空（{"applied":[]}），client 在文件锁内读改写；两侧共用同一结构保证序列化一致。
type Ledger struct {
	Applied []string `json:"applied"`
}
