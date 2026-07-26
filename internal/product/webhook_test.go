package product

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type webhookRepositoryStub struct {
	mu         sync.Mutex
	succeeded  []string
	statuses   []DeliveryStatus
	lastErrors []string
}

func (r *webhookRepositoryStub) ClaimDeliveries(context.Context, int, time.Duration) ([]WebhookDelivery, error) {
	return nil, nil
}

func (r *webhookRepositoryStub) MarkDeliverySucceeded(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.succeeded = append(r.succeeded, id)
	return nil
}

func (r *webhookRepositoryStub) MarkDeliveryFailed(_ context.Context, delivery WebhookDelivery, message string, _ time.Duration) (DeliveryStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := DeliveryPending
	if delivery.Attempts >= delivery.MaxAttempts {
		status = DeliveryDeadLetter
	}
	r.statuses = append(r.statuses, status)
	r.lastErrors = append(r.lastErrors, message)
	return status, nil
}

func (r *webhookRepositoryStub) ListDeliveries(context.Context, string, DeliveryStatus, int) ([]WebhookDelivery, error) {
	return nil, nil
}

func (r *webhookRepositoryStub) ReplayDelivery(context.Context, string, string) error {
	return nil
}

// TestWebhookWorkerSignsSuccessfulDelivery validates the exact payload, delivery metadata, and HMAC signature sent to a receiver.
func TestWebhookWorkerSignsSuccessfulDelivery(t *testing.T) {
	secret := "outbound_test"
	now := time.Unix(1_750_000_000, 0)
	payload := []byte(`{"type":"order.confirmed"}`)
	repository := &webhookRepositoryStub{}
	receiver := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if string(body) != string(payload) {
			t.Errorf("unexpected payload: %s", body)
		}
		if request.Header.Get("X-Northstar-Delivery") != "wh_1" {
			t.Errorf("missing delivery id header")
		}
		if err := VerifyOutboundWebhook(request.Header.Get("X-Northstar-Signature"), body, secret, now, time.Second); err != nil {
			t.Errorf("verify delivered signature: %v", err)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	worker := NewWebhookWorker(repository, receiver.Client(), secret, time.Minute, time.Second, nil, slog.Default())
	worker.now = func() time.Time { return now }

	err := worker.process(context.Background(), WebhookDelivery{
		ID: "wh_1", EventType: "order.confirmed", Payload: payload, TargetURL: receiver.URL, Attempts: 1, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("process webhook: %v", err)
	}
	if len(repository.succeeded) != 1 || repository.succeeded[0] != "wh_1" {
		t.Fatalf("delivery was not marked successful: %+v", repository.succeeded)
	}
}

// TestWebhookWorkerRetriesThenDeadLetters validates retry scheduling before the configured terminal attempt.
func TestWebhookWorkerRetriesThenDeadLetters(t *testing.T) {
	repository := &webhookRepositoryStub{}
	receiver := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()
	worker := NewWebhookWorker(repository, receiver.Client(), "secret", time.Minute, time.Second, nil, slog.Default())
	delivery := WebhookDelivery{ID: "wh_1", EventType: "order.confirmed", Payload: []byte(`{}`), TargetURL: receiver.URL, MaxAttempts: 2}

	delivery.Attempts = 1
	if err := worker.process(context.Background(), delivery); err == nil {
		t.Fatal("expected first delivery attempt to fail")
	}
	delivery.Attempts = 2
	if err := worker.process(context.Background(), delivery); err == nil {
		t.Fatal("expected final delivery attempt to fail")
	}
	if len(repository.statuses) != 2 || repository.statuses[0] != DeliveryPending || repository.statuses[1] != DeliveryDeadLetter {
		t.Fatalf("unexpected delivery states: %+v", repository.statuses)
	}
	if len(repository.lastErrors) != 2 || repository.lastErrors[0] == "" {
		t.Fatal("receiver errors were not retained")
	}
}

// TestWebhookBackoffCapsDelay validates exponential retry growth and the one-minute upper bound.
func TestWebhookBackoffCapsDelay(t *testing.T) {
	if delay := webhookBackoff(1); delay != time.Second {
		t.Fatalf("unexpected first delay: %v", delay)
	}
	if delay := webhookBackoff(20); delay != time.Minute {
		t.Fatalf("expected capped delay, got %v", delay)
	}
}

var _ DeliveryRepository = (*webhookRepositoryStub)(nil)
