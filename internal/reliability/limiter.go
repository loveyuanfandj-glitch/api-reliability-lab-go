package reliability

import (
	"sync"
	"time"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// LimiterOption customizes a TokenBucketLimiter.
type LimiterOption func(*TokenBucketLimiter)

// WithLimiterClock replaces wall-clock time, primarily for deterministic tests.
func WithLimiterClock(clock Clock) LimiterOption {
	return func(limiter *TokenBucketLimiter) {
		limiter.clock = clock
	}
}

// TokenBucketLimiter enforces the same token-bucket policy independently for each key.
type TokenBucketLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	clock   Clock
	buckets map[string]*tokenBucket
}

// NewTokenBucketLimiter creates a limiter that refills rate tokens per second up to burst.
func NewTokenBucketLimiter(rate float64, burst int, options ...LimiterOption) *TokenBucketLimiter {
	if rate <= 0 {
		panic("reliability: limiter rate must be positive")
	}
	if burst <= 0 {
		panic("reliability: limiter burst must be positive")
	}

	limiter := &TokenBucketLimiter{
		rate:    rate,
		burst:   float64(burst),
		clock:   realClock{},
		buckets: make(map[string]*tokenBucket),
	}
	for _, option := range options {
		option(limiter)
	}
	return limiter
}

// Allow consumes one token for key and reports whether the request may proceed.
func (l *TokenBucketLimiter) Allow(key string) bool {
	now := l.clock.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &tokenBucket{tokens: l.burst - 1, last: now}
		return true
	}

	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens = min(l.burst, bucket.tokens+elapsed*l.rate)
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false
	}

	bucket.tokens--
	return true
}

// Refund returns one token after a later admission layer rejects the request.
// The bucket remains capped at its configured burst.
func (l *TokenBucketLimiter) Refund(key string) {
	l.mu.Lock()
	if bucket, ok := l.buckets[key]; ok {
		bucket.tokens = min(l.burst, bucket.tokens+1)
	}
	l.mu.Unlock()
}

// Forget removes the accumulated state for key.
func (l *TokenBucketLimiter) Forget(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// Reset clears all accumulated buckets. It is intended for isolated demos and tests.
func (l *TokenBucketLimiter) Reset() {
	l.mu.Lock()
	l.buckets = make(map[string]*tokenBucket)
	l.mu.Unlock()
}
