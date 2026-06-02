package examples

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
)

// Run 是各测试包 TestMain 的统一入口。
func Run(m *testing.M) int {
	kit, err := mtkit.New(
		mtkit.Config{
			Global: mtkit.GlobalConfig{
				Project:     "mssiot_user",
				RuntimePath: "",
			},
			Daemon: mtkit.DaemonConfig{
				Addr:    "127.0.0.1:0",
				Token:   "",
				IdleTTL: 36 * time.Second,
			},
			Client: mtkit.ClientConfig{
				HTTPTimeout: 1 * time.Minute,
			},
			SUT: newSUTBootPlan(),
		},
		func(ctx context.Context) ([]container.ContainerRegistration, error) {
			return RegisterContainers(ctx)
		},
	)
	if err != nil {
		log.Printf("mtkit.New failed: %v", err)
		return 1
	}
	return kit.Run(m)
}
