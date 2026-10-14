package config

import "testing"

func TestConfigSecurity(t *testing.T) {
	base := map[string]string{"CLICKHOUSE_ADDR": "clickhouse:9440", "CLICKHOUSE_USER": "analytics_read", "CLICKHOUSE_PASSWORD": "test", "OIDC_STAFF_ISSUER": "https://auth.example/realms/duynhlab-staff"}
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
