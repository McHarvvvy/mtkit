package examples

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
)

// Run 是各测试包 TestMain 的统一入口。
func Run(m *testing.M) int {
	kit, err := mtkit.New(
		"mssiot_user",
		&mtkit.Config{
			RuntimeDir: filepath.Join(os.TempDir(), "mt", "mssiot_user"),
			DaemonMode: mtkit.DaemonModeConfig{
				Addr:    "127.0.0.1:0",
				Token:   "",
				IdleTTL: 36 * time.Second,
			},
			ClientMode: mtkit.ClientModeConfig{
				HTTPTimeout: 1 * time.Minute,
			},
		},
	)
	if err != nil {
		log.Printf("mtkit.New failed: %v", err)
		return 1
	}
	return kit.
		RegisterContainers(func(ctx context.Context) ([]container.ContainerRegistration, error) {
			return RegisterContainers(ctx)
		}).
		RegisterSUT(newSUTBootPlan()).
		Run(m)
}
