package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
)

var testNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
var testFilters = domain.Filters{Currency: "USD", From: "2026-10-01", To: "2026-10-03"}

func beats(at time.Time) []domain.Heartbeat {
	return []domain.Heartbeat{{Name: "order", At: &at}, {Name: "checkout", At: &at}, {Name: "payment", At: &at}}
}
func TestFreshness(t *testing.T) {
	for _, tt := range []struct {
		age          time.Duration
		valid, stale bool
	}{{0, true, false}, {120 * time.Second, true, false}, {120*time.Second + time.Nanosecond, true, true}, {300 * time.Second, true, true}, {300*time.Second + time.Nanosecond, false, false}, {-time.Nanosecond, false, false}} {
		t.Run(tt.age.String(), func(t *testing.T) {
			meta, err := freshness(beats(testNow.Add(-tt.age)), testNow)
			if (err == nil) != tt.valid {
				t.Fatal(err)
			}
			if tt.valid && meta.Stale != tt.stale {
				t.Fatal(meta)
			}
		})
	}
	for _, rows := range [][]domain.Heartbeat{nil, beats(time.Time{}), {{Name: "order"}, {Name: "checkout"}, {Name: "payment"}}, {{Name: "order", At: &testNow}, {Name: "order", At: &testNow}, {Name: "payment", At: &testNow}}} {
		if _, err := freshness(rows, testNow); err == nil {
			t.Fatal("accepted missing/duplicate heartbeat")
		}
	}
	rows := beats(testNow)
	older := testNow.Add(-30 * time.Second)
	rows[1].At = &older
	meta, err := freshness(rows, testNow)
	if err != nil || !meta.DataThrough.Equal(older) {
		t.Fatal(meta, err)
	}
}
func TestExactAssembly(t *testing.T) {
	facts := domain.Facts{Daily: []domain.Daily{{Date: "2026-10-01", Settled: "18014398509481987", Refunded: "20000000000000000"}}, CapturedOrders: 2}
	got, err := assemble(testFilters, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.KPI.Net != "-1985601490518013" || got.KPI.Average == nil || *got.KPI.Average != "9007199254740994" {
		t.Fatalf("wrong exact totals %+v", got.KPI)
	}
	if len(got.Trend) != 2 || got.Trend[1].Net != "0" || got.Products == nil {
		t.Fatal("missing zero day or empty array")
	}
	facts.Daily[0].Settled = "-3"
	got, err = assemble(testFilters, facts)
	if err != nil || *got.KPI.Average != "-2" {
		t.Fatal(got.KPI, err)
	}
	facts.CapturedOrders = 0
	got, err = assemble(testFilters, facts)
	if err != nil || got.KPI.Average != nil {
		t.Fatal("zero denominator")
	}
}
func TestAssemblyFailsClosed(t *testing.T) {
	tests := []domain.Facts{
		{CapturedOrders: domain.MaxSafeCount + 1},
		{Funnel: domain.Funnel{Started: domain.MaxSafeCount + 1}},
		{Daily: []domain.Daily{{Date: "2026-09-30", Settled: "0", Refunded: "0"}}},
		{Daily: []domain.Daily{{Date: "2026-10-01", Settled: "1.5", Refunded: "0"}}},
		{Daily: []domain.Daily{{Date: "2026-10-01", Settled: "0", Refunded: "-1"}}},
		{Daily: []domain.Daily{{Date: "2026-10-01"}, {Date: "2026-10-01"}}},
		{Products: []domain.Product{{Subtotal: "0", Quantity: domain.MaxSafeCount + 1}}},
	}
	for i, facts := range tests {
		if _, err := assemble(testFilters, facts); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	// Stages are independent observations, not a monotonically descending funnel.
	got, err := assemble(testFilters, domain.Facts{Funnel: domain.Funnel{Started: 2, Created: 2, Captured: 0, Completed: 2}})
	if err != nil || got.Funnel.Completed != 2 {
		t.Fatal(got, err)
	}
}

type fakeReader struct {
	heartbeats     []domain.Heartbeat
	err            error
	reads          int
	read           func(context.Context)
	heartbeatCalls int
	heartbeat      func()
}

func (f *fakeReader) Heartbeats(context.Context) ([]domain.Heartbeat, error) {
	f.heartbeatCalls++
	if f.heartbeat != nil {
		f.heartbeat()
	}
	return f.heartbeats, f.err
}
func (f *fakeReader) Read(ctx context.Context, _ domain.Filters) (domain.Facts, error) {
	f.reads++
	if f.read != nil {
		f.read(ctx)
	}
	return domain.Facts{}, ctx.Err()
}
func (f *fakeReader) Ready(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("no deadline")
	}
	return f.err
}
func TestServiceFreshnessAndCancellation(t *testing.T) {
	now := testNow
	r := &fakeReader{heartbeats: beats(now.Add(-301 * time.Second))}
	s, err := New(r, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Overview(context.Background(), testFilters); err == nil || r.reads != 0 {
		t.Fatal("queried stale data")
	}
	r.heartbeats = beats(now.Add(-299 * time.Second))
	r.read = func(ctx context.Context) {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 5*time.Second {
			t.Error("missing request limit")
		}
		now = now.Add(2 * time.Second)
	}
	if _, err = s.Overview(context.Background(), testFilters); err == nil {
		t.Fatal("did not age out heartbeat after query")
	}
	r.read = nil
	r.heartbeats = beats(now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Overview(ctx, testFilters); err == nil {
		t.Fatal("returned success after cancellation")
	}
	r.err = errors.New("secret SQL details")
	if _, err = s.Overview(context.Background(), testFilters); !errors.Is(err, domain.ErrUnavailable) || err.Error() != domain.ErrUnavailable.Error() {
		t.Fatal("leaked dependency error", err)
	}
	r.err = nil
	if err = s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationStopsFurtherReads(t *testing.T) {
	for _, name := range []string{"before heartbeat", "after heartbeat", "during facts"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &fakeReader{heartbeats: beats(testNow)}
			switch name {
			case "before heartbeat":
				cancel()
			case "after heartbeat":
				reader.heartbeat = cancel
			case "during facts":
				reader.read = func(context.Context) { cancel() }
			}
			s, err := New(reader, func() time.Time { return testNow })
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Overview(ctx, testFilters)
			if !errors.Is(err, domain.ErrUnavailable) {
				t.Fatal(err)
			}
			if name == "before heartbeat" && reader.heartbeatCalls != 0 {
				t.Fatal("queried heartbeat after cancel")
			}
			if name != "during facts" && reader.reads != 0 {
				t.Fatal("read facts after cancel")
			}
		})
	}
}
