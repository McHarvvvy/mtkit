// Package ca 维护测试 CA 与按 host 动态签发的叶证书。
//
// 集成测试用 MITM 代理拦截 SUT 的 HTTPS 调用：代理握手时需要为 SNI 中出现
// 的任意域名出示一张可信证书。Bundle 持有
//
// 注意：本包实现**两种 CA 获取机制**，一种是从配置文件加载的固定测试 CA，
// 按 host 即时签发叶证书并缓存，避免每次握手重复签发；另一种是运行时动态签发 CA 叶节点证书

package ca

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 动态 CA 模式产物文件名：落在 RuntimeDir 下，与静态模式用户指定路径互不冲突。
const (
	GeneratedCACertFileName = "generated-ca.crt"
	GeneratedCAKeyFileName  = "generated-ca.key"
)

// Bundle 持有测试 CA 与按 host 动态签发的叶证书缓存。
//
// rootKey 用 crypto.Signer 而非具体类型：CA 是外部配置资产，可能用 RSA / ECDSA /
// Ed25519 任一算法（设计 §4.7）。x509.CreateCertificate 的签名者参数本就收 crypto.Signer，
// 三种私钥类型都满足；叶证书自身固定用 RSA，与 CA 算法无关。
type Bundle struct {
	rootCert *x509.Certificate
	rootKey  crypto.Signer
	cache    sync.Map // host → *tls.Certificate
}

// Generate 在 dir 下动态生成 RSA 2048 自签 CA cert + key，并直接构造 Bundle。
//
// 文件名取 envvar.GeneratedCA{Cert,Key}FileName（generated-ca.crt / .key），
// 与静态模式下用户指定的 cert/key 路径互不冲突；daemon 启动时把 certPath 回填到
// cfg.HTTP.CACertFile，由 writeRuntime 暴露给 SUT / 调试工具。
//
// CA 24 小时有效期，每次 daemon 启动新生成；CommonName 与 install/uninstall 脚本对齐
// （便于 dev 调试模式手动匹配 Subject）。
//
// 动态模式固定生成 RSA：每次启动新生成、无需对接外部资产，RSA 2048 足够且
// 与 install 脚本兼容。静态模式（LoadFromFiles）则接受 RSA / ECDSA / Ed25519 任一算法。
func Generate(dir string) (*Bundle, string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca: generate key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mtkit faked Integration Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca: create certificate: %w", err)
	}

	certPath := filepath.Join(dir, GeneratedCACertFileName)
	keyPath := filepath.Join(dir, GeneratedCAKeyFileName)
	if err := writePEMFile(certPath, "CERTIFICATE", der); err != nil {
		return nil, "", "", err
	}
	if err := writePEMFile(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)); err != nil {
		return nil, "", "", err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca: parse just-created cert: %w", err)
	}
	return &Bundle{rootCert: cert, rootKey: key}, certPath, keyPath, nil
}

func writePEMFile(path, blockType string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("ca: create %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("ca: encode pem %s: %w", path, err)
	}
	return nil
}

// LoadFromFiles 从配置路径加载 CA cert + key。
// 文件不存在 / 解析失败时返回明确错误，daemon 拒绝启动。
func LoadFromFiles(certPath, keyPath string) (*Bundle, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read cert %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read key %s: %w", keyPath, err)
	}
	return Load(certPEM, keyPEM)
}

// Load 解析 CA 证书与私钥的 PEM 字节，构造 Bundle。
func Load(certPEM, keyPEM []byte) (*Bundle, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("ca: failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("ca: failed to decode private key PEM")
	}
	key, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse private key: %w", err)
	}

	return &Bundle{rootCert: cert, rootKey: key}, nil
}

// CertFor 返回为 host 签发的叶证书。首次签发后结果会被缓存。
//
// host 允许为空（少数客户端不发送 SNI），此时返回 CommonName 为 "unknown-sni" 的通用证书。
func (b *Bundle) CertFor(host string) (*tls.Certificate, error) {
	if host == "" {
		host = "unknown-sni"
	}
	if v, ok := b.cache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}
	cert, err := b.sign(host)
	if err != nil {
		return nil, err
	}
	// LoadOrStore 防止并发签发重复。
	if actual, loaded := b.cache.LoadOrStore(host, cert); loaded {
		return actual.(*tls.Certificate), nil
	}
	return cert, nil
}

func (b *Bundle) sign(host string) (*tls.Certificate, error) {
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("ca: generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: generate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, b.rootCert, &leafKey.PublicKey, b.rootKey)
	if err != nil {
		return nil, fmt.Errorf("ca: sign leaf for %s: %w", host, err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{der, b.rootCert.Raw},
		PrivateKey:  leafKey,
		Leaf:        tmpl,
	}, nil
}

// parsePrivateKey 兼容 PKCS#1（RSA）、SEC1（EC）与 PKCS#8（RSA / ECDSA / Ed25519）PEM 编码，
// 返回 crypto.Signer 供 x509.CreateCertificate 用作 CA 签名者。
func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("ca: private key does not implement crypto.Signer")
	}
	return signer, nil
}
