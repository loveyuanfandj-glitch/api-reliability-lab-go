package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/app"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/store"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/telemetry"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/upstream"
	"github.com/prometheus/client_golang/prometheus"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("WEB_DIR", "../../web")
	t.Setenv("DEMO_MODE", "true")
	metrics := telemetry.NewMetrics(prometheus.NewRegistry())
	service := app.NewService(store.NewMemory(100), upstream.NewSimulator(), metrics)
	return NewServer(service, metrics, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

func TestIdempotencyPayloadConflictReturns409(t *testing.T) {
	handler := testHandler(t)
	requestOrder(t, handler, "tenant-alpha-key", "conflict-key")
	body := bytes.NewBufferString(`{"event_id":"different-event","quantity":2}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", body)
	request.Header.Set("X-API-Key", "tenant-alpha-key")
	request.Header.Set("Idempotency-Key", "conflict-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
}

func TestOrderLifecycleAndTenantIsolation(t *testing.T) {
	handler := testHandler(t)
	first := requestOrder(t, handler, "tenant-alpha-key", "stable-key")
	second := requestOrder(t, handler, "tenant-alpha-key", "stable-key")
	if first.Order.ID != second.Order.ID || !second.Replayed {
		t.Fatalf("idempotency failed: first=%#v second=%#v", first, second)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/orders/"+first.Order.ID, nil)
	request.Header.Set("X-API-Key", "tenant-beta-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read status = %d, want 404", response.Code)
	}
}

func TestWebSocketReplaysMissedEvents(t *testing.T) {
	handler := testHandler(t)
	first := requestOrder(t, handler, "tenant-alpha-key", "ws-one")
	second := requestOrder(t, handler, "tenant-alpha-key", "ws-two")

	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"X-API-Key": []string{"tenant-alpha-key"}}
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/stream?since="+jsonNumber(first.Order.Sequence), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer connection.CloseNow()
	var event domain.Event
	if err := wsjson.Read(ctx, connection, &event); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if event.Sequence != second.Order.Sequence || event.OrderID != second.Order.ID {
		t.Fatalf("unexpected replay event: %#v", event)
	}
}

func TestDashboardAndHealth(t *testing.T) {
	handler := testHandler(t)
	for _, path := range []string{"/", "/styles.css", "/app.js", "/healthz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("GET %s: status=%d bytes=%d", path, response.Code, response.Body.Len())
		}
	}
}

func requestOrder(t *testing.T, handler http.Handler, apiKey, idempotencyKey string) app.CreateResult {
	t.Helper()
	body := bytes.NewBufferString(`{"event_id":"event-integration","quantity":2}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", apiKey)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", response.Code, response.Body.String())
	}
	var result app.CreateResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func jsonNumber(value uint64) string {
	return strings.TrimSpace(string(mustJSON(value)))
}

func mustJSON(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}
