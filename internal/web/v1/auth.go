package v1

import (
	"encoding/json"
	"net/http"

	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/httpx"
	"github.com/gin-gonic/gin"
)

// authmw v0.38 still emits {error} for JWT failures. Add the stable code at
// that boundary while retaining its signature/issuer/audience verification.
func staffJWT(verifier *authmw.Verifier) gin.HandlerFunc {
	verify := authmw.MiddlewareJWT(verifier)
	return func(c *gin.Context) {
		original := c.Writer
		c.Writer = &authWriter{ResponseWriter: original}
		defer func() { c.Writer = original }()
		verify(c)
	}
}

type authWriter struct{ gin.ResponseWriter }

func (w *authWriter) Write(data []byte) (int, error) {
	status := w.Status()
	if status == http.StatusUnauthorized || status == http.StatusServiceUnavailable {
		var body map[string]any
		if json.Unmarshal(data, &body) == nil && body["code"] == nil {
			code := httpx.CodeUnauthorized
			if status == http.StatusServiceUnavailable {
				code = "AUTH_UNAVAILABLE"
			}
			body["code"] = code
			updated, err := json.Marshal(body)
			if err != nil {
				return 0, err
			}
			w.Header().Del("Content-Length")
			if _, err = w.ResponseWriter.Write(updated); err != nil {
				return 0, err
			}
			return len(data), nil
		}
	}
	return w.ResponseWriter.Write(data)
}
