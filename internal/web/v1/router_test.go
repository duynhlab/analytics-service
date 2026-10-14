package v1

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"github.com/duynhlab/pkg/authmw"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type serviceStub struct {
	calls int
	err   error
}

func (s *serviceStub) Overview(ctx context.Context, f domain.Filters) (domain.Overview, error) {
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing deadline")
	}
	if ctx.Err() != nil {
		return domain.Overview{}, ctx.Err()
	}
	return domain.Overview{Filters: f}, s.err
}
func (s *serviceStub) Ready(context.Context) error { return s.err }
func TestDirectBackendAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer jwks.Close()
	const issuer = "https://staff.example/realms/duynhlab-staff"
	v, err := authmw.NewVerifier(authmw.Config{Issuer: issuer, Audience: "duynhlab-platform", JWKSURL: jwks.URL})
	if err != nil {
		t.Fatal(err)
	}
	service := &serviceStub{}
	router := New(service, v, nil)
	token := func(change func(jwt.MapClaims)) string {
		claims := jwt.MapClaims{"iss": issuer, "aud": "duynhlab-platform", "sub": "staff-42", "exp": time.Now().Add(time.Hour).Unix(), "realm_access": map[string]any{"roles": []string{"backoffice_admin"}}}
		if change != nil {
			change(claims)
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "test"
		value, e := tok.SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}

	valid := token(nil)
	forgedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(valid, jwt.MapClaims{})
	if err != nil {
		t.Fatal(err)
	}
	forged, err := parsed.SignedString(forgedKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongAlgToken := jwt.NewWithClaims(jwt.SigningMethodHS256, parsed.Claims)
	wrongAlgToken.Header["kid"] = "test"
	wrongAlg, err := wrongAlgToken.SignedString([]byte("test-only-hmac-key"))
	if err != nil {
		t.Fatal(err)
	}

	const route = "/analytics/v1/protected/commerce/overview"
	const query = "?currency=USD&from=2026-10-01&to=2026-10-03"
	tests := []struct {
		name, bearer, query string
		status              int
	}{
		{"staff", valid, query, 200},
		{"bad signature", forged, query, 401},
		{"wrong algorithm", wrongAlg, query, 401},
		{"missing", "", query, 401},
		{"malformed", "not.jwt", query, 401},
		{"customer issuer", token(func(c jwt.MapClaims) { c["iss"] = "https://staff.example/realms/duynhlab" }), query, 401},
		{"wrong audience", token(func(c jwt.MapClaims) { c["aud"] = "other" }), query, 401},
		{"expired", token(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), query, 401},
		{"no expiration", token(func(c jwt.MapClaims) { delete(c, "exp") }), query, 401},
		{"empty subject", token(func(c jwt.MapClaims) { c["sub"] = "" }), query, 401},
		{"staff without role", token(func(c jwt.MapClaims) { delete(c, "realm_access") }), query, 403},
		{"invalid filter", valid, "?currency=VND&from=2026-10-01&to=2026-10-03", 400},
		{"duplicate", valid, query + "&currency=USD", 400},
		{"unknown", valid, query + "&owner=42", 400},
		{"malformed escape", valid, query + "&bad=%zz", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := service.calls
			r := httptest.NewRequest(http.MethodGet, route+tt.query, nil)
			if tt.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tt.bearer)
			}
			r.Header.Set("X-User-Role", "backoffice_admin")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tt.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			if tt.status != 200 && service.calls != before {
				t.Fatal("unauthorized/invalid request reached logic")
			}
			if tt.status != 200 {
				var envelope map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope["code"] == "" || envelope["error"] == "" {
					t.Fatal("not shared envelope")
				}
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceledRequest := httptest.NewRequest(http.MethodGet, route+query, nil).WithContext(ctx)
	canceledRequest.Header.Set("Authorization", "Bearer "+valid)
	canceledResponse := httptest.NewRecorder()
	router.ServeHTTP(canceledResponse, canceledRequest)
	if canceledResponse.Code != 503 {
		t.Fatal("canceled request succeeded", canceledResponse.Code)
	}
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != 200 {
		t.Fatal("healthy readiness failed", ready.Code)
	}
	service.err = errors.New("secret DSN and SQL")
	r := httptest.NewRequest(http.MethodGet, route+query, nil)
	r.Header.Set("Authorization", "Bearer "+valid)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tt := range []struct {
		path   string
		status int
	}{{"/health", 200}, {"/ready", 503}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if w.Code != tt.status {
			t.Fatal(tt, w.Code)
		}
	}
}

func TestJWKSUnavailable(t *testing.T) {
	for _, slow := range []bool{false, true} {
		name := "down"
		if slow {
			name = "delayed outage"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case entered <- struct{}{}:
				default:
				}
				if slow {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				http.Error(w, "sensitive upstream error", http.StatusServiceUnavailable)
			}))
			defer jwks.Close()
			// Bound the fixture even if construction fails unexpectedly.
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			cfg := authmw.Config{Issuer: "https://staff.example/realms/duynhlab-staff", Audience: "duynhlab-platform", JWKSURL: jwks.URL}
			type result struct {
				v   *authmw.Verifier
				err error
			}
			built := make(chan result, 1)
			go func() { v, e := authmw.NewVerifier(cfg); built <- result{v, e} }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("JWKS not requested")
			}
			close(release)
			var got result
			select {
			case got = <-built:
			case <-time.After(3 * time.Second):
				t.Fatal("verifier construction hung")
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": cfg.Issuer, "aud": cfg.Audience, "sub": "staff", "exp": time.Now().Add(time.Hour).Unix()})
			tok.Header["kid"] = "unavailable"
			bearer, err := tok.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			service := &serviceStub{}
			router := New(service, got.v, nil)
			r := httptest.NewRequest(http.MethodGet, "/analytics/v1/protected/commerce/overview?currency=USD&from=2026-10-01&to=2026-10-03", nil)
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 503 || service.calls != 0 || strings.Contains(w.Body.String(), "sensitive") || !strings.Contains(w.Body.String(), "AUTH_UNAVAILABLE") {
				t.Fatal(w.Code, w.Body.String(), service.calls)
			}
		})
	}
}
