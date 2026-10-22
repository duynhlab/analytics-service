package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFailureReasonContract(t *testing.T) {
	for _, reason := range []string{"freshness", "query", "timeout", "limits", "validation", "cancelled"} {
		err := Unavailable(reason)
		if !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() || FailureReason(t.Context(), err) != reason {
			t.Fatal(reason, err)
		}
	}
	if FailureReason(t.Context(), Unavailable("secret SQL")) != "query" {
		t.Fatal("unbounded reason")
	}
	if FailureReason(t.Context(), ErrValidation) != "validation" {
		t.Fatal("validation")
	}
	if FailureReason(t.Context(), errors.New("secret SQL")) != "query" {
		t.Fatal("raw error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if FailureReason(ctx, Unavailable("query")) != "cancelled" {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if FailureReason(ctx, Unavailable("query")) != "timeout" {
		t.Fatal("timeout lost")
	}
	if FailureReason(t.Context(), context.Canceled) != "cancelled" || FailureReason(t.Context(), context.DeadlineExceeded) != "timeout" {
		t.Fatal("wrapped context outcome")
	}
}
