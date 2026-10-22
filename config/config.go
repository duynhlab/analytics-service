// Package config validates the service's explicit runtime dependencies.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/duynhlab/pkg/authmw"
)

type Config struct {
	Listen           string
	ClickHouse       *clickhouse.Options
	ClickHouseTLS    bool
	Auth             authmw.Config
	LogLevel         string
	ProfilingEnabled bool
}

func Load() (Config, error) { return load(os.Getenv) }
func load(get func(string) string) (Config, error) {
	var cfg Config
	// CLICKHOUSE_TLS only governs the ClickHouse hop. It defaults to on; an
	// explicit "false" is for in-cluster deployments whose ClickHouse has no TLS
	// listener, where the network fence is the control (ADR-069 Adoption).
	if get("ANALYTICS_ALLOW_INSECURE_LOCAL") != "" {
		// Removed: a stale value must not silently become "TLS on" or "off".
		return cfg, fmt.Errorf("ANALYTICS_ALLOW_INSECURE_LOCAL was replaced by CLICKHOUSE_TLS")
	}
	tlsFlag := get("CLICKHOUSE_TLS")
	if tlsFlag != "" && tlsFlag != "true" && tlsFlag != "false" {
		return cfg, fmt.Errorf("CLICKHOUSE_TLS must be true or false")
	}
	cfg.ClickHouseTLS = tlsFlag != "false"
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
	if path := get("CLICKHOUSE_CA_FILE"); path != "" && !cfg.ClickHouseTLS {
		// A CA with TLS off is a contradiction: refuse it rather than carry a
		// certificate that nothing verifies.
		return cfg, fmt.Errorf("CLICKHOUSE_CA_FILE requires CLICKHOUSE_TLS=true")
	}
	if cfg.ClickHouseTLS {
		cfg.ClickHouse.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		if path := get("CLICKHOUSE_CA_FILE"); path != "" {
			cert, err := os.ReadFile(filepath.Clean(path))
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
	if !validURL(issuer) || !strings.HasSuffix(issuer, "/realms/duynhlab-staff") {
		return cfg, fmt.Errorf("OIDC_STAFF_ISSUER must name the duynhlab-staff realm")
	}
	if jwks != "" && !validURL(jwks) {
		return cfg, fmt.Errorf("invalid OIDC_STAFF_JWKS_URL")
	}
	audience := get("OIDC_AUDIENCE")
	if audience == "" {
		audience = "duynhlab-platform"
	}
	cfg.Auth = authmw.Config{Issuer: issuer, Audience: audience, JWKSURL: jwks, RequiredAlgorithm: "RS256", RolesClaimPath: "realm_access.roles"}
	cfg.LogLevel = get("LOG_LEVEL")
	// Off by default: obsx reads PYROSCOPE_ENDPOINT itself when enabled.
	switch get("PROFILING_ENABLED") {
	case "", "false":
	case "true":
		cfg.ProfilingEnabled = true
	default:
		return cfg, fmt.Errorf("PROFILING_ENABLED must be true or false")
	}
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

// validURL accepts https anywhere, and http only where the fleet already
// trusts plaintext: the cluster-internal Keycloak Service, the loopback
// addresses and a bare Compose service name (no dot). The staff issuer is a
// public https host in every environment; the JWKS fetch is the in-cluster
// hop (see OIDC_STAFF_JWKS_URL in the deployment docs).
func validURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return plaintextHost(u.Hostname())
	default:
		return false
	}
}

func plaintextHost(host string) bool {
	switch {
	case host == "localhost", host == "127.0.0.1", host == "::1":
		return true
	case strings.HasSuffix(host, ".svc.cluster.local"):
		return true
	case !strings.Contains(host, ".") && net.ParseIP(host) == nil:
		return true // a Compose service name
	}
	return false
}
