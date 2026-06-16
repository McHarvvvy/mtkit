//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/apache/pulsar-client-go/pulsaradmin/pkg/admin"
	padmincfg "github.com/apache/pulsar-client-go/pulsaradmin/pkg/admin/config"
	"github.com/docker/go-connections/nat"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/container"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// 本文件是 PersistEnv 全后端的真实环境 smoke。区别于"直接 new PersistEnv 测组件"：它走 mtkit 框架
// 完整装配链路——经 kit.Run 让 daemon 并发起 redis/mysql/mongo/pulsar/tdengine 五类真容器，组装出
// RuntimeInfo.SUTEnv 落盘 mtkit.runtime.json，再站在调用者角度从该文件读回 SUTEnv 构造 PersistEnv，
// 逐后端连接 + 读写 + 同 key 复用。MySQL 表经 WithSchema 注册，顺带覆盖 provisionRunnable 装配缝。
// 容器并发启动（container.Bundle，maxCreateConcurrency=4），故墙钟≈max(各启动)而非逐个相加。
//
// fail-loud（缺 key panic）是 PersistEnv 纯进程内行为，不需真容器，已剥离到
// client/fixture/persist_env_test.go，不在此重复。
// 运行：go test -tags e2e ./test/e2e/

const (
	persistDB          = "mtkit_e2e" // 复用为 MySQL / Mongo / TDengine 的库名（各自独立 server，同名无碍）
	persistMySQLKey    = "MYSQL_DSN"  // 与 persistSchemaResources 的 sutEnvKey 一致
	persistRedisKey    = "REDIS_ADDR"
	persistMongoKey    = "MONGO_URI"
	persistBrokerKey   = "PULSAR_BROKER_URL"
	persistAdminKey    = "PULSAR_ADMIN_URL"
	persistTDengineKey = "TDENGINE_DSN"

	persistMongoColl = "persist_env"   // provision 建出的 Mongo 集合
	persistPulsarNS  = "public/mtkit"  // provision 建出的 Pulsar namespace
	persistSeedKey   = "smoke:provisioned"
)

// TestE2E5_PersistEnvAllBackends 经真实装配链路验证 PersistEnv 五类后端：连接 + 读写 + 复用。
// 所有 schema（库/表/集合/namespace/种子）由 persistSchemaResources 经 WithSchema→provisionRunnable
// 在 m.Run 前真实建好；子测试只做纯读写、零 DDL。每后端一个子测试，互不掩盖、失败可定位。
func TestE2E5_PersistEnvAllBackends(t *testing.T) {
	requireDocker(t)
	name := uniqueName("e2e-persist")
	dir := t.TempDir()
	setupEnv(t, envOpts{
		runtimeDir: dir, container: "persist", containerName: name,
		schema: true, idleTTLSec: 90,
	})
	t.Cleanup(func() { cleanupPersist(name, dir) })

	code := mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		info, err := mtruntime.Read(runtimeFile(dir))
		if err != nil {
			t.Errorf("读取 runtime 文件失败: %v", err)
			return 1
		}
		// 真实调用者路径：从 daemon 落盘的 SUTEnv 构造 PersistEnv，而非手搓地址。
		env := fixture.NewPersistEnv(info.SUTEnv)

		t.Run("redis", func(t *testing.T) {
			r1 := env.Redis(persistRedisKey)
			if env.Redis(persistRedisKey) != r1 {
				t.Fatal("同一 key 两次取到不同 Redis 客户端（复用失效）")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// 先证 provision 种子已写入（证 redis 那条 Resource 真跑过），再做自身写读。
			seed, err := r1.Get(ctx, persistSeedKey).Result()
			if err != nil || seed != "1" {
				t.Fatalf("provision 种子缺失: got=%q err=%v", seed, err)
			}
			if err := r1.Set(ctx, "smoke:rw", "42", 0).Err(); err != nil {
				t.Fatalf("Redis set 失败: %v", err)
			}
			got, err := r1.Get(ctx, "smoke:rw").Result()
			if err != nil || got != "42" {
				t.Fatalf("Redis 反向读错误: got=%q err=%v", got, err)
			}
		})

		t.Run("mysql", func(t *testing.T) {
			// 库 + 表已由 provision 建好；直接写读全限定名 mtkit_e2e.users，验证整条装配链路兑现。
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			db := env.MySQL(persistMySQLKey)
			if _, err := db.ExecContext(ctx, "INSERT INTO "+persistDB+".users(uid) VALUES(?)", 42); err != nil {
				t.Fatalf("MySQL insert 失败: %v", err)
			}
			var uid int64
			if err := db.QueryRowContext(ctx, "SELECT uid FROM "+persistDB+".users WHERE uid = ?", 42).Scan(&uid); err != nil {
				t.Fatalf("MySQL 反向读失败: %v", err)
			}
			if uid != 42 {
				t.Fatalf("MySQL 反向读值错误: got %d, want 42", uid)
			}
		})

		t.Run("mongo", func(t *testing.T) {
			m1 := env.Mongo(persistMongoKey)
			if env.Mongo(persistMongoKey) != m1 {
				t.Fatal("同一 key 两次取到不同 Mongo 客户端（复用失效）")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// 集合已由 provision 建好；直接写读。
			coll := m1.Database(persistDB).Collection(persistMongoColl)
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
		})

		t.Run("pulsar", func(t *testing.T) {
			c1 := env.PulsarClient(persistBrokerKey)
			if env.PulsarClient(persistBrokerKey) != c1 {
				t.Fatal("同一 key 两次取到不同 Pulsar client（复用失效）")
			}
			// namespace 已由 provision 建好；topic 落在其下 produce/consume。
			topic := "persistent://" + persistPulsarNS + "/" + uniqueName("mtkit-e2e-pulsar")
			consumer := subscribePulsar(t, c1, topic, uniqueName("mtkit-e2e-sub"), 90*time.Second)
			defer consumer.Close()
			producer := createPulsarProducer(t, c1, topic, 90*time.Second)
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
			consumer.Ack(msg)
			if string(msg.Payload()) != string(payload) {
				t.Fatalf("Pulsar 收到 payload 错误: got %q, want %q", msg.Payload(), payload)
			}
		})

		t.Run("tdengine", func(t *testing.T) {
			db1 := env.TDengine(persistTDengineKey)
			if env.TDengine(persistTDengineKey) != db1 {
				t.Fatal("同一 key 两次取到不同 TDengine DB（复用失效）")
			}
			// 库 + 表已由 provision 建好；直接写读 mtkit_e2e.readings。
			ctx := context.Background()
			if _, err := db1.ExecContext(ctx, "INSERT INTO "+persistDB+".readings VALUES (NOW, 42)"); err != nil {
				t.Fatalf("TDengine insert 失败: %v", err)
			}
			var got int
			if err := db1.QueryRowContext(ctx, "SELECT reading_value FROM "+persistDB+".readings LIMIT 1").Scan(&got); err != nil {
				t.Fatalf("TDengine query 失败: %v", err)
			}
			if got != 42 {
				t.Fatalf("TDengine 反向读值错误: got %d, want 42", got)
			}
		})

		return 0
	}})

	if code != 0 {
		t.Fatalf("PersistEnv 全后端 e2e 失败 code=%d", code)
	}
}

