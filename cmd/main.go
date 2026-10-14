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
	web "github.com/duynhlab/analytics-service/internal/web/v1"
	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/logger/slogx"
	"github.com/duynhlab/pkg/obsx"
	"github.com/gin-gonic/gin"
)

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
	oc := obsx.ConfigFromEnv()
	oc.ServiceName = "analytics-service"
	obs, err := obsx.SetupObservability(ctx, oc)
	if err != nil {
		return errors.New("observability initialization failed")
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = obs.Shutdown(shutdown)
	}()
	logger := slogx.New(slogx.Config{Level: cfg.LogLevel, LoggerProvider: obs.LoggerProvider(), Flush: obs.ForceFlush})
	conn, err := clickhouse.Open(cfg.ClickHouse)
	if err != nil {
		return errors.New("ClickHouse initialization failed")
	}
	defer func() {
		if conn.Close() != nil {
			logger.Error(context.Background(), "ClickHouse pool close failed")
		}
	}()
	repo, err := repository.New(conn)
	if err != nil {
		return errors.New("repository initialization failed")
	}
	service, err := logic.New(repo, time.Now)
	if err != nil {
		return errors.New("service initialization failed")
	}
	verifier, err := authmw.NewVerifier(cfg.Auth)
	if err != nil {
		return errors.New("staff verifier initialization failed")
	}
	gin.SetMode(gin.ReleaseMode)
	server := &http.Server{Addr: cfg.Listen, Handler: web.New(service, verifier, logger.Slog()), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 7 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("HTTP listener failed")
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.ProcessStarted(ctx, "http")
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("HTTP shutdown deadline exceeded")
		}
		<-done
	}
	logger.ProcessStopped(context.Background(), "http", "success")
	return nil
}
