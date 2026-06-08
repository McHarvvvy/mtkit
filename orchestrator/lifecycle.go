package orchestrator

import (
	"context"
	"fmt"
	"log"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

func (d *Daemon) ensureInfraStarted(ctx context.Context) error {
	// 关键决策：基础设施启动入口串行化，避免同一 daemon 被并发 Acquire 重复拉容器。
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if d.infraStarted {
		return nil
	}
	err := d.bundle.StartAll(ctx)
	if err != nil {
		log.Printf("mtkit ensureInfraStarted failed: %v", err)
		return err
	}
	d.infraStarted = true
	return nil
}

func (d *Daemon) stopContainersOnly(ctx context.Context) error {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if !d.infraStarted {
		return nil
	}
	// 与 ensureInfraStarted 共享同一把锁，确保不会出现“边启动边停止”的交叉状态。
	if err := d.bundle.StopAll(ctx); err != nil {
		return err
	}
	d.infraStarted = false
	return nil
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
