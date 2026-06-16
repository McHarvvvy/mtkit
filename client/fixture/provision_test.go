package fixture

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileRegistryEnsureOnce 守护 provision 的幂等契约（C1，单线程基线）：同一 key 只执行一次 fn，二次调用被跳过。
// 验证逻辑：对同一 key 连调两次 EnsureOnce，断言首次 skipped=false、二次 skipped=true，且 fn 实际执行次数恰为 1。
func TestFileRegistryEnsureOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtkit.provision.json")
	reg := NewFileRegistry(path)

	var calls int
	skipped, err := reg.EnsureOnce(context.Background(), "mysql.test.users", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped {
		t.Fatal("first call skipped")
	}

	skipped, err = reg.EnsureOnce(context.Background(), "mysql.test.users", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !skipped {
		t.Fatal("second call not skipped")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestFileRegistryDoesNotMarkFailedApply 守护"失败不标记、可重入"契约（C2）：fn 返回错误时 key 不入账本，下次可重试。
// 验证逻辑：先让 fn 返回错误（断言该错误被透传），再对同一 key 用成功 fn 调用，断言这次没被跳过且 fn 真的执行了一次
// （证明上一次失败没有污染账本，重试得以成立）。
func TestFileRegistryDoesNotMarkFailedApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtkit.provision.json")
	reg := NewFileRegistry(path)
	wantErr := errors.New("boom")

	_, err := reg.EnsureOnce(context.Background(), "mysql.test.users", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}

	var calls int
	skipped, err := reg.EnsureOnce(context.Background(), "mysql.test.users", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped {
		t.Fatal("successful retry skipped after failed apply")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestProvisionWrapsResourceKey 守护 Provision 的错误可定位契约（C3）：某资源 Apply 失败时，返回错误要指明是哪个 key。
// 验证逻辑：注入一个 Apply 必失败的 Resource，断言 Provision 返回的错误既能 errors.Is 到原始错误（保留错误链），
// 又在文本中包含该资源的 key——调用方据此能直接定位是哪条 schema 没建成。
func TestProvisionWrapsResourceKey(t *testing.T) {
	reg := NewFileRegistry(filepath.Join(t.TempDir(), "mtkit.provision.json"))
	wantErr := errors.New("apply failed")
	err := Provision(context.Background(), reg, nil, []Resource{testResource{
		key: "mysql.test.users",
		apply: func(context.Context, *PersistEnv) error {
			return wantErr
		},
	}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "mysql.test.users") {
		t.Fatalf("err %q does not contain resource key", err)
	}
}

type testResource struct {
	key   string
	apply func(context.Context, *PersistEnv) error
}

func (r testResource) Key() string { return r.key }

func (r testResource) Apply(ctx context.Context, env *PersistEnv) error {
	return r.apply(ctx, env)
}
