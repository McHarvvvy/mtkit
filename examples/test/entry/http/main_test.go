// Package http_test 是 HTTP 入口（entry）层的黑盒集成测试包（你项目里的 test/entry/http）。
//
// 通过真实 HTTP 调用 SUT 入口、连接真实 MySQL/Redis、（按需）以 fake 替换外部依赖来验证对外契约。
// 用例经 fixture（scope/seeders/request）组织 Given/When/Then；本文件只负责 runtime 接入。
package http_test

import (
	"os"
	"testing"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"

	"shopdemo/test/bootstrap"
	"shopdemo/test/fixture/schema"
)

// TestMain 把本包接入 mtkit 统一 runtime：bootstrap.Run 内部完成容器编排、schema provision、SUT 与
// faked 启动，再透传 m.Run() 退出码。同一句同时适配 daemon 与 client 两种模式（mtkit 自动分流）。
func TestMain(m *testing.M) {
	os.Exit(bootstrap.Run(m, getResources()...))
}

// getResources 组合本包所需的前置 provision 资源——由测试包自己决定 provision 什么，而非 bootstrap 写死。
// profile 入口依赖：账号主表 accounts（登录中间件隐式前置）+ 资料表 profiles（昵称读写）。新增依赖只在此追加一行。
func getResources() []fixture.Resource {
	return []fixture.Resource{
		schema.AccountsTable(),
		schema.ProfilesTable(),
	}
}
