// Package facade 验证 mtkit 门面层的配置规范化、校验与 Run 装配守卫（业务事实 F2，
// 以及 F1 中"非法装配不交付环境"的早返回部分）。
//
// 本层全为纯函数 / 早返回：不触达 orchestrator，不起 daemon、不连 Docker。
// 故所有用例必须只命中 New 校验或 Run 进入 orchestrator 之前的守卫分支——
// 一旦让 Run 走到合法编排路径，会触发 client 模式自启 daemon，超出本层范围。
package facade

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/testcontainers/testcontainers-go"
)

// ─── 辅助 ─────────────────────────────────────────────────────────────────

// startNoop 是不碰 Docker 的容器 Start：返回 nil 容器，框架容忍。
func startNoop(context.Context) (testcontainers.Container, error) { return nil, nil }

// validRegister 返回注册单个合法容器的 RegisterContainersFunc。
func validRegister(name string) mtkit.RegisterContainersFunc {
	return func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{{Name: name, Start: startNoop}}, nil
	}
}

// recordingRunnable 记录 Run 是否被调用，用于断言"非法装配时 m 不应运行"。
type recordingRunnable struct{ ran bool }

func (r *recordingRunnable) Run() int { r.ran = true; return 0 }

// ─── FC-01 runtime_dir 必填 ────────────────────────────────────────────────

func TestFC01_RuntimeDirRequired(t *testing.T) {
	_, err := mtkit.New("proj", &mtkit.Config{RuntimeDir: ""})
	if err == nil {
		t.Fatal("New 应拒绝空 runtime_dir，但返回 nil error")
	}
}

// ─── FC-02 faked CA 成对 ───────────────────────────────────────────────────

func TestFC02_FakedCAMustBePaired(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		cert    string
		key     string
		wantErr bool
	}{
		{"both empty (dynamic CA)", "", "", false},
		{"both set (static CA)", "cert.pem", "key.pem", false},
		{"only cert", "cert.pem", "", true},
		{"only key", "", "key.pem", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &mtkit.Config{
				RuntimeDir: dir,
				Faked: mtkit.FakedConfig{
					Enabled: true,
					HTTP:    mtkit.FakedHTTPConfig{CACertFile: tt.cert, CAKeyFile: tt.key},
				},
			}
			_, err := mtkit.New("proj", cfg)
			if tt.wantErr && err == nil {
				t.Fatalf("期望报错（CA 半填），但通过")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("期望通过，但报错：%v", err)
			}
		})
	}
}

// ─── FC-03 SUT 启用需产物源 ─────────────────────────────────────────────────

func TestFC03_SUTEnabledRequiresBinaryOrSource(t *testing.T) {
	dir := t.TempDir()

	t.Run("enabled but no binary/source", func(t *testing.T) {
		cfg := &mtkit.Config{RuntimeDir: dir}
		cfg.SUT.Enabled = true
		if _, err := mtkit.New("proj", cfg); err == nil {
			t.Fatal("SUT 启用但 binary/source 都空，应报错")
		}
	})

	t.Run("enabled with binary path", func(t *testing.T) {
		cfg := &mtkit.Config{RuntimeDir: dir}
		cfg.SUT.Enabled = true
		cfg.SUT.BinaryPath = "/path/to/sut"
		if _, err := mtkit.New("proj", cfg); err != nil {
			t.Fatalf("SUT 启用且给定 binary_path，应通过：%v", err)
		}
	})
}

// ─── FC-04 RuntimeFile 解析 ─────────────────────────────────────────────────

func TestFC04_RuntimeFileResolution(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "explicit.runtime.json")
	tests := []struct {
		name string
		file string
		want string
	}{
		{"empty -> default name under dir", "", filepath.Join(dir, mtruntime.DefaultRuntimeFileName)},
		{"plain filename -> under dir", "custom.json", filepath.Join(dir, "custom.json")},
		{"absolute -> as-is", abs, abs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kit, err := mtkit.New("proj", &mtkit.Config{RuntimeDir: dir, RuntimeFile: tt.file})
			if err != nil {
				t.Fatalf("New 失败：%v", err)
			}
			if got := kit.RuntimePath(); got != tt.want {
				t.Fatalf("RuntimePath = %q，期望 %q", got, tt.want)
			}
		})
	}
}

