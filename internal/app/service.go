package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/reliability"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/store"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/telemetry"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/upstream"
)

var (
	ErrInvalidInput        = errors.New("invalid order input")
	ErrDependency          = errors.New("inventory dependency failed")
	ErrIdempotencyConflict = reliability.ErrIdempotencyConflict
)

type CreateResult struct {
	Order    domain.Order `json:"order"`
	Replayed bool         `json:"replayed"`
}

type Sample struct {
	At        time.Time `json:"at"`
	Requests  int       `json:"requests"`
	Errors    int       `json:"errors"`
	LatencyMS int64     `json:"latency_ms"`
}

type Snapshot struct {
	Service struct {
		Status        string `json:"status"`
		UptimeSeconds int64  `json:"uptime_seconds"`
		Version       string `json:"version"`
	} `json:"service"`
	Dependency struct {
		Mode                upstream.Mode `json:"mode"`
		CircuitState        string        `json:"circuit_state"`
		ConsecutiveFailures int           `json:"consecutive_failures"`
	} `json:"dependency"`
	Metrics struct {
		OrdersTotal          uint64 `json:"orders_total"`
		OrdersSucceeded      uint64 `json:"orders_succeeded"`
		OrdersFailed         uint64 `json:"orders_failed"`
		DuplicatesSuppressed uint64 `json:"duplicates_suppressed"`
		RateLimited          uint64 `json:"rate_limited"`
		DependencyCalls      uint64 `json:"dependency_calls"`
	} `json:"metrics"`
	RecentEvents []domain.Event `json:"recent_events"`
	Timeseries   []Sample       `json:"timeseries"`
}

type Service struct {
	lifecycleMu sync.RWMutex

	store       *store.Memory
	dependency  *upstream.Simulator
	breaker     *reliability.CircuitBreaker
	idempotency *reliability.IdempotencyRegistry[domain.Order]
	metrics     *telemetry.Metrics
	retryPolicy reliability.RetryPolicy
	startedAt   time.Time

	ordersTotal          atomic.Uint64
	ordersSucceeded      atomic.Uint64
	ordersFailed         atomic.Uint64
	duplicatesSuppressed atomic.Uint64
	rateLimited          atomic.Uint64

	samplesMu sync.RWMutex
	samples   []Sample
}

func NewService(memory *store.Memory, dependency *upstream.Simulator, metrics *telemetry.Metrics) *Service {
	return &Service{
		store:      memory,
		dependency: dependency,
		metrics:    metrics,
		breaker: reliability.NewCircuitBreaker(reliability.BreakerConfig{
			FailureThreshold: 3,
			OpenTimeout:      4 * time.Second,
		}),
		idempotency: reliability.NewIdempotencyRegistry[domain.Order](30 * time.Minute),
		retryPolicy: reliability.RetryPolicy{
			Attempts:  3,
			BaseDelay: 35 * time.Millisecond,
			MaxDelay:  140 * time.Millisecond,
			Jitter:    0.15,
			Retryable: func(err error) bool {
				return !errors.Is(err, context.Canceled)
			},
		},
		startedAt: time.Now().UTC(),
		samples:   make([]Sample, 0, 48),
	}
}

func (s *Service) CreateOrder(ctx context.Context, tenantID, idempotencyKey string, input domain.CreateOrderInput) (CreateResult, error) {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	started := time.Now()
	s.ordersTotal.Add(1)
	if tenantID == "" || idempotencyKey == "" || input.EventID == "" || input.Quantity < 1 || input.Quantity > 12 {
		s.ordersFailed.Add(1)
		s.metrics.OrderRequests.WithLabelValues("invalid").Inc()
		s.recordSample(started, true)
		return CreateResult{}, fmt.Errorf("%w: event_id is required and quantity must be between 1 and 12", ErrInvalidInput)
	}
	fingerprint := input.EventID + "\x00" + fmt.Sprint(input.Quantity)

	order, replayed, err := s.idempotency.DoWithFingerprint(ctx, tenantID, idempotencyKey, fingerprint, func(ctx context.Context) (domain.Order, error) {
		err := s.breaker.Execute(ctx, func(ctx context.Context) error {
			return reliability.Retry(ctx, s.retryPolicy, func(ctx context.Context) error {
				attemptCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				defer cancel()
				mode := s.dependency.Mode()
				err := s.dependency.Reserve(attemptCtx, input.EventID, input.Quantity)
				result := "success"
				if err != nil {
					result = "failure"
				}
				s.metrics.DependencyCalls.WithLabelValues(result, string(mode)).Inc()
				return err
			})
		})
		s.syncCircuitMetric()
		if err != nil {
			return domain.Order{}, fmt.Errorf("%w: %v", ErrDependency, err)
		}
		order := domain.Order{
			ID:       newID(),
			TenantID: tenantID,
			EventID:  input.EventID,
			Quantity: input.Quantity,
			Status:   domain.OrderConfirmed,
		}
		return s.store.Save(order, "order.confirmed"), nil
	})
	if err != nil {
		s.ordersFailed.Add(1)
		result := "failed"
		if errors.Is(err, ErrIdempotencyConflict) {
			result = "conflict"
		}
		s.metrics.OrderRequests.WithLabelValues(result).Inc()
		s.recordSample(started, true)
		return CreateResult{}, err
	}

	if replayed {
		s.duplicatesSuppressed.Add(1)
		s.metrics.DuplicatesSuppressed.Inc()
		s.metrics.OrderRequests.WithLabelValues("replayed").Inc()
	} else {
		s.ordersSucceeded.Add(1)
		s.metrics.OrderRequests.WithLabelValues("succeeded").Inc()
	}
	s.recordSample(started, false)
	return CreateResult{Order: order, Replayed: replayed}, nil
}

