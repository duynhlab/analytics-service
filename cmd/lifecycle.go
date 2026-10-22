package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/duynhlab/pkg/logger/slogx"
)

type cleanupStep struct {
	message string
	close   func(context.Context, error) error
}
type cleanupStack []cleanupStep

func (c *cleanupStack) add(message string, close func(context.Context) error) {
	*c = append(*c, cleanupStep{message, func(ctx context.Context, _ error) error { return close(ctx) }})
}

// trackProcess runs after application resources close and before telemetry flushes.
func (c *cleanupStack) trackProcess(logger *slogx.Logger) {
	*c = append(*c, cleanupStep{"process stop logging failed", func(ctx context.Context, result error) error {
		outcome := slogx.OutcomeGraceful
		if result != nil {
			outcome = slogx.OutcomeError
		}
		logger.ProcessStopped(ctx, slogx.ComponentAPI, outcome)
		return nil
	}})
}

func (c *cleanupStack) close(result error) error {
	for i := len(*c) - 1; i >= 0; i-- {
		// Cleanup must still run after a signal or failed startup. Errors are static:
		// exporters/drivers may put credentials or payloads in their error text.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if (*c)[i].close(ctx, result) != nil {
			result = errors.Join(result, errors.New((*c)[i].message))
		}
		cancel()
	}
	*c = nil
	return result
}

type bootstrapFunc func(context.Context, *cleanupStack) (*http.Server, net.Listener, error)

func runApplication(ctx context.Context, boot bootstrapFunc, drain time.Duration) (err error) {
	var cleanups cleanupStack
	defer func() { err = cleanups.close(err) }()
	if ctx.Err() != nil {
		return nil
	}
	server, listener, err := boot(ctx, &cleanups)
	if err != nil {
		return err
	}
	return serve(ctx, server, listener, drain)
}
func serve(ctx context.Context, server *http.Server, listener net.Listener, drain time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var result error
	served := false
	select {
	case err := <-done:
		served = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("HTTP server failed")
		}
	case <-ctx.Done():
	}
	// Drain on both signal and listener failure before releasing dependencies.
	shutdown, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if server.Shutdown(shutdown) != nil {
		result = errors.Join(result, errors.New("HTTP shutdown deadline exceeded"))
		if server.Close() != nil {
			result = errors.Join(result, errors.New("HTTP force close failed"))
		}
	}
	if !served {
		<-done
	}
	return result
}
