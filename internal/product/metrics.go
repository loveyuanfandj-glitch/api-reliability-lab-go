package product

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Orders               *prometheus.CounterVec
	WebhookDeliveries    *prometheus.CounterVec
	CoordinationFallback prometheus.Counter
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		Orders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "northstar_product_orders_total",
			Help: "Product order requests grouped by result and source.",
		}, []string{"result", "source"}),
		WebhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "northstar_product_webhook_deliveries_total",
			Help: "Outbound webhook attempts grouped by result.",
		}, []string{"result"}),
		CoordinationFallback: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "northstar_product_coordination_fallback_total",
			Help: "Order requests that safely fell back from Redis to PostgreSQL uniqueness.",
		}),
	}
	registerer.MustRegister(metrics.Orders, metrics.WebhookDeliveries, metrics.CoordinationFallback)
	return metrics
}
