package product

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/redis/go-redis/v9"
)

// TestRedisCoordinatorSuppressesConcurrentDuplicates validates that one execution serves all concurrent identical requests.
func TestRedisCoordinatorSuppressesConcurrentDuplicates(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	coordinator := NewRedisCoordinator(client, "test")

	var executions atomic.Int64
	const workers = 24
	results := make(chan OrderResult, workers)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			order, replayed, err := coordinator.Do(context.Background(), "tenant-a", "key-1", "fingerprint-1", func(context.Context) (domain.Order, error) {
				executions.Add(1)
				time.Sleep(15 * time.Millisecond)
				return domain.Order{ID: "ord_1", TenantID: "tenant-a"}, nil
			})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- OrderResult{Order: order, Replayed: replayed}
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("coordinated request failed: %v", err)
	}
	if executions.Load() != 1 {
		t.Fatalf("expected one execution, got %d", executions.Load())
	}
	created := 0
	for result := range results {
		if result.Order.ID != "ord_1" {
			t.Fatalf("unexpected order: %+v", result.Order)
		}
		if !result.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected one original result, got %d", created)
	}
}

// TestRedisCoordinatorRejectsFingerprintConflict validates that a reused key cannot return a result for different input.
func TestRedisCoordinatorRejectsFingerprintConflict(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	coordinator := NewRedisCoordinator(client, "test")

	_, _, err := coordinator.Do(context.Background(), "tenant-a", "key-1", "fingerprint-1", func(context.Context) (domain.Order, error) {
		return domain.Order{ID: "ord_1"}, nil
	})
	if err != nil {
		t.Fatalf("seed result: %v", err)
	}
	_, _, err = coordinator.Do(context.Background(), "tenant-a", "key-1", "fingerprint-2", func(context.Context) (domain.Order, error) {
		return domain.Order{ID: "ord_2"}, nil
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

// TestRedisCoordinatorReportsUnavailable validates that callers can activate the PostgreSQL fallback when Redis is down.
func TestRedisCoordinatorReportsUnavailable(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	coordinator := NewRedisCoordinator(client, "test")
	server.Close()
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := coordinator.Do(ctx, "tenant-a", "key-1", "fingerprint-1", func(context.Context) (domain.Order, error) {
		return domain.Order{}, nil
	})
	if !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("expected coordination unavailable, got %v", err)
	}
}
