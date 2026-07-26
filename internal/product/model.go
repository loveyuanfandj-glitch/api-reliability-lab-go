package product

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

var (
	ErrInvalidInput            = errors.New("invalid product input")
	ErrNotFound                = errors.New("resource not found")
	ErrIdempotencyConflict     = errors.New("idempotency key reused with a different payload")
	ErrCoordinationUnavailable = errors.New("redis coordination unavailable")
	ErrInvalidSignature        = errors.New("invalid webhook signature")
)

type CreateOrderRequest struct {
	TenantID           string
	IdempotencyKey     string
	RequestFingerprint string
	Source             string
	ExternalID         string
	EventID            string
	Quantity           int
	WebhookURL         string
	MaxWebhookAttempts int
}

type OrderResult struct {
	Order    domain.Order `json:"order"`
	Replayed bool         `json:"replayed"`
}

type DeliveryStatus string

const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryProcessing DeliveryStatus = "processing"
	DeliveryDelivered  DeliveryStatus = "delivered"
	DeliveryDeadLetter DeliveryStatus = "dead_letter"
)

type WebhookDelivery struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	TargetURL     string          `json:"target_url"`
	Status        DeliveryStatus  `json:"status"`
	Attempts      int             `json:"attempts"`
	MaxAttempts   int             `json:"max_attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