// ─── FC-05 project 缺省 ─────────────────────────────────────────────────────

func TestFC05_ProjectRequired(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty project rejected", func(t *testing.T) {
		if _, err := mtkit.New("  ", &mtkit.Config{RuntimeDir: dir}); err == nil {
			t.Fatal("New 应拒绝空 project")
		}
	})

	t.Run("non-empty project accepted", func(t *testing.T) {
		if _, err := mtkit.New("myproj", &mtkit.Config{RuntimeDir: dir}); err != nil {
			t.Fatalf("New 应接受非空 project：%v", err)
		}
	})
}

// ─── FC-06 容器注册校验（单一真相 container.ValidateRegistrations）──────────────

func TestFC06_ValidateRegistrations(t *testing.T) {
	tests := []struct {
		name    string
		regs    []container.ContainerRegistration
		wantErr bool
	}{
		{"empty list", nil, true},
		{"empty name", []container.ContainerRegistration{{Name: "", Start: startNoop}}, true},
		{"nil start", []container.ContainerRegistration{{Name: "a", Start: nil}}, true},
		{
			"duplicate name",
			[]container.ContainerRegistration{{Name: "a", Start: startNoop}, {Name: "a", Start: startNoop}},
			true,
		},
		{
			"valid",
			[]container.ContainerRegistration{{Name: "a", Start: startNoop}, {Name: "b", Start: startNoop}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := container.ValidateRegistrations(tt.regs)
			if tt.wantErr && err == nil {
				t.Fatal("期望报错，但通过")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("期望通过，但报错：%v", err)
			}
		})
	}
}

// ─── FC-07 Run 装配守卫（触发早返回，m 不应运行）─────────────────────────────

func TestFC07_RunGuards(t *testing.T) {
	dir := t.TempDir()

	t.Run("no RegisterContainers -> 1", func(t *testing.T) {
		kit, err := mtkit.New("proj", &mtkit.Config{RuntimeDir: dir})
		if err != nil {
			t.Fatalf("New 失败：%v", err)
		}
		m := &recordingRunnable{}
		if code := kit.Run(m); code != 1 {
			t.Fatalf("未注册容器，Run 应返回 1，得 %d", code)
		}
		if m.ran {
			t.Fatal("非法装配下 m.Run() 不应被调用")
		}
	})

	t.Run("invalid registrations -> 1", func(t *testing.T) {
		kit, err := mtkit.New("proj", &mtkit.Config{RuntimeDir: dir})
		if err != nil {
			t.Fatalf("New 失败：%v", err)
		}
		kit.RegisterContainers(func(context.Context) ([]container.ContainerRegistration, error) {
			return []container.ContainerRegistration{
				{Name: "dup", Start: startNoop},
				{Name: "dup", Start: startNoop},
			}, nil
		})
		m := &recordingRunnable{}
		if code := kit.Run(m); code != 1 {
			t.Fatalf("非法注册，Run 应返回 1，得 %d", code)
		}
		if m.ran {
			t.Fatal("非法装配下 m.Run() 不应被调用")
		}
	})

	t.Run("SUT enabled but not registered -> 1", func(t *testing.T) {
		cfg := &mtkit.Config{RuntimeDir: dir}
		cfg.SUT.Enabled = true
		cfg.SUT.BinaryPath = "/path/to/sut" // 让 New 校验通过
		kit, err := mtkit.New("proj", cfg)
		if err != nil {
			t.Fatalf("New 失败：%v", err)
		}
		kit.RegisterContainers(validRegister("redis"))
		m := &recordingRunnable{}
		if code := kit.Run(m); code != 1 {
			t.Fatalf("SUT 启用但未 RegisterSUT，Run 应返回 1，得 %d", code)
		}
		if m.ran {
			t.Fatal("非法装配下 m.Run() 不应被调用")
		}
	})
}
