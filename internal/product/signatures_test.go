package product

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// TestTimestampedSignatures validates valid, tampered, and expired Stripe-style and outbound signatures.
func TestTimestampedSignatures(t *testing.T) {
	payload := []byte(`{"id":"evt_123"}`)
	secret := "whsec_test"
	now := time.Unix(1_750_000_000, 0)
	signature := SignOutboundWebhook(payload, secret, now)

	if err := VerifyOutboundWebhook(signature, payload, secret, now.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("verify outbound signature: %v", err)
	}
	if err := VerifyStripeSignature(signature, append(payload, 'x'), secret, now, 5*time.Minute); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected tampered payload to fail, got %v", err)
	}
	if err := VerifyStripeSignature(signature, payload, secret, now.Add(10*time.Minute), 5*time.Minute); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected expired signature to fail, got %v", err)
	}
}

// TestShopifySignature validates the base64 HMAC format used by Shopify webhooks and rejects tampering.
func TestShopifySignature(t *testing.T) {
	payload := []byte(`{"id":123}`)
	secret := "shopify_test"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if err := VerifyShopifySignature(signature, payload, secret); err != nil {
		t.Fatalf("verify Shopify signature: %v", err)
	}
	if err := VerifyShopifySignature(signature, []byte(`{"id":124}`), secret); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected tampered Shopify payload to fail, got %v", err)
	}
}
