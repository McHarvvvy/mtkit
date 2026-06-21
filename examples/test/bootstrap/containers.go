package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/docker/go-connections/nat"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// SUTEnv 回调吐出的 env key 是 **bootstrap 与 SUT 消费侧的共享契约**：mtkit 据此把容器连接串注入 SUT，
// 你项目的 schema/seeder 也据同一 key 经 PersistEnv 取连接。两边 key 必须逐字一致。
const (
	EnvKeyMySQLDSN  = "SHOP_IT_MYSQL_DSN"
	EnvKeyRedisAddr = "SHOP_IT_REDIS_ADDR"
)

// registerDefaultContainers 声明玩具服务所需容器（mysql + redis）。
// Init 钩子做“SUT 启动就依赖”的一次性初始化（建库）；测试用表放 provision（见 fixture/schema）。
func registerDefaultContainers(_ context.Context) ([]container.ContainerRegistration, error) {
	return []container.ContainerRegistration{
		{
			Name: "shop-mysql",
			Start: func(ctx context.Context) (testcontainers.Container, error) {
				return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
					ContainerRequest: testcontainers.ContainerRequest{
						Name:         "shop-mysql",
						Image:        "mysql:8.0.42", // 用本机/CI 可得的固定 tag，避免每次首拉
						ExposedPorts: []string{"3306/tcp"},
						Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "pass"},
						WaitingFor:   wait.ForListeningPort("3306/tcp").WithStartupTimeout(60 * time.Second),
					},
					Started: true,
				})
			},
			// Init：建库（SUT 连上就要有库）。建“测试用表”不在这里——那是 provision 的职责。
			Init: initMySQL,
			// SUTEnv：容器起来后才知道映射端口，故在此回调里现取并拼 DSN。
			SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
				host, port, err := mappedHostPort(ctx, c, "3306/tcp")
				if err != nil {
					return nil, err
				}
				dsn := fmt.Sprintf("root:pass@tcp(%s:%s)/shop?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true",
					host, port)
				return map[string]string{EnvKeyMySQLDSN: dsn}, nil
			},
		},
		{
			Name: "shop-redis",
			Start: func(ctx context.Context) (testcontainers.Container, error) {
				return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
					ContainerRequest: testcontainers.ContainerRequest{
						Name:         "shop-redis",
						Image:        "redis:8.2.3",
						ExposedPorts: []string{"6379/tcp"},
						WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
					},
					Started: true,
				})
			},
			SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
				host, port, err := mappedHostPort(ctx, c, "6379/tcp")
				if err != nil {
					return nil, err
				}
				return map[string]string{EnvKeyRedisAddr: fmt.Sprintf("%s:%s", host, port)}, nil
			},
		},
	}, nil
}

// initMySQL 建库 shop（SUT 启动必需）。注意只建库，不建测试用表——表交给 provision 跨进程幂等执行。
func initMySQL(ctx context.Context) error {
	host, port, err := mappedHostPortByName(ctx, "shop-mysql", "3306/tcp")
	if err != nil {
		return err
	}
	dsn := fmt.Sprintf("root:pass@tcp(%s:%s)/?multiStatements=true", host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `shop`")
	return err
}

// mappedHostPort 取容器某内部端口的宿主映射（启动后才可知，故在 SUTEnv / Init 里调用）。
func mappedHostPort(ctx context.Context, c testcontainers.Container, p nat.Port) (string, string, error) {
	host, err := c.Host(ctx)
	if err != nil {
		return "", "", err
	}
	mp, err := c.MappedPort(ctx, p)
	if err != nil {
		return "", "", err
	}
	return host, mp.Port(), nil
}

// mappedHostPortByName 在 Init 里据容器名查端口映射（Init 只拿到 ctx，故经 docker inspect 取）。
// 实现略：用 docker client ContainerInspect 读 NetworkSettings.Ports。
func mappedHostPortByName(ctx context.Context, name, portRef string) (string, string, error) {
	_ = ctx
	_ = name
	_ = portRef
	return "127.0.0.1", "3306", nil // 占位
}

// fixedHostPort 是“手动启动 SUT”模式的可选项：把容器端口写死（testcontainers 支持 HostConfigModifier
// 绑定固定 HostPort），这样 SUT 配置文件可直接照抄固定地址、不必每次从 runtime 读。代价是失去“随机端口
// 避免本地端口冲突”的好处，故仅手动模式按需采用。托管模式（run.go）保持随机端口最稳。
var _ = fmt.Sprintf // 占位：示意骨架
