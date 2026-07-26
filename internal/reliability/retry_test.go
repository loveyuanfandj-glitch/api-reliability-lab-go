package reliability

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetryValueUsesBoundedExponentialBackoff validates eventual success, returned values, jitter, and max-delay capping.
func TestRetryValueUsesBoundedExponentialBackoff(t *testing.T) {
	dependencyErr := errors.New("temporary")
	var attempts int
	var delays []time.Duration
	random := []float64{0, 1, 0.5}
	randomIndex := 0
	policy := RetryPolicy{
		Attempts:  4,
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  250 * time.Millisecond,
		Jitter:    0.5,
		RandFloat: func() float64 {
			value := random[randomIndex]
			randomIndex++
			return value
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}

	value, err := RetryValue(context.Background(), policy, func(context.Context) (string, error) {
		attempts++
		if attempts < 4 {
			return "", dependencyErr
		}
		return "reserved", nil
	})
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if value != "reserved" || attempts != 4 {
		t.Fatalf("value = %q, attempts = %d", value, attempts)
	}
	want := []time.Duration{50 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delay %d = %s, want %s", index, delays[index], want[index])
		}
	}
}

// TestRetryStopsAtAttemptBound validates that the final dependency error is returned with no trailing sleep.
func TestRetryStopsAtAttemptBound(t *testing.T) {
	dependencyErr := errors.New("still unavailable")
	var attempts, sleeps int
	err := Retry(context.Background(), RetryPolicy{
		Attempts:  3,
		BaseDelay: time.Millisecond,
		MaxDelay:  time.Second,
		Sleep: func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
	}, func(context.Context) error {
		attempts++
		return dependencyErr
	})

	if !errors.Is(err, dependencyErr) || attempts != 3 || sleeps != 2 {
		t.Fatalf("err = %v, attempts = %d, sleeps = %d", err, attempts, sleeps)
	}
}

// TestRetryHonorsRetryablePredicate validates that permanent failures stop immediately.
func TestRetryHonorsRetryablePredicate(t *testing.T) {
	permanentErr := errors.New("invalid request")
	var attempts int
	err := Retry(context.Background(), RetryPolicy{
		Attempts:  3,
		BaseDelay: time.Millisecond,
		MaxDelay:  time.Second,
		Retryable: func(err error) bool { return !errors.Is(err, permanentErr) },
	}, func(context.Context) error {
		attempts++
		return permanentErr
	})

	if !errors.Is(err, permanentErr) || attempts != 1 {
		t.Fatalf("err = %v, attempts = %d", err, attempts)
	}
}

// TestRetryReturnsContextCancellationDuringBackoff validates prompt cancellation instead of another attempt.
func TestRetryReturnsContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int
	err := Retry(ctx, RetryPolicy{
		Attempts:  3,
		BaseDelay: time.Second,
		MaxDelay:  time.Second,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}, func(context.Context) error {
		attempts++
		return errors.New("temporary")
	})

	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("err = %v, attempts = %d", err, attempts)
	}
}

// TestRetryRejectsInvalidPolicies validates all bounds that keep retry execution finite and delays meaningful.
func TestRetryRejectsInvalidPolicies(t *testing.T) {
	policies := []RetryPolicy{
		{Attempts: 0},
		{Attempts: 1, BaseDelay: -1, MaxDelay: time.Second},
		{Attempts: 1, BaseDelay: time.Second, MaxDelay: time.Millisecond},
		{Attempts: 1, Jitter: -0.1},
		{Attempts: 1, Jitter: 1.1},
	}
	for _, policy := range policies {
		err := Retry(context.Background(), policy, func(context.Context) error { return nil })
		if !errors.Is(err, ErrInvalidRetryPolicy) {
			t.Fatalf("policy %+v returned %v", policy, err)
		}
	}
}
