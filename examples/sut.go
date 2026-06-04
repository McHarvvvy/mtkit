package examples

import (
	"context"
	"os"
	"os/exec"

	"github.com/McHarvvvy/mtkit"
)

type sutBootPlan struct{}

func newSUTBootPlan() mtkit.SUTBootPlan {
	return &sutBootPlan{}
}

func (p *sutBootPlan) Command(ctx context.Context, in mtkit.StartSUTInput, binaryPath string) (*exec.Cmd, error) {
	_ = ctx
	_ = in
	_ = binaryPath
	cmd := exec.Command("go", "version")
	cmd.Dir = os.TempDir()
	return cmd, nil
}

func (p *sutBootPlan) ProbeAddrs() []string { return nil }
