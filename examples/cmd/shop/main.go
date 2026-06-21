// Command shop 是玩具被测服务（SUT）的**骨架**。
//
// 它只搭出 SUT 的外形：在约定端口起 HTTP + gRPC、从环境变量读依赖地址，让示例工程能编译、
// 结构成立。真实业务逻辑留空——要让 examples 的 entry 测试真正跑通，你需要把下面注释标出的部分
// 按自己的服务补全。这正是「接入 mtkit 前，先确保 SUT 能在测试环境下完整启动」那条前提的落点。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
)

// SUT 监听地址：与 test/bootstrap/sut.go 的常量一致（ProbeAddrs / request 基址都按它来）。
// 真实项目里这通常来自 SUT 自己的配置文件，集成测试构建时指向本地端口。
const (
	httpAddr = "127.0.0.1:18080"
	grpcAddr = "127.0.0.1:18081"
)

func main() {
	// mtkit 注入的依赖地址 / 项目自己的测试开关，都从环境变量读。
	deps := struct {
		mysqlDSN    string
		redisAddr   string
		loyaltyAddr string
	}{
		mysqlDSN:    os.Getenv("SHOP_IT_MYSQL_DSN"),
		redisAddr:   os.Getenv("SHOP_IT_REDIS_ADDR"),
		loyaltyAddr: os.Getenv("SHOP_IT_RPC_LOYALTY_ADDR"),
	}

	// TODO(you): 像生产一样初始化全部依赖（连 MySQL/Redis、建 loyalty gRPC client），
	// 只是连接目标换成上面注入的地址。任何在测试环境下跳过的初始化，都会在首个用到它的用例上炸。
	_ = deps

	// HTTP 入口：真实项目在此挂 /shop/v1/profile/setNickname、/getNickname 等 handler。
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "shop SUT skeleton: handler not implemented", http.StatusNotImplemented)
	})
	httpSrv := &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("shop: http serve: %v", err)
		}
	}()

	// gRPC 入口：真实项目在此注册自己的 service。
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("shop: grpc listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			log.Fatalf("shop: grpc serve: %v", err)
		}
	}()

	// 骨架：阻塞住进程，让 mtkit 的就绪探测能连上两个端口。真实项目按自己的方式优雅退出。
	<-context.Background().Done()
}
