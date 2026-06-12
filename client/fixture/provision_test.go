package fixture

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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
