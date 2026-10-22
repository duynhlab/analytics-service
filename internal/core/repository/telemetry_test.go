package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/duynhlab/analytics-service/internal/core/domain"
	"github.com/duynhlab/pkg/obsx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestQueryTelemetry(t *testing.T) {
	for _, kind := range []string{"success", "query", "scan", "close", "limits", "validation", "cancelled", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				spans := tracetest.NewSpanRecorder()
				tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
				mr := sdkmetric.NewManualReader()
				mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
				oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
				otel.SetTracerProvider(tp)
				otel.SetMeterProvider(mp)
				defer func() {
					otel.SetTracerProvider(oldT)
					otel.SetMeterProvider(oldM)
					if err := mp.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
					if err := tp.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				ctx, parent := tp.Tracer("test").Start(t.Context(), "parent")
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				secret := errors.New("private SQL password customer@example.com")
				rows := &testRows{}
				if kind == "close" {
					rows.closeErr = secret
				}
				var client trace.SpanContext
				repo, err := New(queryConn{query: func(ctx context.Context) (driver.Rows, error) {
					client = trace.SpanContextFromContext(ctx)
					if kind == "query" {
						return nil, secret
					}
					if kind == "limits" {
						return nil, &clickhouse.Exception{Code: int32(proto.ErrMemoryLimitExceeded), Message: secret.Error()}
					}
					if kind == "timeout" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return rows, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				err = repo.query(ctx, "daily", "SELECT private SQL", clickhouse.Parameters{"secret": "customer@example.com"}, func(driver.Rows) error {
					time.Sleep(5 * time.Millisecond)
					if kind == "scan" {
						return secret
					}
					if kind == "validation" {
						return domain.ErrUnavailable
					}
					if kind == "cancelled" {
						cancel()
					}
					return nil
				})
				parent.End()
				got := spans.Ended()
				if len(got) != 2 {
					t.Fatalf("duplicate or missing spans: %d", len(got))
				}
				db := got[0]
				if db.Name() != "SELECT analytics.daily" || db.SpanKind() != trace.SpanKindClient || db.Parent().SpanID() != parent.SpanContext().SpanID() || client.SpanID() != db.SpanContext().SpanID() {
					t.Fatal("broken span ancestry or driver context")
				}
				wantReason := kind
				if kind == "scan" || kind == "close" {
					wantReason = "query"
				}
				if kind != "success" && domain.FailureReason(ctx, err) != wantReason {
					t.Fatalf("reason %v: %v", kind, err)
				}
				failed := kind != "success" && kind != "cancelled"
				if (db.Status().Code == codes.Error) != failed {
					t.Fatalf("wrong status: %s %+v", kind, db.Status())
				}
				if strings.Contains(fmt.Sprint(db.Attributes(), db.Events(), db.Status()), "private") || strings.Contains(fmt.Sprint(db.Attributes()), "customer@") {
					t.Fatal("sensitive telemetry")
				}
				var rm metricdata.ResourceMetrics
				if err := mr.Collect(t.Context(), &rm); err != nil {
					t.Fatal(err)
				}
				if len(rm.ScopeMetrics) != 1 || len(rm.ScopeMetrics[0].Metrics) != 1 {
					t.Fatal("duplicate metrics")
				}
				m := rm.ScopeMetrics[0].Metrics[0]
				h, ok := m.Data.(metricdata.Histogram[float64])
				if !ok || m.Name != "db.client.operation.duration" || m.Unit != "s" || len(h.DataPoints) != 1 {
					t.Fatalf("metric: %+v", m)
				}
				p := h.DataPoints[0]
				if !slices.Equal(p.Bounds, obsx.DBDurationBuckets) || p.Count != 1 {
					t.Fatal(p)
				}
				if kind == "success" && p.Sum < 0.005 {
					t.Fatal("duration excluded scan")
				}
				value, hasError := p.Attributes.Value("error.type")
				if hasError != failed || (failed && value.AsString() != wantReason) {
					t.Fatal("metric/span error mismatch", p.Attributes)
				}
				wantCount := 4
				if failed {
					wantCount++
				}
				if p.Attributes.Len() != wantCount {
					t.Fatal("unexpected metric dimension", p.Attributes)
				}
			})
		})
	}
}

func TestQuerySummaryIsBounded(t *testing.T) {
	for _, name := range []string{"heartbeats", "ready", "daily", "captured", "products", "funnel"} {
		if querySummary(name) != "SELECT analytics."+name {
			t.Fatal(name)
		}
	}
	if querySummary("secret tenant input") != "SELECT analytics" {
		t.Fatal("unbounded summary")
	}
}
