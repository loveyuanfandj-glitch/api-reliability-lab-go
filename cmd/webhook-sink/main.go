package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/product"
)

type receivedDelivery struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	At        time.Time       `json:"received_at"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	secret := os.Getenv("OUTBOUND_WEBHOOK_SECRET")
	address := envOrDefault("SINK_ADDRESS", ":8090")
	failures, _ := strconv.ParseInt(os.Getenv("SINK_FAIL_FIRST"), 10, 64)
	var remaining atomic.Int64
	remaining.Store(failures)
	var mu sync.RWMutex
	deliveries := make([]receivedDelivery, 0, 50)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /deliveries", func(response http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		snapshot := append([]receivedDelivery(nil), deliveries...)
		mu.RUnlock()
		writeJSON(response, http.StatusOK, map[string]any{"deliveries": snapshot})
	})
	mux.HandleFunc("POST /webhook", func(response http.ResponseWriter, request *http.Request) {
		payload, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1<<20))
		if err != nil {
			http.Error(response, "invalid payload", http.StatusBadRequest)
			return
		}
		if err := product.VerifyOutboundWebhook(request.Header.Get("X-Northstar-Signature"), payload, secret, time.Now(), 5*time.Minute); err != nil {
			http.Error(response, "invalid signature", http.StatusUnauthorized)
			return
		}
		if remaining.Load() > 0 && remaining.Add(-1) >= 0 {
			http.Error(response, "simulated receiver outage", http.StatusServiceUnavailable)
			return
		}
		mu.Lock()
		deliveries = append(deliveries, receivedDelivery{
			ID:        request.Header.Get("X-Northstar-Delivery"),
			EventType: request.Header.Get("X-Northstar-Event"),
			Payload:   payload,
			At:        time.Now().UTC(),
		})
		if len(deliveries) > 50 {
			deliveries = append([]receivedDelivery(nil), deliveries[len(deliveries)-50:]...)
		}
		mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	})

	logger.Info("webhook sink listening", "address", address, "fail_first", failures)
	if err := http.ListenAndServe(address, mux); err != nil {
		logger.Error("webhook sink stopped", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
