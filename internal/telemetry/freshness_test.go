package telemetry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type readFunc func(context.Context) ([]domain.Heartbeat, error)

func (f readFunc) Heartbeats(ctx context.Context) ([]domain.Heartbeat, error) { return f(ctx) }
func heartbeats(at time.Time) []domain.Heartbeat {
	return []domain.Heartbeat{{Name: "order", At: &at}, {Name: "checkout", At: &at}, {Name: "payment", At: &at}}
}
func fixture(t *testing.T, reader heartbeatReader, now func() time.Time) (*freshnessSampler, *sdkmetric.ManualReader) {
	t.Helper()
	mr := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
	t.Cleanup(func() {
		if err := mp.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	s, err := newFreshness(reader, mp.Meter(scope), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.registration.Unregister(); err != nil {
			t.Error(err)
		}
	})
	return s, mr
}
func collect(t *testing.T, mr *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := mr.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	result := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			result[m.Name] = m
		}
	}
	return result
}
func assertState(t *testing.T, m map[string]metricdata.Metrics, state string, lag *float64) {
	t.Helper()
	if len(m) > 3 {
		t.Fatalf("unexpected instruments: %v", m)
	}
	metric := m["analytics.source.state"]
	gauge, ok := metric.Data.(metricdata.Gauge[int64])
	if !ok || metric.Unit != "1" || len(gauge.DataPoints) != 12 {
		t.Fatalf("state metric: %+v", metric)
	}
	total := int64(0)
	for _, p := range gauge.DataPoints {
		if p.Attributes.Len() != 2 {
			t.Fatal(p.Attributes)
		}
		label, _ := p.Attributes.Value("analytics.source.state")
		want := int64(0)
		if label.AsString() == state {
			want = 1
		}
		if p.Value != want {
			t.Fatalf("state %s got %d want %d", label.AsString(), p.Value, want)
		}
		total += p.Value
	}
	if total != 3 {
		t.Fatal(total)
	}
	metric = m["analytics.source.lag"]
	if lag == nil {
		if metric.Name != "" {
			t.Fatalf("unknown must omit lag: %+v", metric)
		}
		return
	}
	values, ok := metric.Data.(metricdata.Gauge[float64])
	if !ok || metric.Unit != "s" || len(values.DataPoints) != 3 {
		t.Fatalf("lag metric: %+v", metric)
	}
	for _, p := range values.DataPoints {
		if p.Value != *lag || p.Attributes.Len() != 1 {
			t.Fatal(p)
		}
	}
}

func TestFreshnessMetricsIdleBoundariesAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		age   time.Duration
		state string
	}{{0, "healthy"}, {120 * time.Second, "healthy"}, {120*time.Second + time.Millisecond, "stale"}, {300 * time.Second, "stale"}, {300*time.Second + time.Millisecond, "unavailable"}} {
		t.Run(tc.age.String(), func(t *testing.T) {
			now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			calls := 0
			s, mr := fixture(t, readFunc(func(context.Context) ([]domain.Heartbeat, error) { calls++; return heartbeats(now.Add(-tc.age)), nil }), func() time.Time { return now })
			before := collect(t, mr)
			assertState(t, before, "unknown", nil)
			if _, ok := before["analytics.freshness.last_success.time"]; ok {
				t.Fatal("fabricated initial success")
			}
			s.sample(t.Context())
			lag := tc.age.Seconds()
			m := collect(t, mr)
			assertState(t, m, tc.state, &lag)
			last := m["analytics.freshness.last_success.time"]
			if last.Unit != "s" || last.Data.(metricdata.Gauge[float64]).DataPoints[0].Value != float64(now.Unix()) {
				t.Fatal(last)
			}
			now = now.Add(65 * time.Second)
			m = collect(t, mr)
			lag += 65
			got := m["analytics.source.lag"].Data.(metricdata.Gauge[float64]).DataPoints[0].Value
			if got != lag {
				t.Fatalf("lag froze: %v want %v", got, lag)
			}
			now = now.Add(time.Nanosecond)
			assertState(t, collect(t, mr), "unknown", nil)
			if calls != 1 {
				t.Fatalf("collection performed I/O %d", calls)
			}
		})
	}
}

func TestFreshnessInvalidAndRecovery(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "future", "zero", "nil", "unknown-source", "query"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			rows := heartbeats(now)
			var readErr error
			s, mr := fixture(t, readFunc(func(context.Context) ([]domain.Heartbeat, error) { return rows, readErr }), func() time.Time { return now })
			s.sample(t.Context())
			first := float64(now.Unix())
			now = now.Add(time.Second)
			switch kind {
			case "missing":
				rows = rows[:2]
			case "duplicate":
				rows[1] = rows[0]
			case "future":
				rows = heartbeats(now.Add(time.Nanosecond))
			case "zero":
				rows = heartbeats(time.Time{})
			case "nil":
				rows[0].At = nil
			case "unknown-source":
				rows[0].Name = "secret-tenant"
			case "query":
				readErr = errors.New("secret query")
			}
			s.sample(t.Context())
			m := collect(t, mr)
			assertState(t, m, "unknown", nil)
			if m["analytics.freshness.last_success.time"].Data.(metricdata.Gauge[float64]).DataPoints[0].Value != first {
				t.Fatal("advanced last success on failure")
			}
			rows = heartbeats(now)
			readErr = nil
			s.sample(t.Context())
			zero := 0.0
			assertState(t, collect(t, mr), "healthy", &zero)
		})
	}
}

func TestSamplerScheduleTimeoutAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		s, _ := fixture(t, readFunc(func(ctx context.Context) ([]domain.Heartbeat, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}), time.Now)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); s.run(ctx) }()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("missing immediate sample", calls.Load())
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if s.current.Load().valid {
			t.Fatal("timeout accepted")
		}
		time.Sleep(28 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("incorrect cadence", calls.Load())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("sampler leaked")
		}
		time.Sleep(time.Minute)
		if calls.Load() != 2 {
			t.Fatal("sampled after stop")
		}
	})
}

func TestStartFreshnessUnregisters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
		previous := otel.GetMeterProvider()
		otel.SetMeterProvider(mp)
		defer otel.SetMeterProvider(previous)
		defer func() {
			if err := mp.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		stop, err := StartFreshness(t.Context(), readFunc(func(context.Context) ([]domain.Heartbeat, error) { return heartbeats(time.Now()), nil }))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		zero := 0.0
		assertState(t, collect(t, mr), "healthy", &zero)
		if err := stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(collect(t, mr)) != 0 {
			t.Fatal("callback retained after shutdown")
		}
	})
}

func TestSamplerDoesNotOverlapSlowReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		release := make(chan struct{})
		s, _ := fixture(t, readFunc(func(context.Context) ([]domain.Heartbeat, error) {
			calls.Add(1)
			<-release // Simulate a driver that does not promptly honor cancellation.
			return heartbeats(time.Now()), nil
		}), time.Now)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); s.run(ctx) }()
		synctest.Wait()
		time.Sleep(90 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("overlapping reads", calls.Load())
		}
		cancel()
		close(release)
		<-done
		if s.current.Load().valid {
			t.Fatal("accepted result after deadline/cancellation")
		}
	})
}
