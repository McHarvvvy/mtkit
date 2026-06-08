// Command suthelper 是 SUT 进程管理测试用的最小被测进程替身。
//
// 行为由 SUTHELPER_* 环境变量驱动，便于在 sut.Manager 边界断言进程管理行为
// （启动/探活/注入 env/停止/复用/构建），自身不含任何业务逻辑。
package main

import (
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	switch os.Getenv("SUTHELPER_MODE") {
	case "exit":
		// 立即退出，触发就绪失败路径。
		code, _ := strconv.Atoi(os.Getenv("SUTHELPER_EXIT_CODE"))
		os.Exit(code)

	case "listen":
		// 延迟后监听探测端口——模拟"进程起来但服务尚未就绪"的窗口。
		if ms, _ := strconv.Atoi(os.Getenv("SUTHELPER_DELAY_MS")); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		ln, err := net.Listen("tcp", os.Getenv("SUTHELPER_ADDR"))
		if err != nil {
			os.Exit(3)
		}
		go acceptLoop(ln)
		block()

	case "writeenv":
		// 读取指定 env key 的值写入文件——验证注入 env 真的进了本进程。
		val := os.Getenv(os.Getenv("SUTHELPER_READ_KEY"))
		_ = os.WriteFile(os.Getenv("SUTHELPER_OUT_FILE"), []byte(val), 0o644)
		block()

	default: // "stable"：常驻存活，供稳定就绪 / 复用 / 停止用例。
		block()
	}
}

func acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// block 阻塞直到收到终止信号。不用 select{} 是为避免 Go 运行时死锁检测 panic；
// 框架停止时会强制终止进程树，故此处只需不主动退出。
func block() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
