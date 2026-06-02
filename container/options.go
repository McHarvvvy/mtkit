package container

import (
	"context"
	"fmt"
	"strings"

	"github.com/testcontainers/testcontainers-go"
)

// ContainerRegistration 表示单个容器注册项。
type ContainerRegistration struct {
	// Name 是注册项唯一标识，建议与容器 Name 保持一致，用于回滚、清理和日志。
	Name string
	// Start 负责创建并启动容器，返回容器句柄，由框架托管生命周期。
	Start func(ctx context.Context) (testcontainers.Container, error)
	// Init 在所有容器启动后执行（可选），可用于建库、写种子数据等初始化操作。
	Init func(ctx context.Context) error
}

// ValidateRegistrations 校验容器注册项集合：非空、名称非空、Start 非空、名称不重复。
// 作为单一校验真相，供 NewBundle 与门面 New 复用，避免两处规则漂移。
func ValidateRegistrations(regs []ContainerRegistration) error {
	if len(regs) == 0 {
		return fmt.Errorf("container registrations cannot be empty")
	}
	seen := make(map[string]struct{}, len(regs))
	for _, reg := range regs {
		name := strings.TrimSpace(reg.Name)
		if name == "" {
			return fmt.Errorf("container registration name cannot be empty")
		}
		if reg.Start == nil {
			return fmt.Errorf("container registration start cannot be nil: %s", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicated container name: %s", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
