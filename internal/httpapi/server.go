package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/app"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/reliability"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/store"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/telemetry"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/upstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	service       *app.Service
	metrics       *telemetry.Metrics
	logger        *slog.Logger
	tenantLimiter *reliability.TokenBucketLimiter
	keyLimiter    *reliability.TokenBucketLimiter
	ipLimiter     *reliability.TokenBucketLimiter
	apiKeys       map[string]string
	demoMode      bool
	webDir        string
}

func NewServer(service *app.Service, metrics *telemetry.Metrics, logger *slog.Logger) *Server {
	webDir := os.Getenv("WEB_DIR")
	if webDir == "" {
		webDir = "web"
	}
	return &Server{
		service:       service,
		metrics:       metrics,
		logger:        logger,
		tenantLimiter: reliability.NewTokenBucketLimiter(20, 30),
		keyLimiter:    reliability.NewTokenBucketLimiter(15, 25),
		ipLimiter:     reliability.NewTokenBucketLimiter(40, 50),
		apiKeys: map[string]string{
			"tenant-alpha-key": "tenant-alpha",
			"tenant-beta-key":  "tenant-beta",
		},
		demoMode: strings.EqualFold(os.Getenv("DEMO_MODE"), "true"),
		webDir:   webDir,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("POST /v1/orders", s.auth(s.rateLimit(s.createOrder)))
	mux.HandleFunc("GET /v1/orders", s.auth(s.listOrders))
	mux.HandleFunc("GET /v1/orders/{id}", s.auth(s.getOrder))
	mux.HandleFunc("GET /v1/stream", s.auth(s.stream))
	mux.HandleFunc("GET /v1/demo/snapshot", s.demo(s.snapshot))
	mux.HandleFunc("POST /v1/demo/fault", s.demo(s.setFault))
	mux.HandleFunc("POST /v1/demo/reset", s.demo(s.reset))
	mux.Handle("GET /", http.FileServer(http.Dir(s.webDir)))
	return s.observe(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	state := "ready"
	if !s.service.Ready() {
		status = http.StatusServiceUnavailable
		state = "degraded"
	}
	writeJSON(w, status, map[string]any{"status": state, "dependency": s.service.Snapshot().Dependency})
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var input domain.CreateOrderInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain event_id and quantity")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is required and must be at most 128 characters")
		return
	}
	result, err := s.service.CreateOrder(r.Context(), tenantFromContext(r), idempotencyKey, input)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "invalid_order", err.Error())
		case errors.Is(err, app.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
		case errors.Is(err, app.ErrDependency):
			writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "inventory reservation is temporarily unavailable")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		}
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	order, err := s.service.GetOrder(tenantFromContext(r), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "order was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "order could not be loaded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": order})
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"orders": s.service.ListOrders(tenantFromContext(r))})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	tenantID := tenantFromContext(r)
	replay, events, cancel, expired := s.service.SubscribeSince(tenantID, since, 128)
	if expired {
		cancel()
		writeError(w, http.StatusConflict, "replay_window_expired", "cursor is older than the retained event window; reconcile through GET /v1/orders")
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		cancel()
		s.logger.Warn("websocket accept failed", "error", err)
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "stream closed")
	defer cancel()
	for _, event := range replay {
		if err := writeWebSocketJSON(r, connection, event); err != nil {
			return
		}
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			ctx, done := withTimeout(r, 3*time.Second)
			err := connection.Ping(ctx)
			done()
			if err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.TenantID == tenantID {
				if err := writeWebSocketJSON(r, connection, event); err != nil {
					return
				}
			}
		}
	}
}

func (s *Server) snapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.service.Snapshot())
}

func (s *Server) setFault(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Mode upstream.Mode `json:"mode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "mode is required")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}
	if err := s.service.SetFaultMode(request.Mode); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_mode", err.Error())
		return
	}
	s.logger.Info("synthetic dependency mode changed", "mode", request.Mode)
	writeJSON(w, http.StatusOK, map[string]any{"dependency": s.service.Snapshot().Dependency})
}

func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	s.service.Reset()
	s.tenantLimiter.Reset()
	s.keyLimiter.Reset()
	s.ipLimiter.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"status": "reset"})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		tenantID, ok := s.apiKeys[key]
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid X-API-Key is required")
			return
		}
		r.Header.Set("X-Northstar-Tenant", tenantID)
		next(w, r)
	}
}

func (s *Server) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := tenantFromContext(r)
		key := r.Header.Get("X-API-Key")
		keyScope := tenantID + ":" + key
		if !s.keyLimiter.Allow(keyScope) {
			s.rejectRateLimit(w, r, "api_key")
			return
		}
		if !s.tenantLimiter.Allow(tenantID) {
			s.keyLimiter.Refund(keyScope)
			s.rejectRateLimit(w, r, "tenant")
			return
		}
		ip := clientIP(r)
		if !s.ipLimiter.Allow(ip) {
			s.tenantLimiter.Refund(tenantID)
			s.keyLimiter.Refund(keyScope)
			s.rejectRateLimit(w, r, "ip")
			return
		}
		next(w, r)
	}
}

func (s *Server) rejectRateLimit(w http.ResponseWriter, r *http.Request, layer string) {
	s.service.MarkRateLimited()
	s.logger.Warn("request rate limited", "layer", layer, "tenant", tenantFromContext(r), "client_ip", clientIP(r))
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "request rate exceeded the synthetic isolation policy")
}

func (s *Server) demo(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.demoMode {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		capture := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(capture, r)
		path := routeLabel(r.URL.Path)
		s.metrics.HTTPRequests.WithLabelValues(r.Method, path, strconv.Itoa(capture.status)).Inc()
		s.metrics.RequestDuration.WithLabelValues(path).Observe(time.Since(started).Seconds())
		s.logger.Info("http request",
			"method", r.Method,
			"path", path,
			"status", capture.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func tenantFromContext(r *http.Request) string { return r.Header.Get("X-Northstar-Tenant") }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func routeLabel(path string) string {
	if strings.HasPrefix(path, "/v1/orders/") {
		return "/v1/orders/{id}"
	}
	switch path {
	case "/", "/styles.css", "/app.js", "/healthz", "/readyz", "/metrics", "/v1/orders", "/v1/stream", "/v1/demo/snapshot", "/v1/demo/fault", "/v1/demo/reset":
		return path
	default:
		return "other"
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeWebSocketJSON(r *http.Request, connection *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(r, 3*time.Second)
	defer cancel()
	return connection.Write(ctx, websocket.MessageText, payload)
}

func withTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
