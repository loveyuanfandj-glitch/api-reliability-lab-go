package product

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

type Service struct {
	repository         OrderRepository
	deliveries         DeliveryRepository
	coordinator        IdempotencyCoordinator
	metrics            *Metrics
	logger             *slog.Logger
	webhookURL         string
	maxWebhookAttempts int
}

func NewService(repository OrderRepository, deliveries DeliveryRepository, coordinator IdempotencyCoordinator, metrics *Metrics, logger *slog.Logger, webhookURL string, maxWebhookAttempts int) *Service {
	return &Service{
		repository:         repository,
		deliveries:         deliveries,
		coordinator:        coordinator,
		metrics:            metrics,
		logger:             logger,
		webhookURL:         webhookURL,
		maxWebhookAttempts: maxWebhookAttempts,
	}
}

func (s *Service) CreateOrder(ctx context.Context, tenantID, idempotencyKey, source, externalID string, input domain.CreateOrderInput) (OrderResult, error) {
	if source == "" {
		source = "api"
	}
	if externalID == "" {
		externalID = input.EventID
	}
	if tenantID == "" || len(tenantID) > 100 || idempotencyKey == "" || len(idempotencyKey) > 200 || source == "" || len(source) > 50 || externalID == "" || len(externalID) > 200 || input.EventID == "" || len(input.EventID) > 200 || input.Quantity < 1 || input.Quantity > 12 {
		s.recordOrder("invalid", source)
		return OrderResult{}, fmt.Errorf("%w: tenant, idempotency key, source, external_id and event_id are required and bounded; quantity must be between 1 and 12", ErrInvalidInput)
	}
	fingerprint := requestFingerprint(input, source, externalID)
	request := CreateOrderRequest{
		TenantID:           tenantID,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		Source:             source,
		ExternalID:         externalID,
		EventID:            input.EventID,
		Quantity:           input.Quantity,
		WebhookURL:         s.webhookURL,
		MaxWebhookAttempts: s.maxWebhookAttempts,
	}

	repositoryReplayed := false
	create := func(ctx context.Context) (domain.Order, error) {
		order, replayed, err := s.repository.CreateOrder(ctx, request)
		repositoryReplayed = replayed
		return order, err
	}
	order, coordinationReplayed, err := s.coordinator.Do(ctx, tenantID, idempotencyKey, fingerprint, create)
	if errors.Is(err, ErrCoordinationUnavailable) {
		s.recordCoordinationFallback()
		s.logger.Warn("redis coordination unavailable; using postgres uniqueness fallback", "tenant_id", tenantID, "error", err)
		order, repositoryReplayed, err = s.repository.CreateOrder(ctx, request)
		coordinationReplayed = false
	}
	if err != nil {
		result := "failed"
		if errors.Is(err, ErrIdempotencyConflict) {
			result = "conflict"
		}
		s.recordOrder(result, source)
		return OrderResult{}, err
	}
	replayed := coordinationReplayed || repositoryReplayed
	result := "created"
	if replayed {
		result = "replayed"
	}
	s.recordOrder(result, source)
	return OrderResult{Order: order, Replayed: replayed}, nil
}

func (s *Service) GetOrder(ctx context.Context, tenantID, orderID string) (domain.Order, error) {
	return s.repository.GetOrder(ctx, tenantID, orderID)
}

func (s *Service) ListOrders(ctx context.Context, tenantID string, limit int) ([]domain.Order, error) {
	return s.repository.ListOrders(ctx, tenantID, limit)
}

func (s *Service) ListDeliveries(ctx context.Context, tenantID string, status DeliveryStatus, limit int) ([]WebhookDelivery, error) {
	if status != "" && status != DeliveryPending && status != DeliveryProcessing && status != DeliveryDelivered && status != DeliveryDeadLetter {
		return nil, fmt.Errorf("%w: unknown delivery status", ErrInvalidInput)
	}
	return s.deliveries.ListDeliveries(ctx, tenantID, status, limit)
}

func (s *Service) ReplayDelivery(ctx context.Context, tenantID, id string) error {
	return s.deliveries.ReplayDelivery(ctx, tenantID, id)
}

func (s *Service) Ready(ctx context.Context) error {
	if err := s.repository.Ping(ctx); err != nil {
		return err
	}
	return s.coordinator.Ping(ctx)
}

func (s *Service) recordOrder(result, source string) {
	if s.metrics != nil {
		s.metrics.Orders.WithLabelValues(result, strings.ToLower(source)).Inc()
	}
}

func (s *Service) recordCoordinationFallback() {
	if s.metrics != nil {
		s.metrics.CoordinationFallback.Inc()
	}
}

func requestFingerprint(input domain.CreateOrderInput, source, externalID string) string {
	sum := sha256.Sum256([]byte(input.EventID + "\x00" + fmt.Sprint(input.Quantity) + "\x00" + source + "\x00" + externalID))
	return hex.EncodeToString(sum[:])
}
