package product

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address               string
	DatabaseURL           string
	RedisURL              string
	APIKeys               map[string]string
	IntegrationTenantID   string
	OutboundWebhookURL    string
	OutboundWebhookSecret string
	StripeWebhookSecret   string
	ShopifyWebhookSecret  string
	WebhookMaxAttempts    int
	WebhookPollInterval   time.Duration
	WebhookLease          time.Duration
	WebhookRequestTimeout time.Duration
}

func LoadConfig() (Config, error) {
	webhookMaxAttempts, err := envInt("WEBHOOK_MAX_ATTEMPTS", 5)
	if err != nil {
		return Config{}, err
	}
	webhookPollInterval, err := envDuration("WEBHOOK_POLL_INTERVAL", 250*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	webhookLease, err := envDuration("WEBHOOK_LEASE", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	webhookRequestTimeout, err := envDuration("WEBHOOK_REQUEST_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	apiKeys, err := parseAPIKeys(os.Getenv("PRODUCT_API_KEYS"))
	if err != nil {
		return Config{}, err
	}
	config := Config{
		Address:               envOrDefault("PRODUCT_ADDRESS", ":8081"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		RedisURL:              os.Getenv("REDIS_URL"),
		IntegrationTenantID:   envOrDefault("INTEGRATION_TENANT_ID", "tenant-alpha"),
		OutboundWebhookURL:    os.Getenv("OUTBOUND_WEBHOOK_URL"),
		OutboundWebhookSecret: os.Getenv("OUTBOUND_WEBHOOK_SECRET"),
		StripeWebhookSecret:   os.Getenv("STRIPE_WEBHOOK_SECRET"),
		ShopifyWebhookSecret:  os.Getenv("SHOPIFY_WEBHOOK_SECRET"),
		WebhookMaxAttempts:    webhookMaxAttempts,
		WebhookPollInterval:   webhookPollInterval,
		WebhookLease:          webhookLease,
		WebhookRequestTimeout: webhookRequestTimeout,
		APIKeys:               apiKeys,
	}

	if config.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if config.RedisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required")
	}
	if len(config.APIKeys) == 0 {
		return Config{}, fmt.Errorf("PRODUCT_API_KEYS must contain at least one key=tenant mapping")
	}
	if config.IntegrationTenantID == "" || len(config.IntegrationTenantID) > 100 {
		return Config{}, fmt.Errorf("INTEGRATION_TENANT_ID must be between 1 and 100 characters")
	}
	if config.OutboundWebhookURL != "" {
		if config.OutboundWebhookSecret == "" {
			return Config{}, fmt.Errorf("OUTBOUND_WEBHOOK_SECRET is required when OUTBOUND_WEBHOOK_URL is set")
		}
		parsedURL, err := url.ParseRequestURI(config.OutboundWebhookURL)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
			return Config{}, fmt.Errorf("OUTBOUND_WEBHOOK_URL must be an absolute HTTP(S) URL")
		}
	}
	if config.WebhookMaxAttempts < 1 || config.WebhookPollInterval <= 0 || config.WebhookLease <= 0 || config.WebhookRequestTimeout <= 0 {
		return Config{}, fmt.Errorf("webhook attempts and durations must be positive")
	}
	if config.WebhookLease <= config.WebhookRequestTimeout {
		return Config{}, fmt.Errorf("WEBHOOK_LEASE must be longer than WEBHOOK_REQUEST_TIMEOUT")
	}
	return config, nil
}

func parseAPIKeys(value string) (map[string]string, error) {
	result := make(map[string]string)
	for entry := range strings.SplitSeq(value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		key, tenant, ok := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		tenant = strings.TrimSpace(tenant)
		if !ok || key == "" || tenant == "" {
			return nil, fmt.Errorf("PRODUCT_API_KEYS contains an invalid key=tenant mapping")
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("PRODUCT_API_KEYS contains duplicate key %q", key)
		}
		result[key] = tenant
	}
	return result, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return value, nil
}
