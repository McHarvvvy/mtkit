package fixture

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	_ "github.com/go-sql-driver/mysql"
)

// RedisClient 是 PersistEnv 暴露的 Redis 客户端类型。
type RedisClient = redis.UniversalClient

// MongoClient 是 PersistEnv 暴露的 Mongo 客户端类型。
type MongoClient = *mongo.Client

// PersistEnv 为 Resource.Apply 提供测试基础设施连接，连接懒初始化并按 sutEnvKey 在进程内缓存复用。
//
// 刻意用具体 struct 而非 interface：唯一消费者 Resource.Apply 是真 I/O（对真实 testcontainers 跑 DDL），
// 没有可独立单测的纯逻辑，丢掉接口替换缝无实际损失；换来的是新增后端只需加方法——纯增量，
// 不破坏任何调用方，也没有 mock 实现要同步更新。
//
// sutEnvKey 是 RuntimeInfo.SUTEnv（mtkit.runtime.json 的 sut_env 字段）的 key，如 "MYSQL_DSN"，
// 不是容器名或库名；多容器、多库靠不同 key 区分。
type PersistEnv struct {
	sutEnv map[string]string

	// mu 串行化懒连接的创建。provision 本就被账本文件锁串行化，这里仅防同进程并发首连同一 key。
	mu    sync.Mutex
	mysql map[string]*sql.DB
	redis map[string]redis.UniversalClient
	mongo map[string]*mongo.Client
}

// NewPersistEnv 创建进程级连接池；sutEnv 即 RuntimeInfo.SUTEnv，构造时拷贝一份避免外部改动串入。
func NewPersistEnv(sutEnv map[string]string) *PersistEnv {
	copied := make(map[string]string, len(sutEnv))
	for k, v := range sutEnv {
		copied[k] = v
	}
	return &PersistEnv{
		sutEnv: copied,
		mysql:  make(map[string]*sql.DB),
		redis:  make(map[string]redis.UniversalClient),
		mongo:  make(map[string]*mongo.Client),
	}
}

// MySQL 返回 sutEnvKey 对应的 MySQL 连接（懒建并缓存）。
// 连接失败（key 缺失、DSN 有误、连接超时）按基建故障 panic，与 §1.7 fail-loud 一致。
func (e *PersistEnv) MySQL(sutEnvKey string) *sql.DB {
	e.mu.Lock()
	defer e.mu.Unlock()
	if db := e.mysql[sutEnvKey]; db != nil {
		return db
	}
	db, err := sql.Open("mysql", e.mustValue(sutEnvKey))
	if err != nil {
		panic(fmt.Sprintf("fixture: open mysql %s: %v", sutEnvKey, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// sql.Open 不实连，Ping 才能暴露 DSN/网络问题，故必须显式 Ping 一次再缓存。
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		panic(fmt.Sprintf("fixture: ping mysql %s: %v", sutEnvKey, err))
	}
	e.mysql[sutEnvKey] = db
	return db
}

// Redis 返回 sutEnvKey 对应的 Redis 客户端（懒建并缓存）。
func (e *PersistEnv) Redis(sutEnvKey string) redis.UniversalClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	if client := e.redis[sutEnvKey]; client != nil {
		return client
	}
	client := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{e.mustValue(sutEnvKey)}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		panic(fmt.Sprintf("fixture: ping redis %s: %v", sutEnvKey, err))
	}
	e.redis[sutEnvKey] = client
	return client
}

// Mongo 返回 sutEnvKey 对应的 Mongo 客户端（懒建并缓存）。
func (e *PersistEnv) Mongo(sutEnvKey string) *mongo.Client {
	e.mu.Lock()
	defer e.mu.Unlock()
	if client := e.mongo[sutEnvKey]; client != nil {
		return client
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(e.mustValue(sutEnvKey)))
	if err != nil {
		panic(fmt.Sprintf("fixture: connect mongo %s: %v", sutEnvKey, err))
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		panic(fmt.Sprintf("fixture: ping mongo %s: %v", sutEnvKey, err))
	}
	e.mongo[sutEnvKey] = client
	return client
}

func (e *PersistEnv) mustValue(sutEnvKey string) string {
	v := e.sutEnv[sutEnvKey]
	if v == "" {
		panic(fmt.Sprintf("fixture: sut env key %q is empty or missing", sutEnvKey))
	}
	return v
}
