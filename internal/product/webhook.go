package product

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type WebhookWorker struct {
	repository   DeliveryRepository
	client       *http.Client
	secret       string
	lease        time.Duration
	pollInterval time.Duration
	batchSize    int
	metrics      *Metrics
	logger       *slog.Logger
	now          func() time.Time
}

func NewWebhookWorker(repository DeliveryRepository, client *http.Client, secret string, lease, pollInterval time.Duration, metrics *Metrics, logger *slog.Logger) *WebhookWorker {
	return &WebhookWorker{
		repository:   repository,
		client:       client,
		secret:       secret,
		lease:        lease,
		pollInterval: pollInterval,
		batchSize:    10,
		metrics:      metrics,
		logger:       logger,
		now:          time.Now,
	}
}

func (w *WebhookWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.ProcessBatch(ctx); err != nil && ctx.Err() == nil {
			w.logger.Error("webhook worker batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *WebhookWorker) ProcessBatch(ctx context.Context) (int, error) {
	deliveries, err := w.repository.ClaimDeliveries(ctx, w.batchSize, w.lease)
	if err != nil {
		return 0, err
	}
	var group sync.WaitGroup
	for _, delivery := range deliveries {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := w.process(ctx, delivery); err != nil {
				w.logger.Warn("webhook delivery attempt failed", "delivery_id", delivery.ID, "attempt", delivery.Attempts, "error", err)
			}
		}()
	}
	group.Wait()
	return len(deliveries), nil
}

func (w *WebhookWorker) process(ctx context.Context, delivery WebhookDelivery) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.TargetURL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return w.fail(ctx, delivery, err)
	}
	now := w.now().UTC()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Northstar-Webhook/1.0")
	request.Header.Set("X-Northstar-Delivery", delivery.ID)
	request.Header.Set("X-Northstar-Event", delivery.EventType)
	request.Header.Set("X-Northstar-Signature", SignOutboundWebhook(delivery.Payload, w.secret, now))

	response, err := w.client.Do(request)
	if err != nil {
		return w.fail(ctx, delivery, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return w.fail(ctx, delivery, fmt.Errorf("receiver returned %d: %s", response.StatusCode, strings.TrimSpace(string(body))))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
	if err := w.repository.MarkDeliverySucceeded(ctx, delivery.ID); err != nil {
		return err
	}
	w.recordDelivery("delivered")
	return nil
}

func (w *WebhookWorker) fail(ctx context.Context, delivery WebhookDelivery, cause error) error {
	delay := webhookBackoff(delivery.Attempts)
	status, err := w.repository.MarkDeliveryFailed(ctx, delivery, cause.Error(), delay)
	if err != nil {
		return fmt.Errorf("record failed delivery after %v: %w", cause, err)
	}
	if status == DeliveryDeadLetter {
		w.recordDelivery("dead_letter")
	} else {
		w.recordDelivery("retry_scheduled")
	}
	return cause
}

func (w *WebhookWorker) recordDelivery(result string) {
	if w.metrics != nil {
		w.metrics.WebhookDeliveries.WithLabelValues(result).Inc()
	}
}

func webhookBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 6)
	return min(delay, time.Minute)
}
