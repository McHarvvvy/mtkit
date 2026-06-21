//go:build mtkit_it

// 本文件是「SUT 接入 mtkit HTTPS MITM 信任」的**模板插件**——它不是 mtkit 框架编译的
// 一部分，而是供 SUT **拷贝到自己 main 包**的现成文件（用法见同目录 README.md）。
//
// 作用：把 faked 的根 CA 装入进程内 RootCAs，使 SUT 信任 faked 对 HTTPS 外部依赖做
// MITM 时现签的叶证书。跨平台一致，不依赖 SSL_CERT_FILE、也不写系统信任库。
//
// 拷到 SUT 的 cmd/<sut>/ 下即可：它是 main 包文件，init 随 SUT 编入、main() 前自动执行，
// 无需任何 import 触发。`//go:build mtkit_it` 保证仅集成测试构建（boot plan 加 -tags
// mtkit_it）编入，生产构建逐字节不含。CA 路径由 mtkit 经 MTKIT_CA_CERT_FILE 环境变量注入。
package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
)

func init() {
	// 名字须与 mtkit 注入侧一致（source of truth：mtkit/constant.EnvMTKitCACertFile）。
	// 本文件是脱离式模板、零 mtkit import，故此处只能硬编码字面量。
	caFile := os.Getenv("MTKIT_CA_CERT_FILE")
	if caFile == "" {
		return
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		// fail loud：集成测试声明了 CA 却读不到，属环境装配错误，不可静默放行。
		panic("mtkit addon: read ca cert " + caFile + ": " + err.Error())
	}
	// 从系统根证书池起步再追加 faked CA，避免丢失系统根（不替换、只增信任）。
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		panic("mtkit addon: append ca cert failed: " + caFile)
	}
	// SUT 所有出站 HTTP(S) 若走 http.DefaultTransport，覆盖其 TLSClientConfig.RootCAs
	// 一处即全覆盖。init 早于任何请求，无并发风险。自建 Transport 的 client 需自行装填。
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("mtkit addon: http.DefaultTransport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	tr.TLSClientConfig.RootCAs = pool
}
