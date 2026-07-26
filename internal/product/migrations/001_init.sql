CREATE TABLE IF NOT EXISTS product_events (
    sequence BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,
    order_id TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    status TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS product_events_tenant_sequence_idx
    ON product_events (tenant_id, sequence);

CREATE TABLE IF NOT EXISTS product_orders (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    source TEXT NOT NULL,
    external_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 12),
    status TEXT NOT NULL,
    sequence BIGINT NOT NULL REFERENCES product_events(sequence),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS product_orders_tenant_sequence_idx
    ON product_orders (tenant_id, sequence);

CREATE TABLE IF NOT EXISTS product_webhook_deliveries (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    target_url TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS product_webhook_deliveries_due_idx
    ON product_webhook_deliveries (status, next_attempt_at);

CREATE INDEX IF NOT EXISTS product_webhook_deliveries_tenant_created_idx
    ON product_webhook_deliveries (tenant_id, created_at DESC);
