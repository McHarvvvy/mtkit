package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
	"github.com/McHarvvvy/mtkit/container"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/McHarvvvy/mtkit/sut"
)

// Config 表示守护进程配置。
type Config struct {
	// Addr 指定 daemon 监听地址，支持 127.0.0.1:0 自动分配端口。
	Addr string
	// Token 指定 daemon 鉴权令牌，客户端请求需携带相同值。
	Token string
	// Project 用于标识当前测试项目，便于日志与运行时隔离。
	Project string
	// RuntimePath 指定 runtime 发现文件路径，供客户端发现 daemon。
	RuntimePath string
	RuntimeDir  string
	// IdleTTL 指定 daemon 在无活跃租约时的自动退出时间。
	IdleTTL time.Duration
	// Faked 定义 faked 内嵌引擎配置。
	Faked mtfaked.Config
	// FakedEnabled 指定是否启用内嵌 faked 引擎。
	FakedEnabled bool
	// SUT 定义被测服务启动计划与生命周期策略。
	SUTConfig sut.Config
	SUT       sut.SUTBootPlan
}

// Daemon 负责管理 mtkit 测试基础设施（容器 / SUT / 租约）的生命周期。
type Daemon struct {
	cfg         Config
	regs        []container.ContainerRegistration
	bundle      *container.Bundle
	store       *leaseStore
	httpServer  *http.Server
	startedAt   time.Time
	runtimePath string
	runtimeInfo mtruntime.RuntimeInfo // Start 写入的基线，faked/sut env 就绪后追加段重写
	faked       *mtfaked.Server       // nil 表示 faked 未启用
	fakedEnv    map[string]string     // ensureFakedStarted 暂存的 faked 注入 env
	sutEnvReady bool                  // prepareSUTEnv 是否已组装并落盘（幂等）
	sut         *sut.Manager

	startMu      sync.Mutex
	infraStarted bool

	shutdownOnce sync.Once
	done         chan struct{}

	// acquireInFlight 用于抑制空闲回收误判：
	// 请求还在执行中时，即使 active lease 为 0 也不能触发退出。
	acquireInFlight atomic.Int32
}

