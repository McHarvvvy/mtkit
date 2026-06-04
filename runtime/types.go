package runtime

import "time"

// RuntimeInfo 表示 daemon 发现文件内容。
type RuntimeInfo struct {
	Addr      string        `json:"addr"`
	Token     string        `json:"token"`
	PID       int           `json:"pid"`
	StartedAt time.Time     `json:"started_at"`
	Faked     *FakedRuntime `json:"faked,omitempty"`
	// SUTEnv 是 daemon 注入给 SUT 的全部环境变量（容器连接信息 + faked 连接信息），
	// 启动后落盘，纯排查用。
	SUTEnv map[string]string `json:"sut_env,omitempty"`
}

// FakedRuntime 是内嵌 faked 引擎数据面地址（faked 未启用或未就绪时为 nil）。
//
// 供调试 / 可见性：测试 client 用 Addr（daemon control 端口）注册 rule、SUT 用注入的
// env 拿地址，二者均不依赖本段；它只是把 faked 地址也落进 runtime 文件方便排查。
type FakedRuntime struct {
	GRPCEndpoints map[string]string `json:"grpc_endpoints,omitempty"`
	ProxyAddr     string            `json:"proxy_addr,omitempty"`
	CACertFile    string            `json:"ca_cert_file,omitempty"`
}
