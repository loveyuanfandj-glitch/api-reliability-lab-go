package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

type HTTPServer struct {
	service             *Service
	apiKeys             map[string]string
	integrationTenantID string
	stripeSecret        string
	shopifySecret       string
	logger              *slog.Logger
}

func NewHTTPServer(service *Service, config Config, metricsHandler http.Handler, logger *slog.Logger) http.Handler {
	server := &HTTPServer{
		service:             service,
		apiKeys:             config.APIKeys,
		integrationTenantID: config.IntegrationTenantID,
		stripeSecret:        config.StripeWebhookSecret,
		shopifySecret:       config.ShopifyWebhookSecret,
		logger:              logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.ready)
	mux.Handle("GET /metrics", metricsHandler)
	mux.HandleFunc("POST /v1/integrations/stripe/webhook", server.stripeWebhook)
	mux.HandleFunc("POST /v1/integrations/shopify/webhook", server.shopifyWebhook)
	mux.Handle("POST /v1/orders", server.authenticate(http.HandlerFunc(server.createOrder)))
	mux.Handle("GET /v1/orders", server.authenticate(http.HandlerFunc(server.listOrders)))
	mux.Handle("GET /v1/orders/{orderID}", server.authenticate(http.HandlerFunc(server.getOrder)))
	mux.Handle("GET /v1/webhook-deliveries", server.authenticate(http.HandlerFunc(server.listDeliveries)))
	mux.Handle("POST /v1/webhook-deliveries/{deliveryID}/retry", server.authenticate(http.HandlerFunc(server.replayDelivery)))
	return server.observe(mux)
}

func (s *HTTPServer) createOrder(response http.ResponseWriter, request *http.Request) {
	var input domain.CreateOrderInput
	if err := readJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	result, err := s.service.CreateOrder(request.Context(), tenantFrom(request.Context()), idempotencyKey, "api", "", input)
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(response, status, result)
}

func (s *HTTPServer) getOrder(response http.ResponseWriter, request *http.Request) {
	order, err := s.service.GetOrder(request.Context(), tenantFrom(request.Context()), request.PathValue("orderID"))
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, order)
}

func (s *HTTPServer) listOrders(response http.ResponseWriter, request *http.Request) {
	orders, err := s.service.ListOrders(request.Context(), tenantFrom(request.Context()), queryLimit(request, 100))
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"orders": orders})
}

func (s *HTTPServer) listDeliveries(response http.ResponseWriter, request *http.Request) {
	deliveries, err := s.service.ListDeliveries(request.Context(), tenantFrom(request.Context()), DeliveryStatus(request.URL.Query().Get("status")), queryLimit(request, 100))
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"deliveries": deliveries})
}

func (s *HTTPServer) replayDelivery(response http.ResponseWriter, request *http.Request) {
	if err := s.service.ReplayDelivery(request.Context(), tenantFrom(request.Context()), request.PathValue("deliveryID")); err != nil {
		s.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "retry_scheduled"})
}

func (s *HTTPServer) stripeWebhook(response http.ResponseWriter, request *http.Request) {
	if s.stripeSecret == "" {
		writeError(response, http.StatusServiceUnavailable, "integration_not_configured", "Stripe webhook secret is not configured")
		return
	}
	payload, err := readBody(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := VerifyStripeSignature(request.Header.Get("Stripe-Signature"), payload, s.stripeSecret, time.Now(), 5*time.Minute); err != nil {
		writeError(response, http.StatusUnauthorized, "invalid_signature", "signature verification failed")
		return
	}
	integration, err := ParseStripeOrder(payload)
	s.handleIntegration(response, request, "stripe", integration, err)
}

func (s *HTTPServer) shopifyWebhook(response http.ResponseWriter, request *http.Request) {
	if s.shopifySecret == "" {
		writeError(response, http.StatusServiceUnavailable, "integration_not_configured", "Shopify webhook secret is not configured")
		return
	}
	payload, err := readBody(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := VerifyShopifySignature(request.Header.Get("X-Shopify-Hmac-Sha256"), payload, s.shopifySecret); err != nil {
		writeError(response, http.StatusUnauthorized, "invalid_signature", "signature verification failed")
		return
	}
	integration, err := ParseShopifyOrder(payload)
	s.handleIntegration(response, request, "shopify", integration, err)
}

func (s *HTTPServer) handleIntegration(response http.ResponseWriter, request *http.Request, source string, integration IntegrationOrder, err error) {
	if errors.Is(err, ErrIgnoredEvent) {
		writeJSON(response, http.StatusAccepted, map[string]any{"accepted": true, "order_created": false})
		return
	}
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	result, err := s.service.CreateOrder(request.Context(), s.integrationTenantID, integration.IdempotencyKey, source, integration.ExternalID, integration.Input)
	if err != nil {
		s.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"accepted": true, "result": result})
}

func (s *HTTPServer) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *HTTPServer) ready(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.service.Ready(ctx); err != nil {
		writeError(response, http.StatusServiceUnavailable, "not_ready", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *HTTPServer) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		tenantID, ok := s.apiKeys[request.Header.Get("X-API-Key")]
		if !ok {
			writeError(response, http.StatusUnauthorized, "unauthorized", "a valid X-API-Key is required")
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), tenantContextKey{}, tenantID)))
	})
}

func (s *HTTPServer) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		s.logger.Info("http request", "method", request.Method, "path", request.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *HTTPServer) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrIdempotencyConflict):
		writeError(response, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(response, http.StatusNotFound, "not_found", err.Error())
	default:
		s.logger.Error("product request failed", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

type tenantContextKey struct{}

func tenantFrom(ctx context.Context) string {
	value, _ := ctx.Value(tenantContextKey{}).(string)
	return value
}

func readJSON(request *http.Request, target any) error {
	payload, err := readBody(request)
	if err != nil {
		return err
	}
	return decodeJSON(payload, target)
}

func readBody(request *http.Request) ([]byte, error) {
	const maxBodySize = 1 << 20
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxBodySize {
		return nil, fmt.Errorf("request body must be at most 1 MiB")
	}
	return body, nil
}

func queryLimit(request *http.Request, fallback int) int {
	limit, err := strconv.Atoi(request.URL.Query().Get("limit"))
	if err != nil || limit < 1 {
		return fallback
	}
	return limit
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