// New 创建守护进程实例。
func New(cfg Config, regs []container.ContainerRegistration) (*Daemon, error) {
	if cfg.Addr == "" {
		// 关键决策：默认使用随机可用端口，避免本地并发测试或残留进程导致固定端口冲突。
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Token == "" {
		cfg.Token = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 15 * time.Second
	}
	if cfg.RuntimePath == "" {
		cfg.RuntimePath = mtruntime.DefaultRuntimePath("default")
	}
	if strings.TrimSpace(cfg.Project) == "" {
		cfg.Project = "default"
	}
	bundle, err := container.NewBundle(regs)
	if err != nil {
		return nil, err
	}
	var fakedServer *mtfaked.Server
	if cfg.FakedEnabled {
		fakedServer, err = mtfaked.New(cfg.Faked)
		if err != nil {
			return nil, err
		}
	}
	// 注入 SUT 的 env（容器 + faked 连接信息）在容器/引擎 Start 后才已知，故 sutInput 此处不含——
	// 由 prepareSUTEnv 组装后经 sut.Manager.SetInjectEnv 在 SUT 启动前填入（见 lifecycle.go）。
	sutInput := sut.StartSUTInput{
		Project:     cfg.Project,
		RuntimePath: cfg.RuntimePath,
		RuntimeDir:  cfg.RuntimeDir,
	}
	return &Daemon{
		cfg:         cfg,
		regs:        regs,
		bundle:      bundle,
		store:       newLeaseStore(),
		startedAt:   time.Now(),
		runtimePath: cfg.RuntimePath,
		faked:       fakedServer,
		sut:         sut.NewManager(cfg.SUTConfig, cfg.SUT, sutInput),
		done:        make(chan struct{}),
	}, nil
}

// Start 启动守护进程并阻塞直到退出。
func (d *Daemon) Start(ctx context.Context) error {
	logFile, err := mtruntime.OpenDaemonLogFile(d.runtimePath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	if err := container.CleanupOrphans(ctx, d.regs); err != nil {
		return err
	}

	mux := http.NewServeMux()
	d.registerLeaseRoutes(mux)
	if d.faked != nil {
		// faked 内嵌：rule 注册 handler 挂 daemon 主 mux（与 lease 共用端口），必须在 Serve 之前。
		// 无鉴权——与原独立 faked control 一致（仅面向本机测试进程）。
		d.faked.RegisterControl(mux)
	}
	// 段3 预留：fixtured 协调路由在此追加，与 lease 共用同一 mux / 端口 / 鉴权：
	//   d.fixtured.RegisterRoutes(mux, d.withAuth)

	ln, err := net.Listen(mtconstant.ProtocolTCP, d.cfg.Addr)
	if err != nil {
		return err
	}
	d.httpServer = &http.Server{Handler: mux}

	d.runtimeInfo = mtruntime.RuntimeInfo{
		Addr:      ln.Addr().String(),
		Token:     d.cfg.Token,
		PID:       os.Getpid(),
		StartedAt: d.startedAt,
	}
	if err = mtruntime.Write(d.runtimePath, d.runtimeInfo); err != nil {
		_ = ln.Close()
		return err
	}
	// 关键决策：无论是正常关闭还是 Serve 异常返回，都在退出路径兜底清理 runtime 文件。
	// shutdownServer 中也会执行同样清理，这里再次兜底可覆盖异常分支并保持幂等。
	defer mtruntime.Remove(d.runtimePath)

	go d.loopReaper()
	go func() {
		<-ctx.Done()
		d.shutdownServer(context.Background())
	}()

	err = d.httpServer.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// registerLeaseRoutes 把 lease 控制面路由挂到 daemon 主 mux（统一经 withAuth 鉴权）。
// 路由装配独立成方法，便于段3 让 fixtured 把协调路由挂到同一 mux / 端口。
func (d *Daemon) registerLeaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc(protocol.PathAcquire, d.withAuth(d.handleAcquire))
	mux.HandleFunc(protocol.PathHeartbeat, d.withAuth(d.handleHeartbeat))
	mux.HandleFunc(protocol.PathRelease, d.withAuth(d.handleRelease))
	mux.HandleFunc(protocol.PathState, d.withAuth(d.handleState))
}

func (d *Daemon) withAuth(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// daemon 仅面向本机测试进程，但仍通过 token 做最小隔离，
		// 防止同机其它进程误调用导致租约状态错乱。
		token := strings.TrimSpace(r.Header.Get(protocol.HeaderToken))
		if token == "" || token != d.cfg.Token {
			writeError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (d *Daemon) handleAcquire(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	d.acquireInFlight.Add(1)
	defer d.acquireInFlight.Add(-1)
	var req protocol.AcquireReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("mtkit acquire decode request failed: %v", err)
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	// 关键决策：Acquire 串联“容器基础设施就绪 + 被测服务进程就绪”，
	// 只有两者都完成才返回租约，确保用例拿到的是可用环境而非半初始化状态。
	infraStart := time.Now()
	if err := d.ensureInfraStarted(r.Context()); err != nil {
		log.Printf("mtkit acquire ensure infra failed cost=%s err=%v", time.Since(infraStart), err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	fakedStart := time.Now()
	if err := d.ensureFakedStarted(r.Context()); err != nil {
		log.Printf("mtkit acquire ensure faked failed cost=%s err=%v", time.Since(fakedStart), err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	// 容器 + faked 就绪后组装注入 SUT 的全部 env（容器连接 + faked 连接），并落盘 sut_env。
	if err := d.prepareSUTEnv(r.Context()); err != nil {
		log.Printf("mtkit acquire prepare sut env failed err=%v", err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	appStart := time.Now()
	if err := d.ensureSUTStarted(r.Context()); err != nil {
		log.Printf("mtkit acquire ensure sut failed cost=%s err=%v", time.Since(appStart), err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	l := d.store.acquire(req.PID, mtconstant.LeaseTTL)
	writeJSON(w, http.StatusOK, protocol.AcquireResp{
		LeaseID:    l.ID,
		AcquiredAt: time.Now(),
	})
}

func (d *Daemon) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	var req protocol.HeartbeatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	if ok := d.store.heartbeat(req.LeaseID, mtconstant.LeaseTTL); !ok {
		writeError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "lease not found")
		return
	}
	writeJSON(w, http.StatusOK, protocol.OKResp{OK: true})
}

func (d *Daemon) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	var req protocol.ReleaseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	if ok := d.store.release(req.LeaseID); !ok {
		writeError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "lease not found")
		return
	}
	writeJSON(w, http.StatusOK, protocol.OKResp{OK: true})
}

func (d *Daemon) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	active := d.store.activeCount(time.Now())
	sutState := d.sut.Snapshot()
	var fakedState mtfaked.State
	if d.faked != nil {
		fakedState = d.faked.Snapshot()
	}
	state := protocol.StateResp{
		InfraReady:     d.bundle.Started(),
		ActiveLeases:   active,
		Containers:     d.bundle.Containers(),
		StartedAt:      d.startedAt,
		IdleTTLSecond:  int(d.cfg.IdleTTL.Seconds()),
		AppReady:       sutState.Ready,
		AppPID:         sutState.PID,
		AppStartedAt:   sutState.StartedAt,
		AppRestarts:    sutState.RestartCount,
		AppLastError:   sutState.LastError,
		// faked 内嵌：随 daemon 生命周期，无独立 PID / 重启计数 / 进程级错误。
		FakedReady:     fakedState.Ready,
		FakedStartedAt: fakedState.StartedAt,
	}
	writeJSON(w, http.StatusOK, state)
}

func (d *Daemon) loopReaper() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var idleSince time.Time
	var sutIdleSince time.Time
	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			now := time.Now()
			d.store.gc(now)
			active := d.store.activeCount(now)
			if active > 0 {
				// 只要有活跃租约，两个空闲计时器都要重置，
				// 否则会在边界时刻误触发提前回收。
				idleSince = time.Time{}
				sutIdleSince = time.Time{}
				continue
			}
			if d.acquireInFlight.Load() > 0 {
				// Acquire 尚未落租约前也视为“忙碌期”，
				// 防止刚启动完 infra/sut 就被 reaper 抢先停掉。
				idleSince = time.Time{}
				sutIdleSince = time.Time{}
				continue
			}
			// 关键决策：SUT 与 daemon 使用双层空闲策略，
			// 先按 SUT IdleTTL 回收被测服务，再按 daemon IdleTTL 回收容器与进程本体。
			if sutIdleSince.IsZero() {
				sutIdleSince = now
			} else if now.Sub(sutIdleSince) >= d.sut.IdleTTL() {
				_ = d.stopSUTOnly(context.Background())
			}
			// 关键决策：这里不依赖 bundle.Started()。
			// 即使 infra 尚未成功启动，空闲 daemon 也要能按 IdleTTL 自动退出，避免并发竞争产生孤儿 runner。
			if idleSince.IsZero() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) >= d.cfg.IdleTTL {
				d.shutdownServer(context.Background())
				return
			}
		}
	}
}

func (d *Daemon) shutdownServer(ctx context.Context) {
	d.shutdownOnce.Do(func() {
		close(d.done)
		// 先停 SUT / faked 再停容器：
		// 避免 SUT 在依赖容器先下线后出现大量无意义报错。
		_ = d.stopSUTOnly(ctx)
		_ = d.stopFakedOnly(ctx)
		_ = d.stopContainersOnly(ctx)
		if d.httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_ = d.httpServer.Shutdown(shutdownCtx)
		}
		mtruntime.Remove(d.runtimePath)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResp{Code: code, Message: message})
}