// funcResource 是 e2e 测试内的通用 Resource 实现：provision 语义全在 apply 闭包里。
// mtkit 框架不预设任何 Resource 类型（见 client/fixture/resource.go），使用方按需自定义——此即一例。
type funcResource struct {
	key   string
	apply func(ctx context.Context, env *fixture.PersistEnv) error
}

func (r funcResource) Key() string { return r.key }

func (r funcResource) Apply(ctx context.Context, env *fixture.PersistEnv) error {
	return r.apply(ctx, env)
}

// persistSchemaResources 声明 persist e2e 的全部前置 schema，经 WithSchema 在 m.Run 前真实建立。
// 顺序即执行顺序（Provision 按 slice 遍历），故"建库"排在"建表"前；Apply 全幂等以容忍 daemon 复用与重入。
func persistSchemaResources() []fixture.Resource {
	return []fixture.Resource{
		// MySQL：DSN 不选库，故先建库，再建全限定名的表。
		funcResource{key: "mysql.persist.db", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			_, err := env.MySQL(persistMySQLKey).ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+persistDB+"`")
			return err
		}},
		funcResource{key: "mysql." + persistDB + ".users", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			_, err := env.MySQL(persistMySQLKey).ExecContext(ctx,
				"CREATE TABLE IF NOT EXISTS "+persistDB+".users (uid BIGINT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
			return err
		}},
		// Redis 无 schema：种子一个 key 让 provision 可观察（子测试反向读验证 Resource 真跑过）。
		funcResource{key: "redis.persist.seed", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			return env.Redis(persistRedisKey).Set(ctx, persistSeedKey, "1", 0).Err()
		}},
		// Mongo：建集合；已存在（NamespaceExists=48）视为成功以幂等。
		funcResource{key: "mongo.persist.coll", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			err := env.Mongo(persistMongoKey).Database(persistDB).CreateCollection(ctx, persistMongoColl)
			var cmdErr mongo.CommandError
			if errors.As(err, &cmdErr) && cmdErr.Code == 48 {
				return nil
			}
			return err
		}},
		// Pulsar：list-then-create namespace，避免脆弱的 409 错误串匹配。
		funcResource{key: "pulsar.persist.ns", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			tenant := strings.SplitN(persistPulsarNS, "/", 2)[0]
			ns := env.PulsarAdmin(persistAdminKey).Namespaces()
			existing, err := ns.GetNamespaces(tenant)
			if err != nil {
				return err
			}
			for _, n := range existing {
				if n == persistPulsarNS {
					return nil
				}
			}
			return ns.CreateNamespace(persistPulsarNS)
		}},
		// TDengine：先建库，再建全限定名的表。
		funcResource{key: "tdengine.persist.db", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			_, err := env.TDengine(persistTDengineKey).ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+persistDB)
			return err
		}},
		funcResource{key: "tdengine." + persistDB + ".readings", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
			_, err := env.TDengine(persistTDengineKey).ExecContext(ctx,
				"CREATE TABLE IF NOT EXISTS "+persistDB+".readings (ts TIMESTAMP, reading_value INT)")
			return err
		}},
	}
}

