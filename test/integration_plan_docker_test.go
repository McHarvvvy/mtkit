//go:build integration_plan

package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mtkit "github.com/McHarvvvy/mtkit"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TC-08: 成功接管测试并透传退出码
//
// 说明：调用 Run(m) 时，框架应确保所注册的真实容器已启动且可用。
// 最后将 m 的测试退出码如实向上透传。
//
// Given 构造合法 Config
// And   RegisterContainersFunc 注册一个真实 Redis 容器，
//
//	注册项 Name 为 "redis"，Start 中设置 ContainerRequest.Name 与之一致
//
// # defer/t.Cleanup 注册兜底清理
// When  调用 New() 后执行 Run(fakeM)
// Then  Run 阻塞执行
// And   fakeM.Run() 内部通过 Docker API 按容器名 "redis" inspect 获取映射端口，
//
//	并验证 Redis PING 连通性
//
// And   fakeM.Run() 返回 0 时 Run 返回 0；fakeM.Run() 返回 1 时 Run 返回 1
// ---------------------------------------------------------------------------

func TestTC08SuccessfulRunAndExitCodePassthrough(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	registerFn := singleRedisRegisterFunc("redis")

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	// 验证退出码 0 透传
	var verified bool
	code := inst.Run(&fakeRunnable{run: func() int {
		addr, err := redisAddressByContainerName(context.Background(), "redis")
		if err != nil {
			return 1
		}
		if err := pingRedis(addr); err != nil {
			return 1
		}
		verified = true
		return 0
	}})
	assert.Equal(t, 0, code, "Run should return 0 when fakeM returns 0")
	assert.True(t, verified, "Redis PING inside Run should succeed, confirming container is live")

	// 验证退出码 1 透传（第二次 Run 复用已有 daemon）
	inst2, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)
	code2 := inst2.Run(&fakeRunnable{run: func() int { return 1 }})
	assert.Equal(t, 1, code2, "Run should pass through exit code 1 from fakeM")
}

// ---------------------------------------------------------------------------
// TC-09: 连续执行测试框架复用底层基础设施
//
// 说明：调用方在多个连续的实例中多次调用框架，框架会自动复用已启动好的
// 底层资源容器群，防止重复消耗机器性能。
//
// Given 构造合法 Config（IdleTTL 设置足够长以覆盖两次 Run 间隔）
// And   RegisterContainersFunc 注册一个真实 Redis 容器
// # defer/t.Cleanup 注册兜底清理
// When  连续执行两次完全独立的 mtkit 实例的 Run(fakeM)
// Then  两次 Run 调用都成功返回 0
// And   两次 fakeM.Run() 内部通过 Docker API inspect 获取的容器 ID 相同
// And   两次 fakeM.Run() 内通过 mtruntime.Read() 获取的 daemon PID 相同
// ---------------------------------------------------------------------------

func TestTC09SequentialRunReusesSameContainer(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC09SequentialRunReusesSameContainer")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc09Config(runtimePath)
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	var id1, id2 string
	var pid1, pid2 int

	inst1, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)
	inst1.Run(&fakeRunnable{run: func() int {
		id1, _ = containerIDByName(context.Background(), "redis")
		info, _ := mtruntime.Read(runtimePath)
		pid1 = info.PID
		return 0
	}})

	inst2, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)
	inst2.Run(&fakeRunnable{run: func() int {
		id2, _ = containerIDByName(context.Background(), "redis")
		info, _ := mtruntime.Read(runtimePath)
		pid2 = info.PID
		return 0
	}})

	require.NotEmpty(t, id1)
	assert.Equal(t, id1, id2, "should reuse the same container")
	require.NotZero(t, pid1)
	assert.Equal(t, pid1, pid2, "should reuse the same daemon process")
}

// ---------------------------------------------------------------------------
// TC-10: 容器部分启动失败触发全量回滚
//
// 说明：在测试运行前的容器启动阶段如果有任意一个发生错误，
// 框架必须回滚所有已正常拉起的容器。
//
// Given 构造合法 Config
// And   RegisterContainersFunc 返回两个 ContainerRegistration：
//
//	注册项 A：Start 正常启动真实 Redis 容器（Name 为 "redis-good"）
//	注册项 B：Start 使用不存在的镜像，导致 testcontainers-go 报错
//
// # defer/t.Cleanup 注册兜底清理
// When  调用 Run(fakeM)
// Then  Run 返回 1
// And   fakeM.Run() 不会被调用
// And   Run 返回后单次验证容器 "redis-good" 已不存在（框架在 Run 返回前已完成回滚）
// ---------------------------------------------------------------------------

