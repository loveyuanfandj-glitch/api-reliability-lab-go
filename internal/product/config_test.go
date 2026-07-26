package product

import (
	"strings"
	"testing"
	"time"
)

// TestLoadConfigAcceptsValidProductSettings validates explicit tenant mappings, webhook URL, and worker timing values.
func TestLoadConfigAcceptsValidProductSettings(t *testing.T) {
	setValidConfig(t)
	t.Setenv("WEBHOOK_MAX_ATTEMPTS", "7")
	t.Setenv("WEBHOOK_POLL_INTERVAL", "500ms")

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load valid config: %v", err)
	}
	if config.APIKeys["alpha-key"] != "tenant-alpha" || config.WebhookMaxAttempts != 7 || config.WebhookPollInterval != 500*time.Millisecond {
		t.Fatalf("unexpected config: %+v", config)
	}
}

// TestLoadConfigRejectsInvalidWorkerSettings validates fail-fast behavior for malformed and non-positive operational values.
func TestLoadConfigRejectsInvalidWorkerSettings(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "invalid attempts", key: "WEBHOOK_MAX_ATTEMPTS", value: "many"},
		{name: "zero attempts", key: "WEBHOOK_MAX_ATTEMPTS", value: "0"},
		{name: "invalid poll duration", key: "WEBHOOK_POLL_INTERVAL", value: "soon"},
		{name: "zero poll duration", key: "WEBHOOK_POLL_INTERVAL", value: "0s"},
		{name: "negative lease", key: "WEBHOOK_LEASE", value: "-1s"},
		{name: "lease shorter than request timeout", key: "WEBHOOK_LEASE", value: "4s"},
		{name: "zero request timeout", key: "WEBHOOK_REQUEST_TIMEOUT", value: "0s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidConfig(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("expected %s=%q to fail", test.key, test.value)
			}
		})
	}
}

// TestLoadConfigRejectsMalformedIdentityAndWebhookSettings validates strict key mappings and absolute HTTP receiver URLs.
func TestLoadConfigRejectsMalformedIdentityAndWebhookSettings(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		message string
	}{
		{name: "malformed key mapping", key: "PRODUCT_API_KEYS", value: "alpha-key", message: "invalid key=tenant"},
		{name: "duplicate key mapping", key: "PRODUCT_API_KEYS", value: "alpha-key=tenant-a,alpha-key=tenant-b", message: "duplicate key"},
		{name: "relative webhook URL", key: "OUTBOUND_WEBHOOK_URL", value: "/webhook", message: "absolute HTTP"},
		{name: "unsupported webhook scheme", key: "OUTBOUND_WEBHOOK_URL", value: "file:///tmp/hook", message: "absolute HTTP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidConfig(t)
			t.Setenv(test.key, test.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected error containing %q, got %v", test.message, err)
			}
		})
	}
}

func setValidConfig(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://example.invalid/northstar")
	t.Setenv("REDIS_URL", "redis://example.invalid:6379/0")
	t.Setenv("PRODUCT_API_KEYS", "alpha-key=tenant-alpha")
	t.Setenv("INTEGRATION_TENANT_ID", "tenant-alpha")
	t.Setenv("OUTBOUND_WEBHOOK_URL", "https://receiver.example/webhook")
	t.Setenv("OUTBOUND_WEBHOOK_SECRET", "test-secret")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")
	t.Setenv("SHOPIFY_WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_MAX_ATTEMPTS", "")
	t.Setenv("WEBHOOK_POLL_INTERVAL", "")
	t.Setenv("WEBHOOK_LEASE", "")
	t.Setenv("WEBHOOK_REQUEST_TIMEOUT", "")
}
