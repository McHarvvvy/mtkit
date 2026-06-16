//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/pulsar-client-go/pulsar"
	mysqldriver "github.com/go-sql-driver/mysql"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// 本文件是 PersistEnv / provision Apply 的真实环境 smoke：连真实容器、跑真实 DDL。仅在 -tags e2e 时编译，
// 与其它 e2e 一样 requireDocker（缺 Docker fail 而非 skip——e2e 经 tag 显式 opt-in，缺依赖应 fail 以免假绿）。
// 运行：go test -tags e2e ./test/e2e/

// startContainer 起一个容器并注册清理；启动失败直接 fatal。
func startContainer(t *testing.T, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("启动容器 %s 失败: %v", req.Image, err)
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
// ②同 key 二次取，断言返回**同一个**客户端实例；③取一个未注册的 key，断言 panic（基建故障 fail-loud）。
//
// 关于 ② 的相等比较：env.Redis 返回 redis.UniversalClient（interface），其动态类型是指针（*redis.Client），
// interface 持有可比较（指针型）动态值时 `==` 合法且零 panic 风险——比较的是 类型+指针。"返回同一缓存实例"
// 是"连接复用"这条框架承诺唯一的可观察形态，故用身份相等断言之，而非咬任何内部字段。
func TestPersistEnvRedisConnectsAndReuses(t *testing.T) {
	requireDocker(t)
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "redis:8.2.3",
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
// 验证手段：起真 MySQL 容器（容器 env MYSQL_DATABASE 令官方镜像初始化时建好 mtkit_test 库，DSN 直接选它，
// 故 CREATE TABLE 有库可建、不会报错）；用真实文件账本 Registry + 指向该库的 PersistEnv 跑一次 Provision；
// 然后**反向读** information_schema 确认目标表存在——前门式验证整条"建表→可见"链路，而非只断言 Apply 返回 nil。
// （幂等"同 key 只执行一次"已由 test/fixture/provision_lock_test.go 的文件锁用例覆盖，此处不再重复断言。）
func TestProvisionMySQLTableCreatesTable(t *testing.T) {
	requireDocker(t)
	const dbName = "mtkit_test"
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "mysql:8.0.42",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "secret",
			"MYSQL_DATABASE":      dbName, // 初始化时建库；DSN 选它，DDL 有库可落
		},
		// MySQL 初始化期会打印两次 "ready for connections"（init + 正式），取第二次为真就绪。
		WaitingFor: wait.ForLog("ready for connections").WithOccurrence(2).WithStartupTimeout(180 * time.Second),
	})
	addr := mappedHostPort(t, c, "3306/tcp")

	const envKey = "MYSQL_DSN"
	const resourceKey = "mysql.mtkit_test.users"
	dsn := "root:secret@tcp(" + addr + ")/" + dbName

	// 主动就绪探测：MySQL 官方镜像初始化期会先起临时 bootstrap server 建库、再重启为正式 server，
	// 两次都打印 "ready for connections"，靠日志计数可能命中两者之间的重启窗口（首连被 EOF/bad connection）。
	// PersistEnv.MySQL 是 fail-loud（单次 Ping 失败即 panic），故这里先用 database/sql 反复 Ping 直到真就绪，
	// 确保后续 env.MySQL 的那一次 Ping 必然落在已稳定的正式 server 上。
	waitMySQLReady(t, dsn, 90*time.Second)

	env := fixture.NewPersistEnv(map[string]string{envKey: dsn})
	reg := fixture.NewFileRegistry(filepath.Join(t.TempDir(), protocol.LedgerFileName))
	res := fixture.MySQLTable(envKey, resourceKey,
		`CREATE TABLE IF NOT EXISTS users (uid BIGINT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)

	ctx := context.Background()
	if err := fixture.Provision(ctx, reg, env, []fixture.Resource{res}); err != nil {
		t.Fatalf("Provision 失败: %v", err)
	}

	// 反向读：确认表真的存在于目标库——证明 Apply 真把表建出来了，而非仅返回 nil。
	var name string
	err := env.MySQL(envKey).QueryRowContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
		dbName, "users").Scan(&name)
	if err != nil {
		t.Fatalf("表未建出（反向读 information_schema 失败）: %v", err)
	}
}

// TestPersistEnvMongoConnectsWritesAndReuses 守护 Mongo 的 PersistEnv 真实连接能力：
// Ping 成功后复用同一 *mongo.Client，并对真实 MongoDB 做一次 insert/find 反向读。
func TestPersistEnvMongoConnectsWritesAndReuses(t *testing.T) {
	requireDocker(t)
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "mongo:5.0.20",
		ExposedPorts: []string{"27017/tcp"},
		WaitingFor:   wait.ForListeningPort("27017/tcp").WithStartupTimeout(90 * time.Second),
	})
	addr := mappedHostPort(t, c, "27017/tcp")

	const key = "MONGO_URI"
	uri := "mongodb://" + addr + "/?directConnection=true"
	env := fixture.NewPersistEnv(map[string]string{key: uri})

	m1 := env.Mongo(key)
	m2 := env.Mongo(key)
	if m1 != m2 {
		t.Fatal("同一 key 两次取到不同的 Mongo 客户端（复用失效）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	coll := m1.Database("mtkit_e2e").Collection("persist_env")
	if _, err := coll.InsertOne(ctx, bson.M{"_id": "mongo-smoke", "value": 42}); err != nil {
		t.Fatalf("Mongo insert 失败: %v", err)
	}
	var got struct {
		Value int `bson:"value"`
	}
	if err := coll.FindOne(ctx, bson.M{"_id": "mongo-smoke"}).Decode(&got); err != nil {
		t.Fatalf("Mongo find 失败: %v", err)
	}
	if got.Value != 42 {
		t.Fatalf("Mongo 反向读值错误: got %d, want 42", got.Value)
	}

	assertPanics(t, "取未注册的 Mongo key 应 fail-loud panic", func() {
		env.Mongo("MONGO_MISSING")
	})
}

// TestPersistEnvPulsarProducesAndConsumes 守护 Pulsar 双端点 PersistEnv：
// Admin REST 真实可达，Broker client 能真实 subscribe/produce/receive，且同 key client 复用。
func TestPersistEnvPulsarProducesAndConsumes(t *testing.T) {
	requireDocker(t)
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "apachepulsar/pulsar:3.0.2",
		ExposedPorts: []string{"6650/tcp", "8080/tcp"},
		Cmd:          []string{"bin/pulsar", "standalone", "-nfw"},
		Env: map[string]string{
			"PULSAR_MEM": "-Xms256m -Xmx256m -XX:MaxDirectMemorySize=128m",
		},
		WaitingFor: wait.ForListeningPort("8080/tcp").WithStartupTimeout(240 * time.Second),
	})
	brokerAddr := mappedHostPort(t, c, "6650/tcp")
	adminAddr := mappedHostPort(t, c, "8080/tcp")

	const brokerKey = "PULSAR_BROKER_URL"
	const adminKey = "PULSAR_ADMIN_URL"
	env := fixture.NewPersistEnv(map[string]string{
		brokerKey: "pulsar://" + brokerAddr,
		adminKey:  "http://" + adminAddr,
	})
	waitPulsarAdminReady(t, env, adminKey, 90*time.Second)

	client1 := env.PulsarClient(brokerKey)
	client2 := env.PulsarClient(brokerKey)
	if client1 != client2 {
		t.Fatal("同一 key 两次取到不同的 Pulsar client（复用失效）")
	}

	topic := "persistent://public/default/" + uniqueName("mtkit-e2e-pulsar")
	consumer := subscribePulsar(t, client1, topic, uniqueName("mtkit-e2e-sub"), 90*time.Second)
	defer consumer.Close()

	producer := createPulsarProducer(t, client1, topic, 90*time.Second)
	defer producer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := []byte("mtkit-pulsar-smoke")
	if _, err := producer.Send(ctx, &pulsar.ProducerMessage{Payload: payload}); err != nil {
		t.Fatalf("Pulsar send 失败: %v", err)
	}
	msg, err := consumer.Receive(ctx)
	if err != nil {
		t.Fatalf("Pulsar receive 失败: %v", err)
	}
	defer consumer.Ack(msg)
	if string(msg.Payload()) != string(payload) {
		t.Fatalf("Pulsar 收到 payload 错误: got %q, want %q", msg.Payload(), payload)
	}

	assertPanics(t, "取未注册的 Pulsar broker key 应 fail-loud panic", func() {
		env.PulsarClient("PULSAR_MISSING")
	})
}

// TestPersistEnvTDengineExecutesDDLAndQuery 守护 TDengine websocket driver：
// PersistEnv.TDengine 能 Ping、复用同一 *sql.DB，并对真实 TDengine 跑 DDL/insert/query。
func TestPersistEnvTDengineExecutesDDLAndQuery(t *testing.T) {
	requireDocker(t)
	c := startContainer(t, testcontainers.ContainerRequest{
		Image:        "tdengine/tdengine:3.3.0.0",
		ExposedPorts: []string{"6041/tcp"},
		WaitingFor:   wait.ForListeningPort("6041/tcp").WithStartupTimeout(180 * time.Second),
	})
	addr := mappedHostPort(t, c, "6041/tcp")

	const key = "TDENGINE_DSN"
	dsn := "root:taosdata@ws(" + addr + ")/"
	waitTDengineReady(t, dsn, 90*time.Second)

	env := fixture.NewPersistEnv(map[string]string{key: dsn})
	db1 := env.TDengine(key)
	db2 := env.TDengine(key)
	if db1 != db2 {
		t.Fatal("同一 key 两次取到不同的 TDengine DB（复用失效）")
	}

	ctx := context.Background()
	dbName := fmt.Sprintf("mtkit_e2e_%d", time.Now().UnixNano())
	if _, err := db1.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+dbName); err != nil {
		t.Fatalf("TDengine create database 失败: %v", err)
	}
	if _, err := db1.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+dbName+".readings (ts TIMESTAMP, reading_value INT)"); err != nil {
		t.Fatalf("TDengine create table 失败: %v", err)
	}
	if _, err := db1.ExecContext(ctx, "INSERT INTO "+dbName+".readings VALUES (NOW, 42)"); err != nil {
		t.Fatalf("TDengine insert 失败: %v", err)
	}
	var got int
	if err := db1.QueryRowContext(ctx, "SELECT reading_value FROM "+dbName+".readings LIMIT 1").Scan(&got); err != nil {
		t.Fatalf("TDengine query 失败: %v", err)
	}
	if got != 42 {
		t.Fatalf("TDengine 反向读值错误: got %d, want 42", got)
	}

	assertPanics(t, "取未注册的 TDengine key 应 fail-loud panic", func() {
		env.TDengine("TDENGINE_MISSING")
	})
}

// waitMySQLReady 反复 Ping 直到 MySQL 正式 server 真正可连或超时——吸收初始化期的 bootstrap→正式 重启窗口。
// mysql 驱动由 client/fixture 的 blank import 全局注册，故这里 database/sql 可直接用 "mysql"。
func waitMySQLReady(t *testing.T, dsn string, timeout time.Duration) {
	t.Helper()
	// 探测期会反复连到尚在重启的 server，go-sql-driver 默认会把每次失败打成 "unexpected EOF"——
	// 那是预期的瞬态噪音，静音掉以免误导（探测成功后照常）。
	_ = mysqldriver.SetLogger(log.New(io.Discard, "", 0))

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer db.Close()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("MySQL 在 %s 内未就绪: %v", timeout, lastErr)
}

func waitTDengineReady(t *testing.T, dsn string, timeout time.Duration) {
	t.Helper()
	db, err := sql.Open("taosWS", dsn)
	if err != nil {
		t.Fatalf("open tdengine: %v", err)
	}
	defer db.Close()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("TDengine 在 %s 内未就绪: %v", timeout, lastErr)
}

func waitPulsarAdminReady(t *testing.T, env *fixture.PersistEnv, adminKey string, timeout time.Duration) {
	t.Helper()
	admin := env.PulsarAdmin(adminKey)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		namespaces, err := admin.Namespaces().GetNamespaces("public")
		if err == nil {
			for _, ns := range namespaces {
				if ns == "public/default" {
					return
				}
			}
			lastErr = fmt.Errorf("public/default namespace not found: %v", namespaces)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Pulsar admin 在 %s 内未就绪: %v", timeout, lastErr)
}

func subscribePulsar(t *testing.T, client pulsar.Client, topic, subscription string, timeout time.Duration) pulsar.Consumer {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		consumer, err := client.Subscribe(pulsar.ConsumerOptions{
			Topic:            topic,
			SubscriptionName: subscription,
			Type:             pulsar.Exclusive,
		})
		if err == nil {
			return consumer
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	t.Fatalf("Pulsar subscribe 在 %s 内未成功: %v", timeout, lastErr)
	return nil
}

func createPulsarProducer(t *testing.T, client pulsar.Client, topic string, timeout time.Duration) pulsar.Producer {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		producer, err := client.CreateProducer(pulsar.ProducerOptions{Topic: topic})
		if err == nil {
			return producer
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	t.Fatalf("Pulsar producer 在 %s 内未成功: %v", timeout, lastErr)
	return nil
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
