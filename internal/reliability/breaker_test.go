package reliability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestCircuitBreakerFailureRecoveryLifecycle validates closed, open, half-open, and recovered transitions.
func TestCircuitBreakerFailureRecoveryLifecycle(t *testing.T) {
	clock := newManualClock()
	breaker := NewCircuitBreaker(
		BreakerConfig{FailureThreshold: 2, OpenTimeout: 5 * time.Second},
		WithBreakerClock(clock),
	)
	dependencyErr := errors.New("dependency unavailable")
	var calls atomic.Int32
	fail := func(context.Context) error {
		calls.Add(1)
		return dependencyErr
	}

	for range 2 {
		if err := breaker.Execute(context.Background(), fail); !errors.Is(err, dependencyErr) {
			t.Fatalf("got %v, want dependency error", err)
		}
	}
	if state := breaker.State(); state != StateOpen {
		t.Fatalf("state = %s, want open", state)
	}
	if err := breaker.Execute(context.Background(), fail); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("open circuit returned %v", err)
	}
	if calls.Load() != 2 {
		t.Fatal("open circuit should fail without invoking dependency")
	}

	clock.Advance(5 * time.Second)
	if state := breaker.State(); state != StateHalfOpen {
		t.Fatalf("state = %s, want half-open", state)
	}
	if err := breaker.Execute(context.Background(), func(context.Context) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("recovery probe failed: %v", err)
	}
	if state := breaker.State(); state != StateClosed {
		t.Fatalf("state = %s, want closed", state)
	}
}

// TestCircuitBreakerHalfOpenAllowsOneProbe validates probe exclusivity and re-opening after a failed probe.
func TestCircuitBreakerHalfOpenAllowsOneProbe(t *testing.T) {
	clock := newManualClock()
	breaker := NewCircuitBreaker(
		BreakerConfig{FailureThreshold: 1, OpenTimeout: time.Second},
		WithBreakerClock(clock),
	)
	dependencyErr := errors.New("dependency unavailable")
	if err := breaker.Execute(context.Background(), func(context.Context) error { return dependencyErr }); err == nil {
		t.Fatal("opening failure should be returned")
	}
	clock.Advance(time.Second)

	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- breaker.Execute(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return dependencyErr
		})
	}()
	<-started

	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second half-open request returned %v, want ErrCircuitOpen", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, dependencyErr) {
		t.Fatalf("probe returned %v", err)
	}
	if state := breaker.State(); state != StateOpen {
		t.Fatalf("state = %s after failed probe, want open", state)
	}
}

// TestCircuitBreakerIgnoresStaleCompletion validates that an older in-flight success cannot close a newly opened circuit.
func TestCircuitBreakerIgnoresStaleCompletion(t *testing.T) {
	breaker := NewCircuitBreaker(BreakerConfig{FailureThreshold: 1, OpenTimeout: time.Minute})
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- breaker.Execute(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	dependencyErr := errors.New("failure")
	if err := breaker.Execute(context.Background(), func(context.Context) error { return dependencyErr }); err == nil {
		t.Fatal("opening failure should be returned")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("older operation returned %v", err)
	}
	if state := breaker.State(); state != StateOpen {
		t.Fatalf("stale success changed state to %s, want open", state)
	}
}

// TestCircuitBreakerResetClearsStateAndRejectsStaleOutcome validates demo reset semantics during in-flight work.
func TestCircuitBreakerResetClearsStateAndRejectsStaleOutcome(t *testing.T) {
	breaker := NewCircuitBreaker(BreakerConfig{FailureThreshold: 2, OpenTimeout: time.Minute})
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	dependencyErr := errors.New("late failure")
	go func() {
		result <- breaker.Execute(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return dependencyErr
		})
	}()
	<-started

	if err := breaker.Execute(context.Background(), func(context.Context) error { return dependencyErr }); err == nil {
		t.Fatal("pre-reset failure should be returned")
	}
	if failures := breaker.ConsecutiveFailures(); failures != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
	breaker.Reset()
	close(release)
	if err := <-result; !errors.Is(err, dependencyErr) {
		t.Fatalf("in-flight request returned %v", err)
	}
	if state := breaker.State(); state != StateClosed {
		t.Fatalf("state = %s, want closed", state)
	}
	if failures := breaker.ConsecutiveFailures(); failures != 0 {
		t.Fatalf("stale outcome restored %d failures", failures)
	}
}

// TestCircuitStateString validates stable human-readable labels for logs and metrics.
func TestCircuitStateString(t *testing.T) {
	tests := map[CircuitState]string{
		StateClosed:      "closed",
		StateOpen:        "open",
		StateHalfOpen:    "half-open",
		CircuitState(99): "unknown",
	}
	for state, want := range tests {
		if got := state.String(); got != want {
			t.Fatalf("state %d string = %q, want %q", state, got, want)
		}
	}
}