// registerPersistContainers 注册 PersistEnv 全后端冒烟所需的五类真容器。
//
// 各容器相互独立（无启动依赖），由框架并发启动；需"应用级真就绪"（超出端口监听）的 MySQL/Pulsar/
// TDengine 把就绪探测放在 Init——Init 在所有容器起完后、Acquire（及 provision）之前串行执行，
// 经闭包捕获容器句柄拿映射端口。SUTEnv 回调在启动后组装连接串（映射端口启动前不可知）。
func registerPersistContainers(name string) []container.ContainerRegistration {
	var mysqlC, pulsarC, tdengineC testcontainers.Container

	redis := container.ContainerRegistration{
		Name: "redis",
		Start: genericStart(testcontainers.ContainerRequest{
			Name:         name + "-redis",
			Image:        "redis:8.2.3",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		}, nil),
		SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
			addr, err := hostPort(ctx, c, "6379/tcp")
			if err != nil {
				return nil, err
			}
			return map[string]string{persistRedisKey: addr}, nil
		},
	}

	mysql := container.ContainerRegistration{
		Name: "mysql",
		Start: genericStart(testcontainers.ContainerRequest{
			Name:  name + "-mysql",
			Image: "mysql:8.0.42",
			// 不设 MYSQL_DATABASE：库由 provision 的 Resource 建（DSN 不选库），schema 全量经 provision 装配。
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "secret"},
			ExposedPorts: []string{"3306/tcp"},
			// MySQL 初始化期打印两次 "ready for connections"（init + 正式），取第二次为真就绪。
			WaitingFor: wait.ForLog("ready for connections").WithOccurrence(2).WithStartupTimeout(180 * time.Second),
		}, &mysqlC),
		// 官方镜像初始化期会先起临时 bootstrap server 再重启为正式 server，日志计数可能命中重启窗口；
		// provision 是 fail-loud，故 Init 先反复 Ping 直到正式 server 稳定，确保 provision 那次 Ping 必中。
		Init: func(ctx context.Context) error {
			dsn, err := mysqlDSN(ctx, mysqlC)
			if err != nil {
				return err
			}
			return pingSQLUntilReady("mysql", dsn, 90*time.Second)
		},
		SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
			dsn, err := mysqlDSN(ctx, c)
			if err != nil {
				return nil, err
			}
			return map[string]string{persistMySQLKey: dsn}, nil
		},
	}

	mongo := container.ContainerRegistration{
		Name: "mongo",
		Start: genericStart(testcontainers.ContainerRequest{
			Name:         name + "-mongo",
			Image:        "mongo:5.0.20",
			ExposedPorts: []string{"27017/tcp"},
			WaitingFor:   wait.ForListeningPort("27017/tcp").WithStartupTimeout(90 * time.Second),
		}, nil),
		SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
			addr, err := hostPort(ctx, c, "27017/tcp")
			if err != nil {
				return nil, err
			}
			return map[string]string{persistMongoKey: "mongodb://" + addr + "/?directConnection=true"}, nil
		},
	}

	pulsar := container.ContainerRegistration{
		Name: "pulsar",
		Start: genericStart(testcontainers.ContainerRequest{
			Name:         name + "-pulsar",
			Image:        "apachepulsar/pulsar:3.0.2",
			ExposedPorts: []string{"6650/tcp", "8080/tcp"},
			Cmd:          []string{"bin/pulsar", "standalone", "-nfw"},
			Env:          map[string]string{"PULSAR_MEM": "-Xms256m -Xmx256m -XX:MaxDirectMemorySize=128m"},
			WaitingFor:   wait.ForListeningPort("8080/tcp").WithStartupTimeout(240 * time.Second),
		}, &pulsarC),
		// 端口通 ≠ broker/namespace 就绪；Init 探 admin REST 至 public/default 出现再放行。
		Init: func(ctx context.Context) error {
			adminAddr, err := hostPort(ctx, pulsarC, "8080/tcp")
			if err != nil {
				return err
			}
			return pulsarAdminUntilReady("http://"+adminAddr, 90*time.Second)
		},
		SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
			broker, err := hostPort(ctx, c, "6650/tcp")
			if err != nil {
				return nil, err
			}
			adminAddr, err := hostPort(ctx, c, "8080/tcp")
			if err != nil {
				return nil, err
			}
			return map[string]string{
				persistBrokerKey: "pulsar://" + broker,
				persistAdminKey:  "http://" + adminAddr,
			}, nil
		},
	}

	tdengine := container.ContainerRegistration{
		Name: "tdengine",
		Start: genericStart(testcontainers.ContainerRequest{
			Name:         name + "-tdengine",
			Image:        "tdengine/tdengine:3.3.0.0",
			ExposedPorts: []string{"6041/tcp"},
			WaitingFor:   wait.ForListeningPort("6041/tcp").WithStartupTimeout(180 * time.Second),
		}, &tdengineC),
		Init: func(ctx context.Context) error {
			addr, err := hostPort(ctx, tdengineC, "6041/tcp")
			if err != nil {
				return err
			}
			return pingSQLUntilReady("taosWS", "root:taosdata@ws("+addr+")/", 90*time.Second)
		},
		SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
			addr, err := hostPort(ctx, c, "6041/tcp")
			if err != nil {
				return nil, err
			}
			return map[string]string{persistTDengineKey: "root:taosdata@ws(" + addr + ")/"}, nil
		},
	}

	return []container.ContainerRegistration{redis, mysql, mongo, pulsar, tdengine}
}

