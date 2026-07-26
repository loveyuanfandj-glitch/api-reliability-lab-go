package reliability

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen indicates that an operation was rejected without calling its dependency.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitState is the externally visible state of a circuit breaker.
type CircuitState uint8

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

func (state CircuitState) String() string {
	switch state {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// BreakerConfig defines when the circuit opens and when it permits a recovery probe.
type BreakerConfig struct {
	FailureThreshold int
	OpenTimeout      time.Duration
}

// BreakerOption customizes a CircuitBreaker.
type BreakerOption func(*CircuitBreaker)

// WithBreakerClock replaces wall-clock time, primarily for deterministic tests.
func WithBreakerClock(clock Clock) BreakerOption {
	return func(breaker *CircuitBreaker) {
		breaker.clock = clock
	}
}

type breakerTicket struct {
	state      CircuitState
	generation uint64
}

// CircuitBreaker fails fast after consecutive dependency failures and allows one recovery probe.
type CircuitBreaker struct {
	mu                  sync.Mutex
	failureThreshold    int
	openTimeout         time.Duration
	clock               Clock
	state               CircuitState
	consecutiveFailures int
	openedAt            time.Time
	generation          uint64
	halfOpenProbe       bool
}

// NewCircuitBreaker creates a closed circuit breaker.
func NewCircuitBreaker(config BreakerConfig, options ...BreakerOption) *CircuitBreaker {
	if config.FailureThreshold <= 0 {
		panic("reliability: circuit breaker failure threshold must be positive")
	}
	if config.OpenTimeout <= 0 {
		panic("reliability: circuit breaker open timeout must be positive")
	}

	breaker := &CircuitBreaker{
		failureThreshold: config.FailureThreshold,
		openTimeout:      config.OpenTimeout,
		clock:            realClock{},
	}
	for _, option := range options {
		option(breaker)
	}
	return breaker
}

// Execute runs operation when the circuit permits it and records its outcome.
func (b *CircuitBreaker) Execute(ctx context.Context, operation func(context.Context) error) error {
	ticket, allowed := b.beforeRequest()
	if !allowed {
		return ErrCircuitOpen
	}

	err := operation(ctx)
	b.afterRequest(ticket, err == nil)
	return err
}

// State returns the current state, applying a due open-to-half-open transition.
func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advanceState(b.clock.Now())
	return b.state
}

// ConsecutiveFailures returns the failures accumulated toward the opening threshold.
func (b *CircuitBreaker) ConsecutiveFailures() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consecutiveFailures
}

// Reset closes the circuit and clears failure history without accepting stale outcomes.
func (b *CircuitBreaker) Reset() {
	b.mu.Lock()
	b.state = StateClosed
	b.consecutiveFailures = 0
	b.openedAt = time.Time{}
	b.halfOpenProbe = false
	b.generation++
	b.mu.Unlock()
}

func (b *CircuitBreaker) beforeRequest() (breakerTicket, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.advanceState(b.clock.Now())
	if b.state == StateOpen || (b.state == StateHalfOpen && b.halfOpenProbe) {
		return breakerTicket{}, false
	}

	ticket := breakerTicket{state: b.state, generation: b.generation}
	if b.state == StateHalfOpen {
		b.halfOpenProbe = true
	}
	return ticket, true
}

func (b *CircuitBreaker) afterRequest(ticket breakerTicket, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ticket.generation != b.generation || ticket.state != b.state {
		return
	}

	if success {
		b.consecutiveFailures = 0
		if b.state == StateHalfOpen {
			b.state = StateClosed
			b.halfOpenProbe = false
			b.generation++
		}
		return
	}

	if b.state == StateHalfOpen {
		b.open(b.clock.Now())
		return
	}

	b.consecutiveFailures++
	if b.consecutiveFailures >= b.failureThreshold {
		b.open(b.clock.Now())
	}
}

func (b *CircuitBreaker) advanceState(now time.Time) {
	if b.state == StateOpen && now.Sub(b.openedAt) >= b.openTimeout {
		b.state = StateHalfOpen
		b.halfOpenProbe = false
		b.generation++
	}
}

func (b *CircuitBreaker) open(now time.Time) {
	b.state = StateOpen
	b.openedAt = now
	b.consecutiveFailures = 0
	b.halfOpenProbe = false
	b.generation++
}
