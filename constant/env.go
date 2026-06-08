package constant

const (
	// EnvMTKitConfigPath 指定 mtkit 配置文件路径。
	EnvMTKitConfigPath = "MTKIT_CONFIG_PATH"
	// EnvMTKitRunMode 指定 test 进程运行模式。
	EnvMTKitRunMode = "MTKIT_RUN_MODE"
	// EnvMTKitCACertFile 是 mtkit 注入给 SUT 的 faked 根 CA 公钥路径环境变量名。
	// 须与 SUT 侧拷入的 mtkit CA 插件（addons/mtkit_ca_addon.go）读取的名字一致。
	EnvMTKitCACertFile = "MTKIT_CA_CERT_FILE"

	// MTModeDaemon 表示守护进程模式。
	MTModeDaemon = "daemon"
)
