package mtkit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/McHarvvvy/mtkit/constant"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// LifecycleConfig 是配置层的进程生命周期参数；只承载 JSON 可表达的秒数，
// 运行期的 time.Duration 由门面消费时（toSUTRuntimeConfig）派生。
type LifecycleConfig struct {
	BuildTimeoutSeconds int `json:"build_timeout_seconds"`
	ReadyTimeoutSeconds int `json:"ready_timeout_seconds"`
	GracePeriodSeconds  int `json:"grace_period_seconds"`
}

type ProcessConfig struct {
	Enabled    bool            `json:"enabled"`
	BinaryPath string          `json:"binary_path"`
	SourceDir  string          `json:"source_dir"`
	BuildArgs  []string        `json:"build_args"`
	Lifecycle  LifecycleConfig `json:"lifecycle"`
}

type SUTConfig struct {
	ProcessConfig
	IdleTTLSeconds int `json:"idle_ttl_seconds"`
}

// FakedConfig 是 mtkit.config.json 的 faked 块：faked 内嵌 daemon，故只含数据面参数，
// 无 source_dir / binary_path / lifecycle / config_path（见 faked-integration-plan §2）。
//
// gRPC endpoint 的监听地址不在配置里——由各 endpoint 的 Endpoint.Listen() 在代码中声明
// （与 EnvKey() 同属项目领域知识，见 faked-integration-plan §9.2）。
type FakedConfig struct {
	Enabled bool            `json:"enabled"`
	HTTP    FakedHTTPConfig `json:"http"`
}

type FakedHTTPConfig struct {
	ProxyListen string `json:"proxy_listen"`
	CACertFile  string `json:"ca_cert_file"`
	CAKeyFile   string `json:"ca_key_file"`
}

// LoadConfig 按 MTKIT_CONFIG_PATH → 默认路径加载 mtkit JSON 配置。
func LoadConfig(project string) (*Config, error) {
	path := strings.TrimSpace(os.Getenv(constant.EnvMTKitConfigPath))
	if path == "" {
		path = mtruntime.DefaultConfigPath(project)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mtkit config %s: %w", path, err)
	}
	var cfg Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse mtkit config %s: %w", path, err)
	}
	if err = normalizeConfig(project, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func normalizeConfig(project string, cfg *Config) error {
	if p := strings.TrimSpace(project); p != "" {
		cfg.Project = p
	}
	if strings.TrimSpace(cfg.Project) == "" {
		cfg.Project = "default"
	}
	cfg.RuntimeDir = strings.TrimSpace(cfg.RuntimeDir)
	if cfg.RuntimeDir == "" {
		return fmt.Errorf("mtkit config runtime_dir is required")
	}
	cfg.RuntimeFile = mtruntime.ResolveRuntimeFile(cfg.RuntimeDir, cfg.RuntimeFile)

	if strings.TrimSpace(cfg.DaemonMode.Addr) == "" {
		cfg.DaemonMode.Addr = "127.0.0.1:0"
	}
	// 超时 / TTL 类参数在配置层只保留 *Seconds（JSON 无法表达 time.Duration）；
	// 默认值与 Duration 转换统一在门面消费点完成（见 Run / toSUTRuntimeConfig）。
	if cfg.Faked.Enabled && (cfg.Faked.HTTP.CACertFile == "") != (cfg.Faked.HTTP.CAKeyFile == "") {
		return fmt.Errorf("mtkit config faked.http ca_cert_file and ca_key_file must be both set or both empty")
	}
	if err := validateProcessConfig("sut", cfg.SUT.Enabled, cfg.SUT.BinaryPath, cfg.SUT.SourceDir); err != nil {
		return err
	}
	return nil
}

func validateProcessConfig(name string, enabled bool, binaryPath, sourceDir string) error {
	if !enabled {
		return nil
	}
	if strings.TrimSpace(binaryPath) == "" && strings.TrimSpace(sourceDir) == "" {
		return fmt.Errorf("mtkit config %s requires binary_path or source_dir when enabled=true", name)
	}
	return nil
}

func secondsOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