// genericStart 返回启动 req 的 Start 闭包；out 非空时把容器句柄回写，供同注册项的 Init 探就绪用。
func genericStart(req testcontainers.ContainerRequest, out *testcontainers.Container) func(context.Context) (testcontainers.Container, error) {
	return func(ctx context.Context) (testcontainers.Container, error) {
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			return nil, err
		}
		if out != nil {
			*out = c
		}
		return c, nil
	}
}

// hostPort 返回容器某端口映射到宿主的 host:port。
func hostPort(ctx context.Context, c testcontainers.Container, port string) (string, error) {
	host, err := c.Host(ctx)
	if err != nil {
		return "", err
	}
	mapped, err := c.MappedPort(ctx, nat.Port(port))
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, mapped.Port()), nil
}

// mysqlDSN 据容器映射端口组装 DSN，刻意不选库（库由 provision 的 Resource 建，读写用全限定名）。
func mysqlDSN(ctx context.Context, c testcontainers.Container) (string, error) {
	addr, err := hostPort(ctx, c, "3306/tcp")
	if err != nil {
		return "", err
	}
	return "root:secret@tcp(" + addr + ")/", nil
}

// pingSQLUntilReady 反复 Ping 直到 database/sql 后端真就绪或超时（吸收初始化重启窗口）。
// 在 daemon 的 Init 内运行（无 *testing.T），故返回 error 由框架决定回滚。
func pingSQLUntilReady(driver, dsn string, timeout time.Duration) error {
	if driver == "mysql" {
		// 探测期反复连尚在重启的 server，go-sql-driver 默认把每次失败打成 "unexpected EOF"——
		// 预期瞬态噪音，静音以免误导 daemon 日志。
		_ = mysqldriver.SetLogger(log.New(io.Discard, "", 0))
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s 在 %s 内未就绪: %w", driver, timeout, lastErr)
}

// pulsarAdminUntilReady 探 admin REST 至 public/default namespace 出现，证明 broker 真就绪。
func pulsarAdminUntilReady(adminURL string, timeout time.Duration) error {
	cli, err := admin.New(&padmincfg.Config{WebServiceURL: adminURL})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		namespaces, err := cli.Namespaces().GetNamespaces("public")
		if err == nil {
			for _, ns := range namespaces {
				if ns == "public/default" {
					return nil
				}
			}
			lastErr = fmt.Errorf("public/default namespace not found: %v", namespaces)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("pulsar admin 在 %s 内未就绪: %w", timeout, lastErr)
}

// subscribePulsar 重试订阅直到成功或超时（broker 就绪后 topic/订阅仍可能短暂未备好）。
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

// createPulsarProducer 重试创建 producer 直到成功或超时。
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

// cleanupPersist 兜底清理：终止 daemon、强删五类容器、移除 runtime 产物（防 daemon 被硬杀后残留）。
func cleanupPersist(name, dir string) {
	killDaemon(dir)
	for _, suffix := range []string{"-redis", "-mysql", "-mongo", "-pulsar", "-tdengine"} {
		removeContainer(name + suffix)
	}
	_ = os.Remove(runtimeFile(dir))
	_ = os.RemoveAll(mtruntime.RunnerDir(runtimeFile(dir)))
}
