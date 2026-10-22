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

func baseEnv() map[string]string {
	return map[string]string{"CLICKHOUSE_ADDR": "clickhouse:9440", "CLICKHOUSE_USER": "analytics_reader", "CLICKHOUSE_PASSWORD": "test", "OIDC_STAFF_ISSUER": "https://auth.example/realms/duynhlab-staff"}
}

func TestConfigSecurity(t *testing.T) {
	base := baseEnv()
	get := func(k string) string { return base[k] }
	cfg, err := load(get)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClickHouseTLS || cfg.ClickHouse.TLS == nil || cfg.ClickHouse.TLS.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
	if cfg.ProfilingEnabled {
		t.Fatal("profiling must default to off")
	}
	for _, tt := range []struct{ key, value string }{
		{"CLICKHOUSE_USER", "default"}, {"CLICKHOUSE_PASSWORD", ""}, {"CLICKHOUSE_ADDR", "http://db:8123"}, {"HTTP_ADDR", ":0"},
		{"OIDC_STAFF_ISSUER", "https://auth.example/realms/duynhlab"},
		{"OIDC_STAFF_ISSUER", "http://auth.example/realms/duynhlab-staff"},
		{"OIDC_STAFF_JWKS_URL", "http://keys.example/certs"},
		{"OIDC_STAFF_JWKS_URL", "http://10.0.0.5/certs"},
		{"CLICKHOUSE_TLS", "yes"}, {"ANALYTICS_ALLOW_INSECURE_LOCAL", "false"}, {"PROFILING_ENABLED", "on"}, {"CLICKHOUSE_CA_FILE", "/does-not-exist"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
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
}

func TestClickHouseTLSOff(t *testing.T) {
	base := baseEnv()
	base["CLICKHOUSE_TLS"] = "false"
	cfg, err := load(func(k string) string { return base[k] })
	if err != nil || cfg.ClickHouseTLS || cfg.ClickHouse.TLS != nil {
		t.Fatal("TLS off", err)
	}
	// A CA file without TLS is a contradiction, not a silent no-op.
	base["CLICKHOUSE_CA_FILE"] = filepath.Join(t.TempDir(), "ca.pem")
	if _, err := load(func(k string) string { return base[k] }); err == nil || !strings.Contains(err.Error(), "CLICKHOUSE_TLS") {
		t.Fatal("CA accepted with TLS off", err)
	}
}

// Plaintext OIDC endpoints are allowed only where the fleet already trusts the
// network: in-cluster Services, loopback and bare Compose names. The flag that
// turns ClickHouse TLS off has no say in it.
func TestPlaintextOIDCHosts(t *testing.T) {
	for _, tt := range []struct {
		url   string
		allow bool
	}{
		{"http://keycloak.identity.svc.cluster.local:8080/realms/duynhlab-staff/protocol/openid-connect/certs", true},
		{"http://localhost:8081/realms/duynhlab-staff/protocol/openid-connect/certs", true},
		{"http://127.0.0.1:18081/certs", true},
		{"http://keycloak:8080/realms/duynhlab-staff/protocol/openid-connect/certs", true},
		{"http://keys.example/certs", false},
		{"http://10.0.0.5:8080/certs", false},
		{"http://user:pw@keycloak:8080/certs", false},
		{"ftp://keycloak/certs", false},
		{"https://keys.example/certs", true},
	} {
		t.Run(tt.url, func(t *testing.T) {
			base := baseEnv()
			base["OIDC_STAFF_JWKS_URL"] = tt.url
			_, err := load(func(k string) string { return base[k] })
			if (err == nil) != tt.allow {
				t.Fatalf("allow=%v err=%v", tt.allow, err)
			}
		})
	}
	base := baseEnv()
	base["OIDC_STAFF_ISSUER"] = "http://localhost:8081/realms/duynhlab-staff"
	if _, err := load(func(k string) string { return base[k] }); err != nil {
		t.Fatal("loopback issuer", err)
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
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			values := baseEnv()
			values["CLICKHOUSE_CA_FILE"] = path
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
