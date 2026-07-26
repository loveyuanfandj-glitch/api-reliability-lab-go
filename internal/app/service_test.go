package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/store"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/telemetry"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/upstream"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestService() *Service {
	return NewService(store.NewMemory(500), upstream.NewSimulator(), telemetry.NewMetrics(prometheus.NewRegistry()))
}

func TestConcurrentDuplicateSuppression(t *testing.T) {
	service := newTestService()
	const requests = 20
	results := make(chan CreateResult, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.CreateOrder(context.Background(), "alpha", "same-key", domain.CreateOrderInput{EventID: "concert", Quantity: 2})
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
	}
	var orderID string
	for result := range results {
		if orderID == "" {
			orderID = result.Order.ID
		}
		if result.Order.ID != orderID {
			t.Fatalf("got multiple order IDs: %q and %q", orderID, result.Order.ID)
		}
	}
	if got := service.store.Count(); got != 1 {
		t.Fatalf("stored orders = %d, want 1", got)
	}
	if got := service.Snapshot().Metrics.DuplicatesSuppressed; got != requests-1 {
		t.Fatalf("duplicates = %d, want %d", got, requests-1)
	}
}

func TestUnavailableDependencyOpensCircuit(t *testing.T) {
	service := newTestService()
	if err := service.SetFaultMode(upstream.Unavailable); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = service.CreateOrder(context.Background(), "alpha", string(rune('a'+i)), domain.CreateOrderInput{EventID: "concert", Quantity: 1})
	}
	if state := service.Snapshot().Dependency.CircuitState; state != "open" {
		t.Fatalf("circuit state = %s, want open", state)
	}
}

func TestIdempotencyKeyRejectsDifferentPayload(t *testing.T) {
	service := newTestService()
	if _, err := service.CreateOrder(context.Background(), "alpha", "stable-key", domain.CreateOrderInput{EventID: "concert-a", Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateOrder(context.Background(), "alpha", "stable-key", domain.CreateOrderInput{EventID: "concert-b", Quantity: 1})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestResetClearsIdempotencyState(t *testing.T) {
	service := newTestService()
	first, err := service.CreateOrder(context.Background(), "alpha", "reusable-key", domain.CreateOrderInput{EventID: "concert-a", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	service.Reset()
	second, err := service.CreateOrder(context.Background(), "alpha", "reusable-key", domain.CreateOrderInput{EventID: "concert-b", Quantity: 2})
	if err != nil {
		t.Fatalf("create after reset: %v", err)
	}
	if first.Order.ID == second.Order.ID || second.Replayed {
		t.Fatalf("reset returned stale idempotency result: %#v", second)
	}
}