func TestTC10PartialStartFailureRollsBack(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC10PartialStartFailureRollsBack")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc10Config(runtimePath)
	t.Cleanup(func() { cleanupEnv("redis-good", runtimePath) })

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)

	var fakeRan bool
	code := inst.Run(&fakeRunnable{run: func() int {
		fakeRan = true
		return 0
	}})

	assert.Equal(t, 1, code, "Run should return 1 when a container fails to start")
	assert.False(t, fakeRan, "fakeM.Run() should not be called on startup failure")

	// 单次验证：框架在 Run 返回前已完成回滚
	exists, existsErr := containerExists(context.Background(), "redis-good")
	require.NoError(t, existsErr)
	assert.False(t, exists, "redis-good should be cleaned up before Run returns")
}

// ---------------------------------------------------------------------------
// TC-11: Init 函数失败触发全量回滚
//
// 说明：所有容器启动成功后，如果某个注册项的 Init 函数返回错误，
// 框架必须回滚所有已启动的容器。
//
// Given 构造合法 Config
// And   RegisterContainersFunc 返回两个 ContainerRegistration：
//
//	注册项 A：Start 正常启动真实 Redis 容器（Name 为 "redis-a"），Init 为 nil
//	注册项 B：Start 正常启动另一个真实 Redis 容器（Name 为 "redis-b"），
//	          Init 函数返回 error
//
// # defer/t.Cleanup 注册兜底清理
// When  调用 Run(fakeM)
// Then  Run 返回 1
// And   fakeM.Run() 不会被调用
// And   Run 返回后单次验证两个容器均已不存在（框架在 Run 返回前已完成回滚）
// ---------------------------------------------------------------------------

func TestTC11InitFailureRollsBackAll(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC11InitFailureRollsBackAll")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc11Config(runtimePath)
	t.Cleanup(func() {
		cleanupEnv("redis-a", runtimePath)
		_ = cleanupContainerByName(context.Background(), "redis-b")
	})

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)

	var fakeRan bool
	code := inst.Run(&fakeRunnable{run: func() int {
		fakeRan = true
		return 0
	}})

	assert.Equal(t, 1, code, "Run should return 1 when Init fails")
	assert.False(t, fakeRan, "fakeM.Run() should not be called on init failure")

	// 单次验证：框架在 Run 返回前已完成回滚
	exists1, _ := containerExists(context.Background(), "redis-a")
	assert.False(t, exists1, "redis-a should be cleaned up before Run returns")
	exists2, _ := containerExists(context.Background(), "redis-b")
	assert.False(t, exists2, "redis-b should be cleaned up before Run returns")
}

// ---------------------------------------------------------------------------
// TC-14: SUT 环境变量注入验证
//
// 说明：调用方在 GetCommand 中通过 Docker API 查询容器地址并设置 cmd.Env，
// 框架负责用该 cmd 启动 SUT 进程。SUT 进程读取环境变量 ping Redis，
// 写 "success"/"fail" 到文件，fakeM 轮询该文件验证结果。
//
// Given 构造合法 Config，启用 SUT 托管
// And   RegisterContainersFunc 注册一个真实 Redis 容器
// And   SUTBootPlan.GetCommand 通过 Docker API 查询容器地址，
//
//	设置 TEST_REDIS_ADDR=<实际地址> 到 cmd.Env
//
// And   SUT helper 进程读取 TEST_REDIS_ADDR，执行 Redis GET 命令，写 "success"/"fail" 到 envFile
// # defer/t.Cleanup 注册兜底清理
// When  调用 Run(fakeM)
// Then  Run 返回 0
// And   fakeM.Run() 内部轮询 envFile，验证内容为 "success"
// ---------------------------------------------------------------------------

func TestTC14SUTEnvInjectedIntoSUTProcess(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC14SUTEnvInjectedIntoSUTProcess")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc14Config(runtimePath)
	envFile := cfg.SUT.(sutEnvVerifySUT).envFile
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)

	var verified bool
	code := inst.Run(&fakeRunnable{run: func() int {
		// 轮询等待 SUT 写入结果
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			data, readErr := os.ReadFile(envFile)
			if readErr == nil && len(data) > 0 {
				content := strings.TrimSpace(string(data))
				if content == "success" {
					verified = true
					return 0
				}
				return 1 // "fail" 或其他内容
			}
			time.Sleep(200 * time.Millisecond)
		}
		return 1 // timeout
	}})
	assert.Equal(t, 0, code, "Run should return 0 when SUT env injection and Redis GET succeed")
	assert.True(t, verified, "SUT helper should write 'success' after Redis GET command")
}

