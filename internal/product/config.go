package product

import (
	"fmt"
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
	config := Config{
		Address:               envOrDefault("PRODUCT_ADDRESS", ":8081"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		RedisURL:              os.Getenv("REDIS_URL"),
		IntegrationTenantID:   envOrDefault("INTEGRATION_TENANT_ID", "tenant-alpha"),
		OutboundWebhookURL:    os.Getenv("OUTBOUND_WEBHOOK_URL"),
		OutboundWebhookSecret: os.Getenv("OUTBOUND_WEBHOOK_SECRET"),
		StripeWebhookSecret:   os.Getenv("STRIPE_WEBHOOK_SECRET"),
		ShopifyWebhookSecret:  os.Getenv("SHOPIFY_WEBHOOK_SECRET"),
		WebhookMaxAttempts:    envInt("WEBHOOK_MAX_ATTEMPTS", 5),
		WebhookPollInterval:   envDuration("WEBHOOK_POLL_INTERVAL", 250*time.Millisecond),
		WebhookLease:          envDuration("WEBHOOK_LEASE", 30*time.Second),
		WebhookRequestTimeout: envDuration("WEBHOOK_REQUEST_TIMEOUT", 5*time.Second),
	}
	config.APIKeys = parseAPIKeys(os.Getenv("PRODUCT_API_KEYS"))

	if config.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if config.RedisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required")
	}
	if len(config.APIKeys) == 0 {
		return Config{}, fmt.Errorf("PRODUCT_API_KEYS must contain at least one key=tenant mapping")
	}
	if config.OutboundWebhookURL != "" && config.OutboundWebhookSecret == "" {
		return Config{}, fmt.Errorf("OUTBOUND_WEBHOOK_SECRET is required when OUTBOUND_WEBHOOK_URL is set")
	}
	if config.WebhookMaxAttempts < 1 {
		return Config{}, fmt.Errorf("WEBHOOK_MAX_ATTEMPTS must be positive")
	}
	return config, nil
}

func parseAPIKeys(value string) map[string]string {
	result := make(map[string]string)
	for entry := range strings.SplitSeq(value, ",") {
		key, tenant, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if ok && key != "" && tenant != "" {
			result[key] = tenant
		}
	}
	return result
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
