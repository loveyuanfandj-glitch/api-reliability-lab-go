package product

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// TestProductRuntimeEndToEnd validates PostgreSQL persistence, Redis duplicate suppression, tenant isolation, HTTP APIs, and transactional webhook retry.
func TestProductRuntimeEndToEnd(t *testing.T) {
	databaseURL := os.Getenv("PRODUCT_TEST_DATABASE_URL")
	redisURL := os.Getenv("PRODUCT_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("set PRODUCT_TEST_DATABASE_URL and PRODUCT_TEST_REDIS_URL to run product integration tests")
	}
	ctx := context.Background()
	repository, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis URL: %v", err)
	}
	redisClient := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = redisClient.Close() })

	secret := "integration-outbound-secret"
	var sinkCalls atomic.Int64
	var received atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		payload, _ := io.ReadAll(request.Body)
		if err := VerifyOutboundWebhook(request.Header.Get("X-Northstar-Signature"), payload, secret, time.Now(), 5*time.Minute); err != nil {
			http.Error(response, "bad signature", http.StatusUnauthorized)
			return
		}
		if sinkCalls.Add(1) <= 2 {
			http.Error(response, "simulated outage", http.StatusServiceUnavailable)
			return
		}
		received.Add(1)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prefix := "integration:" + productID("run")
	alphaTenant := productID("tenant-alpha")
	betaTenant := productID("tenant-beta")
	testEventID := productID("show")
	coordinator := NewRedisCoordinator(redisClient, prefix)
	service := NewService(repository, repository, coordinator, metrics, logger, sink.URL, 5)
	config := Config{
		APIKeys:              map[string]string{"alpha-key": alphaTenant, "beta-key": betaTenant},
		IntegrationTenantID:  alphaTenant,
		StripeWebhookSecret:  "stripe-integration-secret",
		ShopifyWebhookSecret: "shopify-integration-secret",
	}
	server := httptest.NewServer(NewHTTPServer(service, config, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), logger))
	defer server.Close()

	idempotencyKey := productID("idem")
	requestBody := []byte(fmt.Sprintf(`{"event_id":%q,"quantity":2}`, testEventID))
	statuses := make(chan int, 20)
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			response := productRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/orders", requestBody, "alpha-key", idempotencyKey, nil)
			statuses <- response.StatusCode
			_ = response.Body.Close()
		}()
	}
	group.Wait()
	close(statuses)
	created := 0
	for status := range statuses {
		if status == http.StatusCreated {
			created++
		} else if status != http.StatusOK {
			t.Fatalf("unexpected concurrent create status: %d", status)
		}
	}
	if created != 1 {
		t.Fatalf("expected one created response, got %d", created)
	}
	orders, err := repository.ListOrders(ctx, alphaTenant, 100)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	matching := 0
	for _, order := range orders {
		if order.EventID == testEventID {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("expected one durable order, got %d", matching)
	}

	conflictBody := []byte(fmt.Sprintf(`{"event_id":%q,"quantity":3}`, testEventID))
	conflict := productRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/orders", conflictBody, "alpha-key", idempotencyKey, nil)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected idempotency conflict, got %d", conflict.StatusCode)
	}

	betaList := productRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/orders", nil, "beta-key", "", nil)
	defer betaList.Body.Close()
	var betaPayload struct {
		Orders []json.RawMessage `json:"orders"`
	}
	if err := json.NewDecoder(betaList.Body).Decode(&betaPayload); err != nil {
		t.Fatalf("decode beta order list: %v", err)
	}
	if len(betaPayload.Orders) != 0 {
		t.Fatalf("tenant isolation failed: beta saw %d orders", len(betaPayload.Orders))
	}

	worker := NewWebhookWorker(repository, sink.Client(), secret, time.Minute, time.Millisecond, metrics, logger)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := worker.ProcessBatch(ctx); err != nil {
			t.Fatalf("process webhook batch %d: %v", attempt+1, err)
		}
		_, err := repository.pool.Exec(ctx, `UPDATE product_webhook_deliveries SET next_attempt_at = NOW() WHERE tenant_id = $1`, alphaTenant)
		if err != nil {
			t.Fatalf("advance retry schedule: %v", err)
		}
	}
	deliveries, err := repository.ListDeliveries(ctx, alphaTenant, DeliveryDelivered, 100)
	if err != nil {
		t.Fatalf("list delivered webhooks: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Attempts != 3 || received.Load() != 1 {
		t.Fatalf("unexpected delivery outcome: deliveries=%+v received=%d", deliveries, received.Load())
	}

	stripeID := productID("evt-stripe")
	stripePayload := []byte(fmt.Sprintf(`{"id":%q,"type":"checkout.session.completed","data":{"object":{"id":"cs_integration","client_reference_id":"show-99","metadata":{"quantity":"1"}}}}`, stripeID))
	stripeHeaders := map[string]string{"Stripe-Signature": SignOutboundWebhook(stripePayload, config.StripeWebhookSecret, time.Now())}
	stripeResponse := productRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/integrations/stripe/webhook", stripePayload, "", "", stripeHeaders)
	defer stripeResponse.Body.Close()
	if stripeResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected Stripe webhook success, got %d", stripeResponse.StatusCode)
	}

	shopifyID := time.Now().UnixNano()
	shopifyPayload := []byte(fmt.Sprintf(`{"id":%d,"line_items":[{"quantity":1}],"note_attributes":[{"name":"event_id","value":"show-100"}]}`, shopifyID))
	shopifyHeaders := map[string]string{"X-Shopify-Hmac-Sha256": shopifySignature(shopifyPayload, config.ShopifyWebhookSecret)}
	shopifyResponse := productRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/integrations/shopify/webhook", shopifyPayload, "", "", shopifyHeaders)
	defer shopifyResponse.Body.Close()
	if shopifyResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected Shopify webhook success, got %d", shopifyResponse.StatusCode)
	}
}

