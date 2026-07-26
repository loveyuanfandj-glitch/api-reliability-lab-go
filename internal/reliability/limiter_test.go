package reliability

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTokenBucketLimiterBurstAndRefill validates initial burst capacity, denial, fractional refill, and the burst ceiling.
func TestTokenBucketLimiterBurstAndRefill(t *testing.T) {
	clock := newManualClock()
	limiter := NewTokenBucketLimiter(2, 3, WithLimiterClock(clock))

	for range 3 {
		if !limiter.Allow("tenant-a") {
			t.Fatal("initial burst should be allowed")
		}
	}
	if limiter.Allow("tenant-a") {
		t.Fatal("request beyond burst should be denied")
	}

	clock.Advance(250 * time.Millisecond)
	if limiter.Allow("tenant-a") {
		t.Fatal("half a token should not be enough")
	}
	clock.Advance(250 * time.Millisecond)
	if !limiter.Allow("tenant-a") {
		t.Fatal("one refilled token should be allowed")
	}

	clock.Advance(10 * time.Second)
	for range 3 {
		if !limiter.Allow("tenant-a") {
			t.Fatal("bucket should refill only to burst capacity")
		}
	}
	if limiter.Allow("tenant-a") {
		t.Fatal("refill must remain capped at burst capacity")
	}
}

// TestTokenBucketLimiterIsolatesKeysAndForget validates independent quotas and explicit state reset.
func TestTokenBucketLimiterIsolatesKeysAndForget(t *testing.T) {
	limiter := NewTokenBucketLimiter(1, 1, WithLimiterClock(newManualClock()))

	if !limiter.Allow("tenant-a") || !limiter.Allow("tenant-b") {
		t.Fatal("each key should receive its own initial capacity")
	}
	if limiter.Allow("tenant-a") || limiter.Allow("tenant-b") {
		t.Fatal("both independent buckets should be empty")
	}

	limiter.Forget("tenant-a")
	if !limiter.Allow("tenant-a") {
		t.Fatal("forgotten key should receive a fresh bucket")
	}
	if limiter.Allow("tenant-b") {
		t.Fatal("forgetting one key must not reset another")
	}
}

func TestTokenBucketLimiterResetClearsAllBuckets(t *testing.T) {
	limiter := NewTokenBucketLimiter(1, 1, WithLimiterClock(newManualClock()))
	if !limiter.Allow("tenant-a") || !limiter.Allow("tenant-b") {
		t.Fatal("initial requests should be allowed")
	}
	if limiter.Allow("tenant-a") || limiter.Allow("tenant-b") {
		t.Fatal("buckets should be exhausted")
	}
	limiter.Reset()
	if !limiter.Allow("tenant-a") || !limiter.Allow("tenant-b") {
		t.Fatal("reset should restore every bucket")
	}
}

func TestTokenBucketLimiterRefundRestoresOneToken(t *testing.T) {
	limiter := NewTokenBucketLimiter(1, 1, WithLimiterClock(newManualClock()))
	if !limiter.Allow("tenant") || limiter.Allow("tenant") {
		t.Fatal("expected a one-token bucket")
	}
	limiter.Refund("tenant")
	limiter.Refund("tenant")
	if !limiter.Allow("tenant") || limiter.Allow("tenant") {
		t.Fatal("refund should restore exactly up to burst capacity")
	}
}

// TestTokenBucketLimiterConcurrentBurst validates that concurrent callers cannot overspend a bucket.
func TestTokenBucketLimiterConcurrentBurst(t *testing.T) {
	const burst = 7
	limiter := NewTokenBucketLimiter(1, burst, WithLimiterClock(newManualClock()))
	var allowed atomic.Int32
	var wait sync.WaitGroup

	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if limiter.Allow("shared") {
				allowed.Add(1)
			}
		}()
	}
	wait.Wait()

	if got := allowed.Load(); got != burst {
		t.Fatalf("allowed %d concurrent requests, want %d", got, burst)
	}
}

// TestTokenBucketLimiterIgnoresBackwardTime validates that a clock correction cannot mint tokens.
func TestTokenBucketLimiterIgnoresBackwardTime(t *testing.T) {
	clock := newManualClock()
	limiter := NewTokenBucketLimiter(1, 1, WithLimiterClock(clock))

	if !limiter.Allow("tenant") {
		t.Fatal("initial request should pass")
	}
	clock.Rewind(time.Hour)
	if limiter.Allow("tenant") {
		t.Fatal("backward time must not refill the bucket")
	}
}
