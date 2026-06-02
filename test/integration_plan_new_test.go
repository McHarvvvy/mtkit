//go:build integration_plan

package test

import (
	"context"
	"errors"
	"testing"
	"time"

	mtkit "github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func TestTC01NewFillsDefaultProjectAndRuntimePath(t *testing.T) {
	kitInst, err := mtkit.New(validConfig(), validRegisterFunc("redis"))
	require.NoError(t, err)
	require.NotNil(t, kitInst)
	assert.Equal(t, mtruntime.DefaultRuntimePath("default"), kitInst.RuntimePath(), "New() with empty Project should use default RuntimePath")
}

func TestTC02NewRejectsInvalidKeyFieldsExceptGlobalDefaults(t *testing.T) {
	base := validConfig()
	tests := []struct {
		name string
		cfg  mtkit.Config
	}{
		{
			name: "empty daemon addr",
			cfg: mtkit.Config{
				Global: base.Global,
				Daemon: mtkit.DaemonConfig{Addr: "", IdleTTL: base.Daemon.IdleTTL},
				Client: base.Client,
			},
		},
		{
			name: "invalid client timeout",
			cfg: mtkit.Config{
				Global: base.Global,
				Daemon: base.Daemon,
				Client: mtkit.ClientConfig{HTTPTimeout: 0},
			},
		},
		{
			name: "invalid daemon idle ttl",
			cfg: mtkit.Config{
				Global: base.Global,
				Daemon: mtkit.DaemonConfig{Addr: base.Daemon.Addr, IdleTTL: 0},
				Client: base.Client,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mtkit.New(tt.cfg, validRegisterFunc("redis"))
			assert.Error(t, err, "New() should reject config: %s", tt.name)
		})
	}
}

func TestTC03NewRejectsNilRegisterContainersFunc(t *testing.T) {
	_, err := mtkit.New(validConfig(), nil)
	assert.Error(t, err, "New() should reject nil RegisterContainersFunc")
}

func TestTC04NewRejectsEmptyContainerRegistrationList(t *testing.T) {
	_, err := mtkit.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{}, nil
	})
	assert.Error(t, err, "New() should reject empty ContainerRegistration list")
}

func TestTC05NewRejectsDuplicatedContainerName(t *testing.T) {
	_, err := mtkit.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis", Start: startNoop},
			{Name: "redis", Start: startNoop},
		}, nil
	})
	assert.Error(t, err, "New() should reject duplicate container name")
}

func TestTC06NewRejectsInvalidContainerRegistrationFields(t *testing.T) {
	t.Run("name is empty", func(t *testing.T) {
		_, err := mtkit.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
			return []container.ContainerRegistration{{Name: "", Start: startNoop}}, nil
		})
		assert.Error(t, err, "New() should reject registration with empty name")
	})

	t.Run("start is nil", func(t *testing.T) {
		_, err := mtkit.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
			return []container.ContainerRegistration{{Name: "redis", Start: nil}}, nil
		})
		assert.Error(t, err, "New() should reject registration with nil Start func")
	})
}

func TestTC07NewPropagatesRegisterContainersError(t *testing.T) {
	expectErr := errors.New("config load failed")
	_, err := mtkit.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return nil, expectErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, expectErr, "New() should propagate error from RegisterContainersFunc")
}

func validConfig() mtkit.Config {
	return mtkit.Config{
		Global: mtkit.GlobalConfig{},
		Daemon: mtkit.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: mtkit.ClientConfig{HTTPTimeout: 5 * time.Second},
	}
}

func validRegisterFunc(name string) mtkit.RegisterContainersFunc {
	return func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{{Name: name, Start: startNoop}}, nil
	}
}

func startNoop(context.Context) (testcontainers.Container, error) {
	return nil, nil
}
