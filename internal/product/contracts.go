package product

import (
	"context"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

type OrderRepository interface {
	CreateOrder(context.Context, CreateOrderRequest) (domain.Order, bool, error)
	GetOrder(context.Context, string, string) (domain.Order, error)
	ListOrders(context.Context, string, int) ([]domain.Order, error)
	Ping(context.Context) error
}

type DeliveryRepository interface {
	ClaimDeliveries(context.Context, int, time.Duration) ([]WebhookDelivery, error)
	MarkDeliverySucceeded(context.Context, string) error
	MarkDeliveryFailed(context.Context, WebhookDelivery, string, time.Duration) (DeliveryStatus, error)
	ListDeliveries(context.Context, string, DeliveryStatus, int) ([]WebhookDelivery, error)
	ReplayDelivery(context.Context, string, string) error
}

type IdempotencyCoordinator interface {
	Do(context.Context, string, string, string, func(context.Context) (domain.Order, error)) (domain.Order, bool, error)
	Ping(context.Context) error
}
