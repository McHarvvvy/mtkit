package fixture

import "testing"

// TestPersistEnvMissingKeyPanics 守护 PersistEnv 的 fail-loud 契约：取一个未注册（值缺失）的
// sutEnvKey 时，每个后端访问器都应 panic。这是纯进程内行为（mustValue 在任何 I/O 之前就 panic），
// 不需要真实容器，故留在进程内单测——把它从 e2e 里剥离，免得用真容器的慢成本去养与容器无关的逻辑。
func TestPersistEnvMissingKeyPanics(t *testing.T) {
	env := NewPersistEnv(nil)
	cases := map[string]func(){
		"Redis":        func() { env.Redis("MISSING") },
		"MySQL":        func() { env.MySQL("MISSING") },
		"Mongo":        func() { env.Mongo("MISSING") },
		"TDengine":     func() { env.TDengine("MISSING") },
		"PulsarClient": func() { env.PulsarClient("MISSING") },
		"PulsarAdmin":  func() { env.PulsarAdmin("MISSING") },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("取未注册的 %s key 应 fail-loud panic，但没有", name)
				}
			}()
			fn()
		})
	}
}
