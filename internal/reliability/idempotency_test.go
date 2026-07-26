package reliability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIdempotencyRegistryCoalescesConcurrentCalls validates exactly-once execution and identical results under contention.
func TestIdempotencyRegistryCoalescesConcurrentCalls(t *testing.T) {
	registry := NewIdempotencyRegistry[string](time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	operation := func(context.Context) (string, error) {
		if executions.Add(1) == 1 {
			close(started)
		}
		<-release
		return "order-123", nil
	}

	const callers = 50
	results := make(chan string, callers)
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := registry.Do(context.Background(), "tenant-a", "request-1", operation)
			results <- value
			errorsSeen <- err
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(results)
	close(errorsSeen)

	if executions.Load() != 1 {
		t.Fatalf("operation executed %d times, want 1", executions.Load())
	}
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("caller received error: %v", err)
		}
	}
	for result := range results {
		if result != "order-123" {
			t.Fatalf("result = %q, want order-123", result)
		}
	}
}

// TestIdempotencyRegistryScopesByTenantAndKey validates that neither tenant nor key can collide with another scope.
func TestIdempotencyRegistryScopesByTenantAndKey(t *testing.T) {
	registry := NewIdempotencyRegistry[int](time.Minute)
	var executions atomic.Int32
	operation := func(context.Context) (int, error) {
		return int(executions.Add(1)), nil
	}

	first, _ := registry.Do(context.Background(), "tenant-a", "same-key", operation)
	repeated, _ := registry.Do(context.Background(), "tenant-a", "same-key", operation)
	otherTenant, _ := registry.Do(context.Background(), "tenant-b", "same-key", operation)
	otherKey, _ := registry.Do(context.Background(), "tenant-a", "other-key", operation)

	if first != repeated {
		t.Fatalf("repeated result = %d, want cached %d", repeated, first)
	}
	if otherTenant == first || otherKey == first || executions.Load() != 3 {
		t.Fatalf("results were not independently scoped: %d, %d, %d", first, otherTenant, otherKey)
	}
}

// TestIdempotencyRegistryDoesNotCacheFailures validates that a failed leader can be retried successfully.
func TestIdempotencyRegistryDoesNotCacheFailures(t *testing.T) {
	registry := NewIdempotencyRegistry[string](time.Minute)
	dependencyErr := errors.New("dependency unavailable")
	var executions int
	operation := func(context.Context) (string, error) {
		executions++
		if executions == 1 {
			return "", dependencyErr
		}
		return "order-456", nil
	}

	if _, err := registry.Do(context.Background(), "tenant", "key", operation); !errors.Is(err, dependencyErr) {
		t.Fatalf("first call returned %v", err)
	}
	value, err := registry.Do(context.Background(), "tenant", "key", operation)
	if err != nil || value != "order-456" || executions != 2 {
		t.Fatalf("value = %q, err = %v, executions = %d", value, err, executions)
	}
}

// TestIdempotencyRegistryReportsReplayStatus validates creator, cached, and failed-attempt status values.
func TestIdempotencyRegistryReportsReplayStatus(t *testing.T) {
	registry := NewIdempotencyRegistry[string](time.Minute)
	var executions int
	operation := func(context.Context) (string, error) {
		executions++
		return "order-789", nil
	}

	value, replayed, err := registry.DoWithStatus(context.Background(), "tenant", "key", operation)
	if err != nil || replayed || value != "order-789" {
		t.Fatalf("creator result = %q, replayed = %t, err = %v", value, replayed, err)
	}
	value, replayed, err = registry.DoWithStatus(context.Background(), "tenant", "key", operation)
	if err != nil || !replayed || value != "order-789" || executions != 1 {
		t.Fatalf("cached result = %q, replayed = %t, err = %v, executions = %d", value, replayed, err, executions)
	}

	dependencyErr := errors.New("failed creator")
	_, replayed, err = registry.DoWithStatus(context.Background(), "tenant", "failed-key", func(context.Context) (string, error) {
		return "", dependencyErr
	})
	if !errors.Is(err, dependencyErr) || replayed {
		t.Fatalf("failed creator replayed = %t, err = %v", replayed, err)
	}
}

// TestIdempotencyRegistryWaiterCanCancel validates that one waiting client can leave without interrupting shared work.
func TestIdempotencyRegistryWaiterCanCancel(t *testing.T) {
	registry := NewIdempotencyRegistry[string](time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	leaderResult := make(chan error, 1)
	go func() {
		_, err := registry.Do(context.Background(), "tenant", "key", func(context.Context) (string, error) {
			close(started)
			<-release
			return "complete", nil
		})
		leaderResult <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.Do(ctx, "tenant", "key", func(context.Context) (string, error) {
		t.Fatal("waiter must not execute the operation")
		return "", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter returned %v, want context cancellation", err)
	}
	close(release)
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader returned %v", err)
	}
}

// TestIdempotencyRegistryExpiryAndPurge validates TTL replacement and explicit stale-entry cleanup.
func TestIdempotencyRegistryExpiryAndPurge(t *testing.T) {
	clock := newManualClock()
	registry := NewIdempotencyRegistry[int](time.Minute, WithRegistryClock(clock))
	var executions int
	operation := func(context.Context) (int, error) {
		executions++
		return executions, nil
	}

	first, _ := registry.Do(context.Background(), "tenant", "key", operation)
	clock.Advance(time.Minute)
	second, _ := registry.Do(context.Background(), "tenant", "key", operation)
	if first != 1 || second != 2 {
		t.Fatalf("first = %d, second = %d, want cache refresh", first, second)
	}

	registry.Do(context.Background(), "tenant", "another-key", operation)
	clock.Advance(time.Minute)
	if removed := registry.PurgeExpired(); removed != 2 {
		t.Fatalf("purged %d entries, want 2", removed)
	}
}

func TestIdempotencyRegistryBindsFingerprintUntilExpiry(t *testing.T) {
	clock := newManualClock()
	registry := NewIdempotencyRegistry[string](time.Minute, WithRegistryClock(clock))
	var executions int
	operation := func(context.Context) (string, error) {
		executions++
		return "result", nil
	}

	if _, replayed, err := registry.DoWithFingerprint(context.Background(), "tenant", "key", "payload-a", operation); err != nil || replayed {
		t.Fatalf("initial call replayed=%t err=%v", replayed, err)
	}
	if _, _, err := registry.DoWithFingerprint(context.Background(), "tenant", "key", "payload-b", operation); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload error = %v, want conflict", err)
	}
	if executions != 1 {
		t.Fatalf("operation executions = %d, want 1", executions)
	}

	clock.Advance(time.Minute)
	if _, replayed, err := registry.DoWithFingerprint(context.Background(), "tenant", "key", "payload-b", operation); err != nil || replayed {
		t.Fatalf("post-expiry call replayed=%t err=%v", replayed, err)
	}
	if executions != 2 {
		t.Fatalf("operation executions = %d, want 2", executions)
	}
}
