// Package config validates the service's explicit runtime dependencies.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/duynhlab/pkg/authmw"
)

type Config struct {
	Listen     string
	ClickHouse *clickhouse.Options
	Auth       authmw.Config
	LogLevel   string
}

func Load() (Config, error) { return load(os.Getenv) }
func load(get func(string) string) (Config, error) {
	var cfg Config
	local := get("ANALYTICS_ALLOW_INSECURE_LOCAL")
	if local != "" && local != "true" && local != "false" {
		return cfg, fmt.Errorf("ANALYTICS_ALLOW_INSECURE_LOCAL must be true or false")
	}
	insecure := local == "true"
	cfg.Listen = get("HTTP_ADDR")
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if err := address(cfg.Listen); err != nil {
		return cfg, fmt.Errorf("invalid HTTP_ADDR")
	}
	addr := get("CLICKHOUSE_ADDR")
	if err := address(addr); err != nil {
		return cfg, fmt.Errorf("CLICKHOUSE_ADDR must be host:port")
	}
	user, password := get("CLICKHOUSE_USER"), get("CLICKHOUSE_PASSWORD")
	if user != "analytics_reader" || password == "" {
		return cfg, fmt.Errorf("CLICKHOUSE_USER must be analytics_reader and CLICKHOUSE_PASSWORD is required")
	}
	cfg.ClickHouse = &clickhouse.Options{
		Addr: []string{addr}, Protocol: clickhouse.Native,
		Auth:        clickhouse.Auth{Database: "analytics", Username: user, Password: password},
		DialTimeout: time.Second, ReadTimeout: 2 * time.Second,
		MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: 5 * time.Minute,
	}
	if !insecure {
		cfg.ClickHouse.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		if path := get("CLICKHOUSE_CA_FILE"); path != "" {
			cert, err := os.ReadFile(path)
			if err != nil {
				return cfg, fmt.Errorf("cannot read CLICKHOUSE_CA_FILE")
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(cert) {
				return cfg, fmt.Errorf("invalid CLICKHOUSE_CA_FILE")
			}
			cfg.ClickHouse.TLS.RootCAs = roots
		}
	}
	issuer, jwks := get("OIDC_STAFF_ISSUER"), get("OIDC_STAFF_JWKS_URL")
	if !validURL(issuer, insecure) || !strings.HasSuffix(issuer, "/realms/duynhlab-staff") {
		return cfg, fmt.Errorf("OIDC_STAFF_ISSUER must name the duynhlab-staff realm")
	}
	if jwks != "" && !validURL(jwks, insecure) {
		return cfg, fmt.Errorf("invalid OIDC_STAFF_JWKS_URL")
	}
	audience := get("OIDC_AUDIENCE")
	if audience == "" {
		audience = "duynhlab-platform"
	}
	cfg.Auth = authmw.Config{Issuer: issuer, Audience: audience, JWKSURL: jwks, RequiredAlgorithm: "RS256", RolesClaimPath: "realm_access.roles"}
	cfg.LogLevel = get("LOG_LEVEL")
	return cfg, nil
}
func address(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port")
	}
	return nil
}
func validURL(value string, insecure bool) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || (insecure && u.Scheme == "http"))
}