func (s *Service) GetOrder(tenantID, orderID string) (domain.Order, error) {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	return s.store.Get(tenantID, orderID)
}

func (s *Service) ListOrders(tenantID string) []domain.Order {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	return s.store.List(tenantID)
}

func (s *Service) EventsSince(tenantID string, sequence uint64) []domain.Event {
	return s.store.EventsSince(tenantID, sequence)
}

func (s *Service) Subscribe(buffer int) (<-chan domain.Event, func()) {
	return s.store.Subscribe(buffer)
}

func (s *Service) SubscribeSince(tenantID string, sequence uint64, buffer int) ([]domain.Event, <-chan domain.Event, func(), bool) {
	return s.store.SubscribeSince(tenantID, sequence, buffer)
}

func (s *Service) SetFaultMode(mode upstream.Mode) error { return s.dependency.SetMode(mode) }

func (s *Service) MarkRateLimited() {
	s.rateLimited.Add(1)
	s.metrics.RateLimited.Inc()
}

func (s *Service) Snapshot() Snapshot {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	var snapshot Snapshot
	circuitState := s.breaker.State()
	s.syncCircuitMetricState(circuitState)
	snapshot.Service.Status = "operational"
	if circuitState == reliability.StateOpen {
		snapshot.Service.Status = "degraded"
	}
	snapshot.Service.UptimeSeconds = int64(time.Since(s.startedAt).Seconds())
	snapshot.Service.Version = "1.0.0"
	snapshot.Dependency.Mode = s.dependency.Mode()
	snapshot.Dependency.CircuitState = circuitState.String()
	snapshot.Dependency.ConsecutiveFailures = s.breaker.ConsecutiveFailures()
	snapshot.Metrics.OrdersTotal = s.ordersTotal.Load()
	snapshot.Metrics.OrdersSucceeded = s.ordersSucceeded.Load()
	snapshot.Metrics.OrdersFailed = s.ordersFailed.Load()
	snapshot.Metrics.DuplicatesSuppressed = s.duplicatesSuppressed.Load()
	snapshot.Metrics.RateLimited = s.rateLimited.Load()
	snapshot.Metrics.DependencyCalls = s.dependency.Calls()
	snapshot.RecentEvents = s.store.RecentEvents(12)
	s.samplesMu.RLock()
	snapshot.Timeseries = append([]Sample(nil), s.samples...)
	s.samplesMu.RUnlock()
	return snapshot
}

func (s *Service) Ready() bool {
	state := s.breaker.State()
	s.syncCircuitMetricState(state)
	return state != reliability.StateOpen
}

func (s *Service) Reset() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.store.Reset()
	s.dependency.Reset()
	s.breaker.Reset()
	s.idempotency = reliability.NewIdempotencyRegistry[domain.Order](30 * time.Minute)
	s.ordersTotal.Store(0)
	s.ordersSucceeded.Store(0)
	s.ordersFailed.Store(0)
	s.duplicatesSuppressed.Store(0)
	s.rateLimited.Store(0)
	s.samplesMu.Lock()
	s.samples = nil
	s.samplesMu.Unlock()
	s.syncCircuitMetric()
}

func (s *Service) recordSample(started time.Time, failed bool) {
	sample := Sample{At: time.Now().UTC(), Requests: 1, LatencyMS: time.Since(started).Milliseconds()}
	if failed {
		sample.Errors = 1
	}
	s.samplesMu.Lock()
	s.samples = append(s.samples, sample)
	if len(s.samples) > 40 {
		s.samples = append([]Sample(nil), s.samples[len(s.samples)-40:]...)
	}
	s.samplesMu.Unlock()
}

func (s *Service) syncCircuitMetric() {
	s.syncCircuitMetricState(s.breaker.State())
}

func (s *Service) syncCircuitMetricState(state reliability.CircuitState) {
	value := 0.0
	switch state {
	case reliability.StateHalfOpen:
		value = 1
	case reliability.StateOpen:
		value = 2
	}
	s.metrics.CircuitState.Set(value)
}

func newID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("ord-%d", time.Now().UnixNano())
	}
	return "ord_" + hex.EncodeToString(value[:])
}
