package examples

import (
	"context"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/testcontainers/testcontainers-go"
)

// RegisterContainers 注册容器声明（示例最小实现）。
func RegisterContainers(ctx context.Context) ([]container.ContainerRegistration, error) {
	_ = ctx
	return []container.ContainerRegistration{
		{
			Name: "redis-main",
			Start: func(context.Context) (testcontainers.Container, error) {
				return nil, nil
			},
		},
	}, nil
}
