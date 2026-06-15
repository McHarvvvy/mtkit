//go:build e2e

package fixture_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// 本文件是 PersistEnv / provision Apply 的真实环境 smoke：连真实容器、跑真实 DDL。仅在 -tags e2e 时编译。
// 缺 Docker 时容器启动失败即 t.Fatalf（fail 而非 skip——对齐既有 test/e2e 约定：e2e 经 tag 显式 opt-in，
// 缺依赖应 fail 以免假绿/腐烂）。运行：go test -tags e2e ./test/fixture/

// startContainer 起一个容器并注册清理；启动失败直接 fatal（含 Docker 不可用）。
func startContainer(t *testing.T, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("启动容器 %s 失败（Docker 不可用？）: %v", req.Image, err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	return c
}

// mappedHostPort 返回容器某端口映射到宿主的 host:port。
func mappedHostPort(t *testing.T, c testcontainers.Container, port string) string {
	t.Helper()
	ctx := context.Background()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	mapped, err := c.MappedPort(ctx, nat.Port(port))
	if err != nil {
		t.Fatalf("container mapped port %s: %v", port, err)
	}
	return net.JoinHostPort(host, mapped.Port())
}

// TestPersistEnvRedisConnectsAndReuses 守护 PersistEnv 对外的两条框架保证（D1/D2）：
// 真实连得上 + 同 key 复用同一连接；连接信息缺失则 fail-loud panic。
//
// 验证手段：起真 Redis 容器，NewPersistEnv 注入其地址。①Redis(key) 返回时（内部已 Ping）即证连通；
// ②同 key 二次取，断言返回**同一个**客户端实例（复用是框架明文承诺，身份相等是其唯一可观察形态）；
// ③取一个未注册的 key，断言 panic（基建故障 fail-loud，不返回可吞错误）。
func TestPersistEnvRedisConnectsAndReuses(t *testing.T) {
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp"),
	})
	addr := mappedHostPort(t, c, "6379/tcp")

	const key = "REDIS_ADDR"
	env := fixture.NewPersistEnv(map[string]string{key: addr})

	r1 := env.Redis(key) // 内部 Ping 通过才返回，到这里即证真实连通
	r2 := env.Redis(key)
	if r1 != r2 {
		t.Fatal("同一 key 两次取到不同的 Redis 客户端（复用失效）")
	}

	assertPanics(t, "取未注册的 key 应 fail-loud panic", func() {
		env.Redis("REDIS_MISSING")
	})
}

// TestProvisionMySQLTableCreatesTable 守护整条 provision 链路的**真实兑现**（D3 / C 的真实环境 smoke）：
// 经 Registry + PersistEnv 对真实 MySQL 跑 MySQLTable.Apply，表是否真的被建出来。
//
// 验证手段：起真 MySQL 容器；用真实文件账本 Registry + 指向该库的 PersistEnv 跑一次 Provision；然后
// **反向读** information_schema 确认目标表存在（前门式验证整条"建表→可见"链路，而非只断言 Apply 返回 nil）。
// 再对同 key EnsureOnce，断言 skipped 且不重复执行——跨进程幂等在真实库上的兑现。
func TestProvisionMySQLTableCreatesTable(t *testing.T) {
	const dbName = "mtkit_test"
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "mysql:8.0",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "secret",
			"MYSQL_DATABASE":      dbName,
		},
		// MySQL 初始化期会打印两次 "ready for connections"（init + 正式），取第二次为真就绪。
		WaitingFor: wait.ForLog("ready for connections").WithOccurrence(2).WithStartupTimeout(180 * time.Second),
	})
	addr := mappedHostPort(t, c, "3306/tcp")

	const envKey = "MYSQL_DSN"
	const resourceKey = "mysql.mtkit_test.users"
	dsn := "root:secret@tcp(" + addr + ")/" + dbName

	env := fixture.NewPersistEnv(map[string]string{envKey: dsn})
	reg := fixture.NewFileRegistry(filepath.Join(t.TempDir(), protocol.LedgerFileName))
	res := fixture.MySQLTable(envKey, resourceKey,
		`CREATE TABLE IF NOT EXISTS users (uid BIGINT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)

	ctx := context.Background()
	if err := fixture.Provision(ctx, reg, env, []fixture.Resource{res}); err != nil {
		t.Fatalf("Provision 失败: %v", err)
	}

	// 反向读：确认表真的存在于目标库。
	var name string
	err := env.MySQL(envKey).QueryRowContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
		dbName, "users").Scan(&name)
	if err != nil {
		t.Fatalf("表未建出（反向读 information_schema 失败）: %v", err)
	}

	// 幂等：同 key 再 EnsureOnce 必须 skip，且不重复执行 Apply。
	skipped, err := reg.EnsureOnce(ctx, resourceKey, func() error {
		t.Fatal("已 applied 的 key 不应再次执行 Apply")
		return nil
	})
	if err != nil {
		t.Fatalf("二次 EnsureOnce 出错: %v", err)
	}
	if !skipped {
		t.Fatal("二次 EnsureOnce 未跳过（跨进程幂等失效）")
	}
}

// assertPanics 断言 fn 会 panic，否则用 msg 报错。
func assertPanics(t *testing.T, msg string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s：期望 panic，但没有", msg)
		}
	}()
	fn()
}
