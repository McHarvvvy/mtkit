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
	// 关键决策：按逆序停止，尽量贴近依赖拓扑。
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
	// 关键决策：按逆序停止，尽量贴近依赖拓扑（后启动的通常更依赖前置服务）。
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
