package reliability

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// ErrInvalidRetryPolicy indicates a retry policy that cannot bound execution.
var ErrInvalidRetryPolicy = errors.New("invalid retry policy")

// RetryPolicy controls bounded exponential backoff.
type RetryPolicy struct {
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Jitter    float64
	Retryable func(error) bool
	RandFloat func() float64
	Sleep     func(context.Context, time.Duration) error
}

// Retry executes operation at most policy.Attempts times.
func Retry(ctx context.Context, policy RetryPolicy, operation func(context.Context) error) error {
	_, err := RetryValue(ctx, policy, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, operation(ctx)
	})
	return err
}

// RetryValue executes a value-returning operation with the same bounded retry policy.
func RetryValue[T any](ctx context.Context, policy RetryPolicy, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := validateRetryPolicy(policy); err != nil {
		return zero, err
	}
	if policy.Retryable == nil {
		policy.Retryable = func(error) bool { return true }
	}
	if policy.RandFloat == nil {
		policy.RandFloat = rand.Float64
	}
	if policy.Sleep == nil {
		policy.Sleep = sleepContext
	}

	for attempt := 0; attempt < policy.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		value, err := operation(ctx)
		if err == nil {
			return value, nil
		}
		if attempt == policy.Attempts-1 || !policy.Retryable(err) {
			return zero, err
		}

		delay := retryDelay(policy, attempt)
		if err := policy.Sleep(ctx, delay); err != nil {
			return zero, err
		}
	}

	return zero, nil
}

func validateRetryPolicy(policy RetryPolicy) error {
	if policy.Attempts <= 0 || policy.BaseDelay < 0 || policy.MaxDelay < policy.BaseDelay || policy.Jitter < 0 || policy.Jitter > 1 {
		return ErrInvalidRetryPolicy
	}
	return nil
}

func retryDelay(policy RetryPolicy, retry int) time.Duration {
	delay := policy.BaseDelay
	for range retry {
		if delay >= policy.MaxDelay/2 {
			delay = policy.MaxDelay
			break
		}
		delay *= 2
	}

	if policy.Jitter > 0 && delay > 0 {
		factor := 1 + policy.Jitter*(2*policy.RandFloat()-1)
		delay = time.Duration(float64(delay) * factor)
	}
	return min(delay, policy.MaxDelay)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
