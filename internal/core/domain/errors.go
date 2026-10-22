package domain

import (
	"context"
	"errors"
)

// Unavailable preserves the public sentinel while carrying a bounded internal
// diagnostic. Raw driver errors must never be attached to this error.
func Unavailable(reason string) error {
	switch reason {
	case "freshness", "query", "timeout", "limits", "validation", "cancelled":
	default:
		reason = "query"
	}
	return unavailableError{reason: reason}
}

type unavailableError struct{ reason string }

func (e unavailableError) Error() string { return ErrUnavailable.Error() }
func (e unavailableError) Unwrap() error { return ErrUnavailable }

// FailureReason gives cancellation precedence over an underlying query error.
func FailureReason(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var failure unavailableError
	if errors.As(err, &failure) {
		return failure.reason
	}
	if errors.Is(err, ErrValidation) {
		return "validation"
	}
	return "query"
}
