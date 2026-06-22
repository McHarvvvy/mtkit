// Package harness 是 fixture 各子包共享的“运行期单例引导”（你项目里的 test/fixture/harness）。
//
// 关键纪律：mtkit 的 runtime 文件（含 daemon 地址 / token / SUTEnv）只有在 kit.Run(m) 内部、daemon
// 自启并拿到租约后才写好——**即 m.Run() 执行期才可读**。故 fixture.Client / PersistEnv 不能在 TestMain
// 的 Run 之前构造，必须懒初始化。本包用进程级 sync.Once 兜住这条：全进程只读一次 runtime、只建一份
// client / PersistEnv，供 seeders / request / 用例复用。
//
// 重要：PersistEnv / fixture.Client 是**进程级共享物，绝不可在用例 cleanup 里 Close**——一个用例关掉
// 会拖垮其它并发用例；进程退出自然回收。
package harness

import (
	"sync"
	"testing"

	fake "github.com/McHarvvvy/mtkit/client/fake"
	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"

	"shopdemo/test/contract"
)

var (
	once       sync.Once
	fxClient   *fixture.Client
	fakeClient *fake.Client
	persistEnv *fixture.PersistEnv
	initErr    error
)

// ensure 懒读 runtime 并构造进程级单例；任何一步失败记入 initErr，由 getter 转 t.Fatal。
func ensure() {
	once.Do(func() {
		path, err := contract.RuntimePath()
		if err != nil {
			initErr = err
			return
		}
		info, err := mtruntime.Read(path) // 只在测试执行期调用，绝不在 TestMain Run 之前
		if err != nil {
			initErr = err
			return
		}
		fxClient = fixture.New(info.Addr, info.Token)
		fakeClient = fake.New(info.Addr, info.Token) // 与 fixture 同一 daemon 地址/token
		persistEnv = fixture.NewPersistEnv(info.SUTEnv)
	})
}

// Fixture 返回进程级唯一的 fixture.Client（唯一数据发号 / CaseScope 工厂）。
func Fixture(t testing.TB) *fixture.Client {
	t.Helper()
	ensure()
	if initErr != nil {
		t.Fatalf("harness: fixture client 不可用: %v", initErr)
	}
	return fxClient
}

// Persist 返回进程级唯一的 PersistEnv（seeders 经它直连真实存储）。
func Persist(t testing.TB) *fixture.PersistEnv {
	t.Helper()
	ensure()
	if initErr != nil {
		t.Fatalf("harness: PersistEnv 不可用: %v", initErr)
	}
	return persistEnv
}

// Fake 返回进程级唯一的 fake.Client（用例据它给 fake gRPC/HTTP endpoint 注册 per-case 规则）。
func Fake(t testing.TB) *fake.Client {
	t.Helper()
	ensure()
	if initErr != nil {
		t.Fatalf("harness: fake client 不可用: %v", initErr)
	}
	return fakeClient
}