// TestPostgresFallbackWithoutRedis validates durable idempotency and payload conflict handling when coordination is unavailable.
func TestPostgresFallbackWithoutRedis(t *testing.T) {
	databaseURL := os.Getenv("PRODUCT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PRODUCT_TEST_DATABASE_URL to run product integration tests")
	}
	ctx := context.Background()
	repository, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(repository, repository, unavailableCoordinator{}, nil, logger, "", 3)
	key := productID("fallback")

	first, err := service.CreateOrder(ctx, "tenant-fallback", key, "api", "", structOrder("show-fallback", 1))
	if err != nil || first.Replayed {
		t.Fatalf("create through fallback: result=%+v err=%v", first, err)
	}
	second, err := service.CreateOrder(ctx, "tenant-fallback", key, "api", "", structOrder("show-fallback", 1))
	if err != nil || !second.Replayed || second.Order.ID != first.Order.ID {
		t.Fatalf("replay through fallback: result=%+v err=%v", second, err)
	}
	_, err = service.CreateOrder(ctx, "tenant-fallback", key, "api", "", structOrder("show-fallback", 2))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected fallback conflict, got %v", err)
	}
}

type unavailableCoordinator struct{}

func (unavailableCoordinator) Do(context.Context, string, string, string, func(context.Context) (domain.Order, error)) (domain.Order, bool, error) {
	return domain.Order{}, false, ErrCoordinationUnavailable
}

func (unavailableCoordinator) Ping(context.Context) error {
	return ErrCoordinationUnavailable
}

func productRequest(t *testing.T, client *http.Client, method, url string, body []byte, apiKey, idempotencyKey string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("X-API-Key", apiKey)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("perform HTTP request: %v", err)
	}
	return response
}

func structOrder(eventID string, quantity int) domain.CreateOrderInput {
	return domain.CreateOrderInput{EventID: eventID, Quantity: quantity}
}

func shopifySignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
