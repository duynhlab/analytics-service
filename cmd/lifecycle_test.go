package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartupCleanup(t *testing.T) {
	var order []string
	failure := errors.New("startup failed")
	err := runApplication(t.Context(), func(_ context.Context, stack *cleanupStack) (*http.Server, net.Listener, error) {
		for _, name := range []string{"telemetry", "database"} {
			stack.add(name+" shutdown failed", func(ctx context.Context) error {
				if ctx.Err() != nil {
					t.Error("cleanup inherited cancellation")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("unbounded cleanup")
				}
				order = append(order, name)
				return errors.New("secret connection string")
			})
		}
		return nil, nil, failure
	}, time.Second)
	if !errors.Is(err, failure) || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "telemetry shutdown failed") || !strings.Contains(err.Error(), "database shutdown failed") {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"database", "telemetry"}) {
		t.Fatal(order)
	}
}

func TestCanceledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := runApplication(ctx, func(context.Context, *cleanupStack) (*http.Server, net.Listener, error) {
		t.Fatal("booted after cancellation")
		return nil, nil, nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPDrain(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "graceful"
		if force {
			name = "forced"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release, finished, shutting := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				if force {
					<-r.Context().Done()
				} else {
					<-release
					_, _ = io.WriteString(w, "drained")
				}
				close(finished)
			})}
			server.RegisterOnShutdown(func() { close(shutting) })
			done := make(chan error, 1)
			go func() {
				done <- runApplication(ctx, func(_ context.Context, s *cleanupStack) (*http.Server, net.Listener, error) {
					s.add("cleanup failed", func(context.Context) error {
						if !force {
							select {
							case <-finished:
							default:
								t.Error("dependencies closed before drain")
							}
						}
						return nil
					})
					return server, listener, nil
				}, 100*time.Millisecond)
			}()
			requestDone := make(chan error, 1)
			go func() {
				client := &http.Client{Timeout: 3 * time.Second}
				defer client.CloseIdleConnections()
				resp, e := client.Get("http://" + listener.Addr().String())
				if e == nil {
					_, e = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
				requestDone <- e
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not enter")
			}
			cancel()
			select {
			case <-shutting:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown did not start")
			}
			if !force {
				close(release)
			}
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown hung")
			}
			if force {
				if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("handler context not canceled")
			}
			if e := <-requestDone; !force && e != nil {
				t.Fatal(e)
			}
		})
	}
}

type failedListener struct{ net.Listener }

func (failedListener) Accept() (net.Conn, error) {
	return nil, errors.New("sensitive listener failure")
}
func (failedListener) Close() error { return nil }
func TestListenerFailure(t *testing.T) {
	err := serve(t.Context(), &http.Server{ReadHeaderTimeout: time.Second}, failedListener{}, time.Second)
	if err == nil || err.Error() != "HTTP server failed" {
		t.Fatal(err)
	}
}
