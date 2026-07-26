package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

func TestMemoryTenantIsolationAndReplay(t *testing.T) {
	store := NewMemory(10)
	first := store.Save(domain.Order{ID: "a", TenantID: "alpha", Status: domain.OrderConfirmed}, "order.confirmed")
	store.Save(domain.Order{ID: "b", TenantID: "beta", Status: domain.OrderConfirmed}, "order.confirmed")
	third := store.Save(domain.Order{ID: "c", TenantID: "alpha", Status: domain.OrderFailed}, "order.failed")

	if _, err := store.Get("beta", first.ID); err != ErrNotFound {
		t.Fatalf("expected tenant isolation, got %v", err)
	}
	events := store.EventsSince("alpha", first.Sequence)
	if len(events) != 1 || events[0].Sequence != third.Sequence {
		t.Fatalf("unexpected replay: %#v", events)
	}
}

func TestMemoryConcurrentSaveSequencesAreUnique(t *testing.T) {
	store := NewMemory(200)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store.Save(domain.Order{ID: fmt.Sprintf("order-%d", i), TenantID: "alpha"}, "order.created")
		}(i)
	}
	wg.Wait()
	if got := store.Count(); got != 100 {
		t.Fatalf("count = %d, want 100", got)
	}
	events := store.EventsSince("alpha", 0)
	seen := make(map[uint64]struct{}, len(events))
	for _, event := range events {
		if _, exists := seen[event.Sequence]; exists {
			t.Fatalf("duplicate sequence %d", event.Sequence)
		}
		seen[event.Sequence] = struct{}{}
	}
}

func TestSubscribeSinceReturnsReplayThenLiveEvents(t *testing.T) {
	memory := NewMemory(10)
	first := memory.Save(domain.Order{ID: "one", TenantID: "alpha"}, "order.created")
	second := memory.Save(domain.Order{ID: "two", TenantID: "alpha"}, "order.created")
	replay, live, cancel, expired := memory.SubscribeSince("alpha", first.Sequence, 2)
	defer cancel()
	if expired {
		t.Fatal("fresh cursor should not be expired")
	}
	if len(replay) != 1 || replay[0].Sequence != second.Sequence {
		t.Fatalf("unexpected replay: %#v", replay)
	}
	third := memory.Save(domain.Order{ID: "three", TenantID: "alpha"}, "order.created")
	select {
	case event := <-live:
		if event.Sequence != third.Sequence {
			t.Fatalf("live sequence = %d, want %d", event.Sequence, third.Sequence)
		}
	default:
		t.Fatal("expected live event")
	}
}

func TestSlowSubscriberIsClosedInsteadOfSilentlyLosingEvents(t *testing.T) {
	memory := NewMemory(10)
	_, live, cancel, _ := memory.SubscribeSince("alpha", 0, 1)
	defer cancel()
	memory.Save(domain.Order{ID: "one", TenantID: "alpha"}, "order.created")
	memory.Save(domain.Order{ID: "two", TenantID: "alpha"}, "order.created")

	if _, ok := <-live; !ok {
		t.Fatal("buffered event should be delivered before closure")
	}
	if _, ok := <-live; ok {
		t.Fatal("overflowed subscriber should be closed to force cursor replay")
	}
}

func TestResetClosesSubscribersBeforeSequenceRestarts(t *testing.T) {
	memory := NewMemory(10)
	_, live, cancel, _ := memory.SubscribeSince("alpha", 0, 1)
	defer cancel()
	memory.Reset()
	if _, ok := <-live; ok {
		t.Fatal("reset must close existing subscribers")
	}
	order := memory.Save(domain.Order{ID: "after-reset", TenantID: "alpha"}, "order.created")
	if order.Sequence != 1 {
		t.Fatalf("sequence = %d, want restarted sequence 1", order.Sequence)
	}
}

func TestSubscribeSinceMarksEvictedCursorAndListReconciles(t *testing.T) {
	memory := NewMemory(2)
	first := memory.Save(domain.Order{ID: "one", TenantID: "alpha"}, "order.created")
	memory.Save(domain.Order{ID: "two", TenantID: "alpha"}, "order.created")
	memory.Save(domain.Order{ID: "three", TenantID: "alpha"}, "order.created")
	_, _, cancel, expired := memory.SubscribeSince("alpha", first.Sequence, 1)
	cancel()
	if !expired {
		t.Fatal("evicted cursor should require reconciliation")
	}
	if orders := memory.List("alpha"); len(orders) != 3 {
		t.Fatalf("reconciliation order count = %d, want 3", len(orders))
	}
}
