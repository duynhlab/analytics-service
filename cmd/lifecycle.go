package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

type cleanupStep struct {
	message string
	close   func(context.Context) error
}
type cleanupStack []cleanupStep

func (c *cleanupStack) add(message string, close func(context.Context) error) {
	*c = append(*c, cleanupStep{message, close})
}
func (c *cleanupStack) close() error {
	var result error
	for i := len(*c) - 1; i >= 0; i-- {
		// Cleanup must still run after a signal or failed startup. Errors are static:
		// exporters/drivers may put credentials or payloads in their error text.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if (*c)[i].close(ctx) != nil {
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
	defer func() { err = errors.Join(err, cleanups.close()) }()
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
