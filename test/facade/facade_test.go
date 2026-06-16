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

// TestFC01_RuntimeDirRequired 守护 F2：runtime_dir 是 daemon 发现文件与账本的落点根目录，必填。
// 验证手段：以空 RuntimeDir 调 New，断言在 New 校验阶段就返回 error（尚未进入 orchestrator/起 daemon）。
func TestFC01_RuntimeDirRequired(t *testing.T) {
	_, err := mtkit.New("proj", &mtkit.Config{RuntimeDir: ""})
	if err == nil {
		t.Fatal("New 应拒绝空 runtime_dir，但返回 nil error")
	}
}

// ─── FC-02 faked CA 成对 ───────────────────────────────────────────────────

// TestFC02_FakedCAMustBePaired 守护 F2：静态 CA 的 cert 与 key 必须成对提供（半填是配置错误）。
// 验证手段：四种组合过 New——都空（动态 CA）、都填（静态 CA）应通过；只填 cert / 只填 key 应报错。
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

// TestFC03_SUTEnabledRequiresBinaryOrSource 守护 F2：启用 SUT 必须给出产物来源（预置 binary 或源码 dir），
// 否则框架无从启动被测程序。
// 验证手段：SUT.Enabled 下分别给 空 / binary_path / source_dir 过 New，断言空报错、任一非空通过。
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

	t.Run("enabled with source dir", func(t *testing.T) {
		cfg := &mtkit.Config{RuntimeDir: dir}
		cfg.SUT.Enabled = true
		cfg.SUT.SourceDir = "/path/to/src"
		if _, err := mtkit.New("proj", cfg); err != nil {
			t.Fatalf("SUT 启用且给定 source_dir，应通过：%v", err)
		}
	})
}

// ─── FC-04 RuntimeFile 解析 ─────────────────────────────────────────────────

// TestFC04_RuntimeFileResolution 守护 F2：runtime_file 三种写法解析到正确的对外路径（RuntimePath 是发布产物）。
// 验证手段：空 / 纯文件名 / 绝对路径 分别过 New，断言 RuntimePath() = 默认名落 dir / 该文件名落 dir / 绝对路径原样。
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

// TestFC05_ProjectRequired 守护 F2：project 必填（非空白）——它是 daemon 复用与隔离的命名空间键。
// 验证手段：空白 project 过 New 应报错；非空通过。
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

// TestFC06_ValidateRegistrations 守护 F2 容器注册校验，直接打单一真相 container.ValidateRegistrations
// （门面 Run 与此处共用它，故在此集中验证规则本身，避免规则被绕过或漂移）。
// 验证手段：空列表 / 空名 / nil Start / 重名 / 合法 五种输入，断言前四报错、合法通过。
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

// TestFC07_RunGuards 守护 F1/F2 的"非法装配不交付环境"：装配守卫触发 Run 早返回 1，且绝不调用 m.Run()。
// 验证手段：未注册容器 / 注册非法（重名）/ SUT 启用却未 RegisterSUT 三种，断言 Run 返回 1 且 recordingRunnable.ran
// 为 false——即守卫在进入编排前拦截，被测程序不会在环境未就绪时被跑起来（断言行为，不咬具体哪条守卫命中）。
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
