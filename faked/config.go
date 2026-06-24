package faked

import "strings"

// Config 是 daemon 进程内构造 faked server 的配置，来自 mtkit.config.json 的 faked 块。
//
// faked 内嵌 daemon 主进程：control 面挂 daemon 主 mux（无独立 control 监听），
// 地址由 daemon 写入 mtkit.runtime.json（无独立 faked runtime 文件）。因此本结构
// 只保留数据面参数：代理监听、CA、产物目录。gRPC endpoint 的监听地址不在此——
// 由各 Endpoint.Listen() 在代码中声明（见 faked-integration-plan §9.2）。
type Config struct {
	// ProxyListen 是 HTTP MITM 代理监听地址（默认 127.0.0.1:0）。
	ProxyListen string

	// CACertFile / CAKeyFile：都空走动态 CA（默认），都填走静态 CA，半填拒绝启动。
	CACertFile string
	CAKeyFile  string

	// RuntimeDir 是 faked 写动态 CA 等私有产物的目录（约定 <mtkit.runtime_dir>/faked）。
	RuntimeDir string

	// CallLogDir 是 faked 写调用日志（faked_{grpc,http}_calls.jsonl）的目录。
	// 调用日志是排查用的可观察证据，与 daemon.log / app.log 同属诊断流，故统一汇聚到
	// <mtkit.runtime_dir>/logs；动态 CA 是凭据、性质不同，仍留在 RuntimeDir。
	// 留空时回退到 RuntimeDir，保证独立构造 Config 的单测/旧用法行为不变。
	CallLogDir string
}

// NormalizeConfig 填默认值并做跨字段校验。
func NormalizeConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.RuntimeDir) == "" {
		return errConfig("runtime_dir is required")
	}
	if strings.TrimSpace(cfg.CallLogDir) == "" {
		cfg.CallLogDir = cfg.RuntimeDir
	}
	if strings.TrimSpace(cfg.ProxyListen) == "" {
		cfg.ProxyListen = "127.0.0.1:0"
	}
	if (cfg.CACertFile == "") != (cfg.CAKeyFile == "") {
		return errConfig("ca_cert_file and ca_key_file must be both set or both empty")
	}
	return nil
}

type configError string

func (e configError) Error() string { return "faked config: " + string(e) }

func errConfig(msg string) error { return configError(msg) }
