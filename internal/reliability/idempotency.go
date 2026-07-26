package reliability

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request fingerprint")

type idempotencyKey struct {
	tenant string
	key    string
}

type idempotencyEntry[T any] struct {
	ready       chan struct{}
	value       T
	err         error
	fingerprint string
	expiresAt   time.Time
}

// RegistryOption customizes an IdempotencyRegistry.
type RegistryOption func(*registryOptions)

type registryOptions struct {
	clock Clock
}

// WithRegistryClock replaces wall-clock time, primarily for deterministic tests.
func WithRegistryClock(clock Clock) RegistryOption {
	return func(options *registryOptions) {
		options.clock = clock
	}
}

// IdempotencyRegistry coalesces concurrent work and caches successful results by tenant and key.
type IdempotencyRegistry[T any] struct {
	mu        sync.Mutex
	ttl       time.Duration
	clock     Clock
	entries   map[idempotencyKey]*idempotencyEntry[T]
	nextPurge time.Time
}

// NewIdempotencyRegistry creates a registry whose successful results live for ttl.
func NewIdempotencyRegistry[T any](ttl time.Duration, options ...RegistryOption) *IdempotencyRegistry[T] {
	if ttl <= 0 {
		panic("reliability: idempotency TTL must be positive")
	}

	config := registryOptions{clock: realClock{}}
	for _, option := range options {
		option(&config)
	}
	return &IdempotencyRegistry[T]{
		ttl:       ttl,
		clock:     config.clock,
		entries:   make(map[idempotencyKey]*idempotencyEntry[T]),
		nextPurge: config.clock.Now().Add(min(ttl, time.Minute)),
	}
}

// Do returns a cached result or performs operation once for concurrent callers of the same scope.
func (r *IdempotencyRegistry[T]) Do(
	ctx context.Context,
	tenant string,
	key string,
	operation func(context.Context) (T, error),
) (T, error) {
	value, _, err := r.DoWithStatus(ctx, tenant, key, operation)
	return value, err
}

// DoWithStatus behaves like Do and reports whether the result came from existing work.
func (r *IdempotencyRegistry[T]) DoWithStatus(
	ctx context.Context,
	tenant string,
	key string,
	operation func(context.Context) (T, error),
) (T, bool, error) {
	return r.DoWithFingerprint(ctx, tenant, key, "", operation)
}

// DoWithFingerprint also binds a key to a stable request fingerprint. Reusing
// an active key for different input returns ErrIdempotencyConflict.
func (r *IdempotencyRegistry[T]) DoWithFingerprint(
	ctx context.Context,
	tenant string,
	key string,
	fingerprint string,
	operation func(context.Context) (T, error),
) (T, bool, error) {
	scope := idempotencyKey{tenant: tenant, key: key}
	now := r.clock.Now()

	r.mu.Lock()
	r.purgeExpiredLocked(now, false)
	if entry, ok := r.entries[scope]; ok {
		if entry.expiresAt.IsZero() || now.Before(entry.expiresAt) {
			if entry.fingerprint != fingerprint {
				r.mu.Unlock()
				var zero T
				return zero, false, ErrIdempotencyConflict
			}
			r.mu.Unlock()
			value, err := waitForIdempotencyResult(ctx, entry)
			return value, true, err
		}
		delete(r.entries, scope)
	}

	entry := &idempotencyEntry[T]{ready: make(chan struct{}), fingerprint: fingerprint}
	r.entries[scope] = entry
	r.mu.Unlock()

	value, err := operation(ctx)
	r.mu.Lock()
	entry.value = value
	entry.err = err
	if err == nil {
		entry.expiresAt = r.clock.Now().Add(r.ttl)
	} else {
		delete(r.entries, scope)
	}
	close(entry.ready)
	r.mu.Unlock()
	return value, false, err
}

// PurgeExpired removes expired successful results and returns the number removed.
func (r *IdempotencyRegistry[T]) PurgeExpired() int {
	now := r.clock.Now()
	r.mu.Lock()
	removed := r.purgeExpiredLocked(now, true)
	r.mu.Unlock()
	return removed
}

func (r *IdempotencyRegistry[T]) purgeExpiredLocked(now time.Time, force bool) int {
	if !force && now.Before(r.nextPurge) {
		return 0
	}
	removed := 0
	for key, entry := range r.entries {
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			delete(r.entries, key)
			removed++
		}
	}
	r.nextPurge = now.Add(min(r.ttl, time.Minute))
	return removed
}

func waitForIdempotencyResult[T any](ctx context.Context, entry *idempotencyEntry[T]) (T, error) {
	select {
	case <-entry.ready:
		return entry.value, entry.err
	default:
	}

	select {
	case <-entry.ready:
		return entry.value, entry.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
