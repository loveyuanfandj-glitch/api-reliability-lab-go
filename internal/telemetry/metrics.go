package telemetry

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	HTTPRequests         *prometheus.CounterVec
	OrderRequests        *prometheus.CounterVec
	DependencyCalls      *prometheus.CounterVec
	DuplicatesSuppressed prometheus.Counter
	RateLimited          prometheus.Counter
	RequestDuration      *prometheus.HistogramVec
	CircuitState         prometheus.Gauge
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	m := &Metrics{
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "northstar_http_requests_total",
			Help: "HTTP requests processed by method, route, and status.",
		}, []string{"method", "path", "status"}),
		OrderRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "northstar_order_requests_total",
			Help: "Order requests partitioned by outcome.",
		}, []string{"result"}),
		DependencyCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "northstar_dependency_calls_total",
			Help: "Synthetic inventory dependency calls.",
		}, []string{"result", "mode"}),
		DuplicatesSuppressed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "northstar_duplicates_suppressed_total",
			Help: "Duplicate order requests returned from the idempotency registry.",
		}),
		RateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "northstar_rate_limited_total",
			Help: "Requests rejected by tenant or API-key limits.",
		}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "northstar_request_duration_seconds",
			Help:    "End-to-end HTTP request latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"path"}),
		CircuitState: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "northstar_circuit_state",
			Help: "Circuit state: 0 closed, 1 half-open, 2 open.",
		}),
	}
	registerer.MustRegister(
		m.HTTPRequests,
		m.OrderRequests,
		m.DependencyCalls,
		m.DuplicatesSuppressed,
		m.RateLimited,
		m.RequestDuration,
		m.CircuitState,
	)
	return m
}
