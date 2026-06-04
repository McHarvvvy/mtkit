package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// TestLoad_NonRSACAKeys 保护"静态 CA 支持 RSA 之外的算法"——Bundle.rootKey 是
// crypto.Signer 而非 *rsa.PrivateKey。ECDSA / Ed25519 CA 应能被 Load 接受并签发叶证书。
func TestLoad_NonRSACAKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		gen  func(t *testing.T) (certPEM, keyPEM []byte)
	}{
		{"ecdsa-p256-sec1", genECDSACA},
		{"ed25519-pkcs8", genEd25519CA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			certPEM, keyPEM := tc.gen(t)
			b, err := Load(certPEM, keyPEM)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			leaf, err := b.CertFor("example.test")
			if err != nil {
				t.Fatalf("CertFor: %v", err)
			}
			if leaf == nil || len(leaf.Certificate) == 0 {
				t.Fatalf("expected a signed leaf certificate, got empty")
			}
		})
	}
}

// caTemplate 返回一份自签 CA 证书模板。
func caTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "faked-ca-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
}

func selfSign(t *testing.T, tmpl *x509.Certificate, pub, priv any) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func genECDSACA(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa keygen: %v", err)
	}
	certPEM = selfSign(t, caTemplate(), &key.PublicKey, key)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func genEd25519CA(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	certPEM = selfSign(t, caTemplate(), pub, priv)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8 key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	// 触碰一下 crypto.Signer 断言路径，确保返回类型契约不回退。
	var _ crypto.Signer = priv
	return certPEM, keyPEM
}
