package examples

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/McHarvvvy/mtkit"
)

type sutBootPlan struct{}

func newSUTBootPlan() mtkit.SUTBootPlan {
	return &sutBootPlan{}
}

func (p *sutBootPlan) IsEnable() bool { return false }

func (p *sutBootPlan) GetIdleTTL() time.Duration { return 2 * time.Second }

func (p *sutBootPlan) GetReadyTimeout() time.Duration { return 8 * time.Second }

func (p *sutBootPlan) GetGracePeriod() time.Duration { return 4 * time.Second }

func (p *sutBootPlan) GetCommand(ctx context.Context, in mtkit.StartSUTInput) (*exec.Cmd, error) {
	_ = ctx
	_ = in
	cmd := exec.Command("go", "version")
	cmd.Dir = os.TempDir()
	return cmd, nil
}

func (p *sutBootPlan) GetProbeAddrs() []string { return nil }
