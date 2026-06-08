package echopb

// 这些常量在 e2e 的 faked endpoint 注册（echoendpoint）与真 SUT（internal/sut）之间共享：
// SUT 经注入 env 读 gRPC 直连地址，经代理对 Host 发 HTTPS。放在 echopb 是为了让 SUT
// 只依赖 proto 包、不必拽入 faked 服务端代码。
const (
	// EnvKeyEchoGRPC 是 echo gRPC endpoint 的 EnvKey()——daemon 把直连地址注入到该 env。
	EnvKeyEchoGRPC = "E2E_ECHO_GRPC_ADDR"
	// Host 是 e2e faked HTTP endpoint 的域名（与任何真实/业务域名不重名）。
	Host = "acc.e2e.faked"
	// PathLogin 是 SUT HTTPS 调用的路径；route_key 从 JSON body 字段 account_id 解出。
	PathLogin = "/v1/login"
)
