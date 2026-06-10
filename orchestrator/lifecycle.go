package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// ensureEnvReady 串行化整段环境就绪准备：容器 infra → faked → SUT 注入 env → SUT 进程。
//
// 关键决策：整段持 readyMu。各步都对 daemon 共享状态（runtimeInfo / fakedEnv / sutEnvReady）
// 做幂等写入并可能重写同一个 runtime 文件；并发 Acquire 若不串行会产生数据竞争与落盘互相覆盖。
// 首个 Acquire 完成首次启动后，后续 Acquire 在锁内只是快速走幂等空过——锁不发放租约，发放在锁外。
// 容器启停的并发与幂等由 Bundle 自身的锁负责（StartAll/StopAll 共用 b.mu），故此处无需再叠加 infra 锁。
func (d *Daemon) ensureEnvReady(ctx context.Context) error {
	d.readyMu.Lock()
	defer d.readyMu.Unlock()

	infraStart := time.Now()
	if err := d.ensureInfraStarted(ctx); err != nil {
		log.Printf("mtkit acquire ensure infra failed cost=%s err=%v", time.Since(infraStart), err)
		return err
	}
	fakedStart := time.Now()
	if err := d.ensureFakedStarted(ctx); err != nil {
		log.Printf("mtkit acquire ensure faked failed cost=%s err=%v", time.Since(fakedStart), err)
		return err
	}
	// 容器 + faked 就绪后组装注入 SUT 的全部 env（容器连接 + faked 连接），并落盘 sut_env。
	if err := d.prepareSUTEnv(ctx); err != nil {
		log.Printf("mtkit acquire prepare sut env failed err=%v", err)
		return err
	}
	appStart := time.Now()
	if err := d.ensureSUTStarted(ctx); err != nil {
		log.Printf("mtkit acquire ensure sut failed cost=%s err=%v", time.Since(appStart), err)
		return err
	}
	return nil
}

// ensureInfraStarted 启动容器基础设施（幂等）。并发与幂等由 Bundle 自身的锁与 started 标志保证，
// 故 orchestrator 不再重复维护启动标志：Acquire 路径整体在 readyMu 下串行，与停止路径经 Bundle
// 同一把 b.mu 互斥（StartAll/StopAll 不会交叉）。
func (d *Daemon) ensureInfraStarted(ctx context.Context) error {
	if err := d.bundle.StartAll(ctx); err != nil {
		log.Printf("mtkit ensureInfraStarted failed: %v", err)
		return err
	}
	return nil
}

// stopContainersOnly 停止容器基础设施；幂等性与“边启边停”互斥由 Bundle 的 b.mu 保证
// （StopAll 与 StartAll 共用同一把锁，未启动时为安全空操作）。
func (d *Daemon) stopContainersOnly(ctx context.Context) error {
	return d.bundle.StopAll(ctx)
}

func (d *Daemon) ensureSUTStarted(ctx context.Context) error {
	return d.sut.EnsureStarted(ctx)
}

func (d *Daemon) stopSUTOnly(ctx context.Context) error {
	return d.sut.Stop(ctx)
}

// ensureFakedStarted 启动内嵌 faked 引擎（幂等），把 faked 连接信息暂存为待注入 env，
// 并记录 faked 段到 runtime 基线（最终由 prepareSUTEnv 与 sut_env 一并写盘）。
func (d *Daemon) ensureFakedStarted(ctx context.Context) error {
	if d.faked == nil {
		return nil
	}
	addrs, err := d.faked.Start(ctx)
	if err != nil {
		return err
	}
	d.fakedEnv = fakedInjectEnv(addrs)
	d.runtimeInfo.Faked = &mtruntime.FakedRuntime{
		GRPCEndpoints: addrs.GRPCEndpoints,
		ProxyAddr:     addrs.ProxyAddr,
		CACertFile:    addrs.CACertFile,
	}
	return nil
}

// prepareSUTEnv 在容器与 faked 就绪后、SUT 启动前组装要注入 SUT 的全部环境变量
// （容器连接信息 + faked 连接信息），交给 SUT 并落盘到 runtime 文件。幂等。
func (d *Daemon) prepareSUTEnv(ctx context.Context) error {
	if d.sutEnvReady {
		return nil
	}
	containerEnv, err := d.bundle.SUTEnv(ctx)
	if err != nil {
		return err
	}
	env := make(map[string]string, len(containerEnv)+len(d.fakedEnv))
	for k, v := range containerEnv {
		env[k] = v
	}
	for k, v := range d.fakedEnv {
		// faked 与容器同名 env key 视为开发期错误（命名空间应互不重叠）。
		if _, dup := env[k]; dup {
			return fmt.Errorf("sut env key %q from faked collides with a container env", k)
		}
		env[k] = v
	}
	d.sut.SetInjectEnv(env)
	d.runtimeInfo.SUTEnv = env
	if err := mtruntime.Write(d.runtimePath, d.runtimeInfo); err != nil {
		// 落盘失败仅影响排查，不阻塞 acquire。
		log.Printf("mtkit write runtime sut_env failed: %v", err)
	}
	d.sutEnvReady = true
	return nil
}

// fakedInjectEnv 把 faked 数据面地址转成要注入 SUT 的环境变量：gRPC 直连地址、HTTP 代理、CA。
func fakedInjectEnv(addrs mtfaked.Addrs) map[string]string {
	env := make(map[string]string, len(addrs.GRPCEndpoints)+4)
	for k, v := range addrs.GRPCEndpoints {
		env[k] = v // <env_key>=<grpc_addr>：SUT 直连 fake gRPC，不经代理
	}
	if addrs.ProxyAddr != "" {
		proxy := "http://" + addrs.ProxyAddr
		env["HTTP_PROXY"] = proxy
		env["HTTPS_PROXY"] = proxy
		env["NO_PROXY"] = "127.0.0.1,localhost"
	}
	if addrs.CACertFile != "" {
		env[mtconstant.EnvMTKitCACertFile] = addrs.CACertFile
	}
	return env
}

func (d *Daemon) stopFakedOnly(ctx context.Context) error {
	if d.faked == nil {
		return nil
	}
	return d.faked.Stop(ctx)
}
