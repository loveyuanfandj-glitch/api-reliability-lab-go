package domain

import "time"

type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderConfirmed OrderStatus = "confirmed"
	OrderFailed    OrderStatus = "failed"
)

type Order struct {
	ID        string      `json:"id"`
	TenantID  string      `json:"tenant_id"`
	EventID   string      `json:"event_id"`
	Quantity  int         `json:"quantity"`
	Status    OrderStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Sequence  uint64      `json:"sequence"`
}

type Event struct {
	Sequence uint64      `json:"sequence"`
	Type     string      `json:"type"`
	OrderID  string      `json:"order_id"`
	TenantID string      `json:"tenant_id"`
	Status   OrderStatus `json:"status"`
	At       time.Time   `json:"at"`
}

type CreateOrderInput struct {
	EventID  string `json:"event_id"`
	Quantity int    `json:"quantity"`
}