// ---------------------------------------------------------------------------
// TC-15: SUT 就绪探测保证 fakeM.Run() 执行时端口已就绪
//
// 说明：SUT 开启服务探活时，框架承诺给到 m 实例的环境不仅拉起成功，
// 更是网络服务完成就绪的。进入 m.Run() 时目标探测端口必定已就绪可连。
//
// Given 构造包含启用真实 SUT 探活计划（探测某一端口）的合法 Config
// And   真实被测 SUT 进程模拟启动延迟：延迟耗时 300ms 后才绑定目标监听端口
// # defer/t.Cleanup 注册兜底清理
// When  调用 Run(fakeM)
// Then  fakeM.Run() 内部对 SUT 监听端口发起 TCP 连接，连接成功
// And   Run 正常返回 0
// ---------------------------------------------------------------------------

func TestTC15SUTProbeReadyBeforeFakeMRun(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC15SUTProbeReadyBeforeFakeMRun")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc15Config(runtimePath)
	probeAddr := cfg.SUT.(delayedProbeSUT).probeAddr
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)

	var probeOK bool
	code := inst.Run(&fakeRunnable{run: func() int {
		if probeErr := probeTCP(probeAddr, 2*time.Second); probeErr != nil {
			return 1
		}
		probeOK = true
		return 0
	}})
	assert.Equal(t, 0, code, "Run should return 0 after SUT probe passes")
	assert.True(t, probeOK, "TCP probe to SUT port should succeed before fakeM.Run() is entered")
}

// ---------------------------------------------------------------------------
// TC-16: Daemon 空闲退出
//
// 说明：当所有 lease 释放且无新请求时，daemon 应在 IdleTTL 超时后
// 自动退出并清理 runtime 文件。
//
// Given 构造合法 Config，Daemon.IdleTTL 设置为 1s
// And   RegisterContainersFunc 注册一个真实 Redis 容器
// # defer/t.Cleanup 注册兜底清理
// When  调用 Run(fakeM)，fakeM.Run() 记录 daemon PID 后立即返回 0
// Then  Run 返回 0
// And   在最多 10s 的容忍窗口内轮询断言：
//   - runtime.json 文件已被删除
//   - daemon 进程已退出（PID 不再存活）
//   - 容器已不存在
//
// ---------------------------------------------------------------------------

func TestTC16DaemonIdleExitCleansUp(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC16DaemonIdleExitCleansUp")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc16Config(runtimePath)
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	inst, err := mtkit.New(cfg, registerFn)
	require.NoError(t, err)

	var daemonPID int
	code := inst.Run(&fakeRunnable{run: func() int {
		info, readErr := mtruntime.Read(runtimePath)
		if readErr != nil {
			return 1
		}
		daemonPID = info.PID
		return 0
	}})
	assert.Equal(t, 0, code, "Run should return 0 before daemon idle recycle starts")
	require.NotZero(t, daemonPID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, statErr := os.Stat(runtimePath)
		runtimeGone := os.IsNotExist(statErr)
		exists, existsErr := containerExists(context.Background(), "redis")
		containerGone := existsErr == nil && !exists
		daemonGone := !processAlive(daemonPID)
		if runtimeGone && containerGone && daemonGone {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("daemon idle exit timeout: runtime, container, or daemon process still exists")
}

// ---------------------------------------------------------------------------
// TC-17: 多 client 并发 Acquire 复用同一容器
//
// 说明：go test ./... 场景下多个测试进程并发请求 daemon，所有请求应成功
// 获取 lease 并共享同一组容器。
//
// Given 构造合法 Config
// And   RegisterContainersFunc 注册一个真实 Redis 容器（Name 为 "redis"）
// # defer/t.Cleanup 注册兜底清理
// When  3 个独立 goroutine 并发执行 New() + Run(fakeM)
// Then  所有 Run 成功返回 0
// And   通过 Docker API inspect 容器 "redis"，验证只有一个 Redis 容器在运行
// ---------------------------------------------------------------------------

func TestTC17ConcurrentRunReusesSameContainer(t *testing.T) {
	requireDocker(t)
	t.Setenv("MT_SCENARIO", "TestTC17ConcurrentRunReusesSameContainer")
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	cfg, registerFn := tc17Config(runtimePath)
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	containerIDs := make(chan string, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, instErr := mtkit.New(cfg, registerFn)
			if instErr != nil {
				return
			}
			inst.Run(&fakeRunnable{run: func() int {
				id, _ := containerIDByName(context.Background(), "redis")
				containerIDs <- id
				return 0
			}})
		}()
	}
	wg.Wait()
	close(containerIDs)

	ids := make(map[string]struct{})
	for id := range containerIDs {
		ids[id] = struct{}{}
	}
	assert.Len(t, ids, 1, "all concurrent Runs should share the same container")
}
