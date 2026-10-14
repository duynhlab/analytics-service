// Package v1 exposes the staff-only overview and process/dependency probes.
package v1

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/httpmw"
	"github.com/duynhlab/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type OverviewService interface {
	Overview(context.Context, domain.Filters) (domain.Overview, error)
	Ready(context.Context) error
}

func New(service OverviewService, verifier *authmw.Verifier, logger *slog.Logger) *gin.Engine {
	router := gin.New()
	// No proxy headers participate in authorization; JWT is always verified here.
	_ = router.SetTrustedProxies(nil)
	router.Use(httpmw.Tracing("analytics-service"), httpmw.Logging(logger), httpmw.Recovery(logger))
	router.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/ready", func(c *gin.Context) {
		if err := service.Ready(c.Request.Context()); err != nil {
			unavailable(c)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	protected := router.Group("/analytics/v1/protected", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, staffJWT(verifier), authmw.MiddlewareRequireRole("backoffice_admin"))
	protected.GET("/commerce/overview", func(c *gin.Context) {
		values, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			invalid(c)
			return
		}
		filters, err := domain.ParseFilters(values)
		if err != nil {
			invalid(c)
			return
		}
		result, err := service.Overview(c.Request.Context(), filters)
		if errors.Is(err, domain.ErrValidation) {
			invalid(c)
			return
		}
		if err != nil {
			unavailable(c)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	return router
}
func invalid(c *gin.Context) {
	httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "Require currency=USD and a UTC from/to interval of 1 to 90 days")
}
func unavailable(c *gin.Context) {
	httpx.RespondError(c, http.StatusServiceUnavailable, "ANALYTICS_UNAVAILABLE", "Analytics data is unavailable")
}
