package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOverviewFailureReasons(t *testing.T) {
	for _, reason := range []string{"freshness", "query", "timeout", "limits", "validation", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			defer func() {
				otel.SetTracerProvider(previous)
				if err := tp.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			reader := &fakeReader{heartbeats: beats(testNow)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch reason {
			case "freshness":
				reader.heartbeats = nil
			case "cancelled":
				cancel()
			default:
				reader.err = domain.Unavailable(reason)
			}
			service, err := New(reader, func() time.Time { return testNow })
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Overview(ctx, testFilters)
			if !errors.Is(err, domain.ErrUnavailable) || err.Error() != domain.ErrUnavailable.Error() {
				t.Fatal("public error changed", err)
			}
			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatal(len(spans))
			}
			span := spans[0]
			found := false
			for _, a := range span.Attributes() {
				if string(a.Key) == "analytics.unavailable.reason" {
					found = a.Value.AsString() == reason
				}
			}
			if !found {
				t.Fatal("missing bounded reason", span.Attributes())
			}
			if (span.Status().Code == codes.Error) != (reason != "cancelled") {
				t.Fatal(span.Status())
			}
			if len(span.Events()) != 0 {
				t.Fatal("unexpected error payload")
			}
		})
	}
}
