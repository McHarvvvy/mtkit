package container

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/testcontainers/testcontainers-go"
)

const maxCreateConcurrency = 4

type startResult struct {
	name string
	ctr  testcontainers.Container
	err  error
}

// Bundle 负责测试容器资源生命周期。
//
// 契约：本 bundle 假定注册的容器【相互独立】——StartAll 并发启动、不保证注册顺序即启动顺序，
// 故容器不得依赖另一容器“先就绪”。需要容器间初始化关系（建库、写种子）的，放到 Init：
// Init 在所有容器启动后串行执行，可经闭包捕获访问任意已启动容器。停止按注册逆序，仅是礼貌性
// 收尾（后注册的通常更外围），不承诺依赖拓扑语义。若将来出现真正的容器间启动依赖，应显式引入
// 阶段/依赖模型，而非依赖这里的隐式顺序。
type Bundle struct {
	mu      sync.Mutex
	started bool
	regs    []ContainerRegistration

	startOrder  []string
	cleanup     []testcontainers.Container
	containerBy map[string]testcontainers.Container
}

// NewBundle 创建容器资源管理对象，校验注册项合法性。
func NewBundle(regs []ContainerRegistration) (*Bundle, error) {
	if err := ValidateRegistrations(regs); err != nil {
		return nil, err
	}
	return &Bundle{
		regs:        regs,
		startOrder:  make([]string, 0, len(regs)),
		cleanup:     make([]testcontainers.Container, 0, len(regs)),
		containerBy: make(map[string]testcontainers.Container, len(regs)),
	}, nil
}

// StartAll 启动全部依赖容器。
func (b *Bundle) StartAll(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.started {
		return nil
	}

	containerByName, startErr := b.startAllContainers(ctx, b.regs)
	if startErr != nil {
		return startErr
	}
	for _, reg := range b.regs {
		ctr, ok := containerByName[reg.Name]
		if !ok {
			_ = terminateCreatedContainers(ctx, b.regs, containerByName)
			return fmt.Errorf("container not found after start: %s", reg.Name)
		}
		b.startOrder = append(b.startOrder, reg.Name)
		b.cleanup = append(b.cleanup, ctr)
		b.containerBy[reg.Name] = ctr
	}

	// 关键决策：容器全部启动后再执行 Init，确保初始化逻辑可访问所有已启动容器。
	for _, reg := range b.regs {
		if reg.Init == nil {
			continue
		}
		if err := reg.Init(ctx); err != nil {
			// 关键决策：初始化失败同样回滚，保证下次 Acquire 仍从干净环境开始。
			_ = b.rollback(ctx)
			return fmt.Errorf("run init for %s failed: %w", reg.Name, err)
		}
	}

	b.started = true
	return nil
}

// StopAll 销毁全部依赖容器。
func (b *Bundle) StopAll(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.stopAllContainers(ctx); err != nil {
		return err
	}
	b.resetState()
	return nil
}

// SUTEnv 收集各容器注册项 SUTEnv 回调产出的环境变量（容器连接信息）。
//
// 在容器启动后调用：按 Name 取出对应容器句柄传给各自的 SUTEnv 回调，合并所有产物。
// 不同容器贡献同名 env key 视为开发期错误，直接报错（避免静默覆盖）。
func (b *Bundle) SUTEnv(ctx context.Context) (map[string]string, error) {
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return nil, fmt.Errorf("bundle not started")
	}
	type job struct {
		reg ContainerRegistration
		ctr testcontainers.Container
	}
	jobs := make([]job, 0, len(b.regs))
	for _, reg := range b.regs {
		if reg.SUTEnv == nil {
			continue
		}
		jobs = append(jobs, job{reg: reg, ctr: b.containerBy[reg.Name]})
	}
	b.mu.Unlock()

	// 回调可能做 docker 调用（MappedPort），故不持锁执行。
	out := make(map[string]string)
	for _, j := range jobs {
		env, err := j.reg.SUTEnv(ctx, j.ctr)
		if err != nil {
			return nil, fmt.Errorf("build sut env for %s: %w", j.reg.Name, err)
		}
		for k, v := range env {
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("duplicate sut env key %q (from container %s)", k, j.reg.Name)
			}
			out[k] = v
		}
	}
	return out, nil
}

// Started 返回当前容器资源是否已启动。
func (b *Bundle) Started() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

// Containers 返回当前已启动的容器注册名列表。
func (b *Bundle) Containers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]string, len(b.startOrder))
	copy(result, b.startOrder)
	sort.Strings(result)
	return result
}

func (b *Bundle) rollback(ctx context.Context) error {
	err := b.stopAllContainers(ctx)
	b.resetState()
	return err
}

func (b *Bundle) startAllContainers(ctx context.Context, regs []ContainerRegistration) (map[string]testcontainers.Container, error) {
	containerByName := make(map[string]testcontainers.Container, len(regs))
	if len(regs) == 0 {
		return containerByName, nil
	}

	createCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan ContainerRegistration, len(regs))
	for _, reg := range regs {
		jobs <- reg
	}
	close(jobs)

	results := make(chan startResult, len(regs))
	workerN := maxCreateConcurrency
	if len(regs) < workerN {
		workerN = len(regs)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for reg := range jobs {
				if createCtx.Err() != nil {
					continue
				}
				ctr, err := reg.Start(createCtx)
				if err != nil {
					results <- startResult{name: reg.Name, err: fmt.Errorf("start container %s failed: %w", reg.Name, err)}
					cancel()
					continue
				}
				results <- startResult{name: reg.Name, ctr: ctr}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		containerByName[result.name] = result.ctr
	}

	if firstErr != nil {
		if terminateErr := terminateCreatedContainers(ctx, regs, containerByName); terminateErr != nil {
			return nil, fmt.Errorf("%w; rollback failed: %v", firstErr, terminateErr)
		}
		return nil, firstErr
	}
	return containerByName, nil
}

func terminateCreatedContainers(ctx context.Context, regs []ContainerRegistration, containers map[string]testcontainers.Container) error {
	var firstErr error
	// 容器假定相互独立（见 Bundle 契约）；逆序停止仅为礼貌性收尾，不承诺依赖语义。
	for i := len(regs) - 1; i >= 0; i-- {
		ctr, ok := containers[regs[i].Name]
		if !ok || ctr == nil {
			continue
		}
		if err := ctr.Terminate(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (b *Bundle) stopAllContainers(ctx context.Context) error {
	var firstErr error
	// 容器假定相互独立（见 Bundle 契约）；逆序停止仅为礼貌性收尾，不承诺依赖语义。
	for i := len(b.cleanup) - 1; i >= 0; i-- {
		ctr := b.cleanup[i]
		if ctr == nil {
			continue
		}
		if err := ctr.Terminate(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (b *Bundle) resetState() {
	b.started = false
	b.startOrder = make([]string, 0, len(b.regs))
	b.cleanup = make([]testcontainers.Container, 0, len(b.regs))
	b.containerBy = make(map[string]testcontainers.Container, len(b.regs))
}
