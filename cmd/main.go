package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/duynhlab/analytics-service/config"
	"github.com/duynhlab/analytics-service/internal/core/repository"
	logic "github.com/duynhlab/analytics-service/internal/logic/v1"
	"github.com/duynhlab/analytics-service/internal/telemetry"
	web "github.com/duynhlab/analytics-service/internal/web/v1"
	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/logger/slogx"
	"github.com/duynhlab/pkg/obsx"
	"github.com/gin-gonic/gin"
)

// version is set by the release build (-X main.version); SERVICE_VERSION in
// the environment wins because the deployment knows the image tag.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runApplication(ctx, func(ctx context.Context, cleanups *cleanupStack) (*http.Server, net.Listener, error) {
		return bootstrap(ctx, cfg, cleanups)
	}, 8*time.Second)
}

func bootstrap(ctx context.Context, cfg config.Config, cleanups *cleanupStack) (*http.Server, net.Listener, error) {
	oc := obsx.ConfigFromEnv()
	oc.ServiceName = "analytics-service"
	if oc.ServiceVersion == "" {
		oc.ServiceVersion = version
	}
	obs, err := obsx.SetupObservability(ctx, oc)
	if err != nil {
		return nil, nil, errors.New("observability initialization failed")
	}
	cleanups.add("telemetry shutdown failed", obs.Shutdown)

	logger := slogx.New(slogx.Config{Level: cfg.LogLevel, LoggerProvider: obs.LoggerProvider(), Flush: obs.ForceFlush})
	cleanups.trackProcess(logger)
	if cfg.ProfilingEnabled {
		stopProfiling, err := obsx.SetupProfiling()
		if err != nil {
			return nil, nil, errors.New("profiling initialization failed")
		}
		cleanups.add("profiling shutdown failed", stopProfiling)
	}
	if !cfg.ClickHouseTLS {
		// The in-cluster ClickHouse has no TLS listener; the fence is NetworkPolicy
		// plus the reader password. Say so once per start so it cannot go unnoticed.
		logger.Warn(ctx, "ClickHouse TLS is off (CLICKHOUSE_TLS=false)")
	}
	conn, err := clickhouse.Open(cfg.ClickHouse)
	if err != nil {
		return nil, nil, errors.New("ClickHouse initialization failed")
	}
	cleanups.add("ClickHouse pool close failed", func(context.Context) error { return conn.Close() })

	repo, err := repository.New(conn)
	if err != nil {
		return nil, nil, errors.New("repository initialization failed")
	}
	service, err := logic.New(repo, time.Now)
	if err != nil {
		return nil, nil, errors.New("service initialization failed")
	}
	stopFreshness, err := telemetry.StartFreshness(ctx, repo)
	if err != nil {
		return nil, nil, errors.New("freshness telemetry initialization failed")
	}
	cleanups.add("freshness telemetry shutdown failed", stopFreshness)
	verifier, err := authmw.NewVerifier(cfg.Auth)
	if err != nil {
		return nil, nil, errors.New("staff verifier initialization failed")
	}
	gin.SetMode(gin.ReleaseMode)
	server := &http.Server{Addr: cfg.Listen, Handler: web.New(service, verifier, logger.Slog()), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 7 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, nil, errors.New("HTTP listener failed")
	}
	logger.ProcessStarted(ctx, slogx.ComponentAPI)
	return server, listener, nil
}
