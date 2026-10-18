package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigSecurity(t *testing.T) {
	base := map[string]string{"CLICKHOUSE_ADDR": "clickhouse:9440", "CLICKHOUSE_USER": "analytics_reader", "CLICKHOUSE_PASSWORD": "test", "OIDC_STAFF_ISSUER": "https://auth.example/realms/duynhlab-staff"}
	get := func(k string) string { return base[k] }
	cfg, err := load(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouse.TLS == nil || cfg.ClickHouse.TLS.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
	for _, tt := range []struct{ key, value string }{{"CLICKHOUSE_USER", "default"}, {"CLICKHOUSE_PASSWORD", ""}, {"CLICKHOUSE_ADDR", "http://db:8123"}, {"HTTP_ADDR", ":0"}, {"OIDC_STAFF_ISSUER", "https://auth.example/realms/duynhlab"}, {"OIDC_STAFF_JWKS_URL", "http://keys.example/certs"}, {"ANALYTICS_ALLOW_INSECURE_LOCAL", "yes"}, {"CLICKHOUSE_CA_FILE", "/does-not-exist"}} {
		t.Run(tt.key, func(t *testing.T) {
			old, exists := base[tt.key]
			base[tt.key] = tt.value
			t.Cleanup(func() {
				if exists {
					base[tt.key] = old
				} else {
					delete(base, tt.key)
				}
			})
			if _, err := load(get); err == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
	base["ANALYTICS_ALLOW_INSECURE_LOCAL"] = "true"
	base["OIDC_STAFF_ISSUER"] = "http://localhost:8081/realms/duynhlab-staff"
	cfg, err = load(get)
	if err != nil || cfg.ClickHouse.TLS != nil {
		t.Fatal("local mode", err)
	}
}

func TestCustomCA(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		data  []byte
		valid bool
	}{{"valid", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), true}, {"invalid", []byte("sensitive invalid CA content"), false}} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(path, tt.data, 0600); err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"CLICKHOUSE_ADDR": "clickhouse:9440", "CLICKHOUSE_USER": "analytics_reader", "CLICKHOUSE_PASSWORD": "test", "OIDC_STAFF_ISSUER": "https://auth.example/realms/duynhlab-staff", "CLICKHOUSE_CA_FILE": path}
			cfg, err := load(func(k string) string { return values[k] })
			if !tt.valid {
				if err == nil || strings.Contains(err.Error(), "sensitive") {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = cert.Verify(x509.VerifyOptions{Roots: cfg.ClickHouse.TLS.RootCAs}); err != nil {
				t.Fatal(err)
			}
			if cfg.ClickHouse.TLS.InsecureSkipVerify {
				t.Fatal("CA disabled TLS verification")
			}
		})
	}
}
