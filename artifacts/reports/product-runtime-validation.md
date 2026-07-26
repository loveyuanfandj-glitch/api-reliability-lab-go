# Product runtime validation

Validated locally on 2026-07-26 before publishing the product-runtime change.

## Environment

- Base revision: `5cc4990`
- Go: `go1.25.7 darwin/arm64`
- Security scan toolchain: `go1.25.12 darwin/arm64` (matching CI/container)
- Docker client/server: `29.4.3` / `29.2.1`
- PostgreSQL test container: `postgres:17-alpine`
- Redis test container: `redis:7-alpine`

## Automated checks

```text
go vet ./...                                                        PASS
go test -race -count=1 ./...                                       PASS
PRODUCT_TEST_DATABASE_URL=... PRODUCT_TEST_REDIS_URL=... \
  go test -race -count=1 \
  -run 'TestProductRuntimeEndToEnd|TestPostgresFallbackWithoutRedis' \
  ./internal/product                                                PASS
go build ./cmd/server ./cmd/replay ./cmd/product-server \
  ./cmd/webhook-sink                                                PASS
docker build -f Dockerfile.product -t northstar-product-api:test . PASS
GOTOOLCHAIN=go1.25.12 govulncheck ./...                             PASS (0 reachable vulnerabilities)
```

The integration assertions verified:

- 20 concurrent HTTP submissions with one tenant/idempotency key produced one durable order;
- identical retries returned the original order, while a changed fingerprint returned conflict;
- another tenant could not list that order;
- order, event, and outbound delivery were created through the same PostgreSQL transaction;
- a valid signed receiver failed twice and accepted the third attempt;
- the resulting delivery was `delivered` with `attempts: 3`;
- Stripe- and Shopify-shaped signed sandbox payloads created orders;
- an unavailable Redis coordinator fell back to PostgreSQL without allowing duplicate or conflicting writes.

## Post-implementation bug audit

A second review added regression coverage for production-shaped provider payloads and operational edge cases:

- Stripe/Shopify payloads may contain unknown provider fields while the public order API remains strict;
- malformed but correctly signed provider JSON returns `400`, not an internal `500`;
- Redis coordination keys hash the tenant/key tuple so delimiter-like values cannot cross tenant boundaries;
- corrupt Redis result data activates the PostgreSQL fallback instead of returning a cached zero value;
- worker configuration rejects invalid durations and leases shorter than the HTTP timeout;
- claimed webhook batches execute concurrently so later jobs do not expire their leases while waiting behind earlier network calls;
- repeated PostgreSQL integration runs reset only the explicitly configured test tables, preventing stale outbox jobs from making results order-dependent;
- concurrent Redis-unavailable requests still produce exactly one PostgreSQL order.

## Condensed container acceptance evidence

The order response below omits run-specific timestamps for readability; IDs and status values are from the captured run.

```text
GET /readyz
{"status":"ready"}

POST /v1/orders
HTTP/1.1 201 Created
{"order":{"id":"ord_cb2f26330302256bdc5639a8","tenant_id":"tenant-alpha","event_id":"event-product-demo","quantity":2,"status":"confirmed","sequence":5},"replayed":false}

POST /v1/orders (same key and payload)
HTTP/1.1 200 OK
{"order_id":"ord_cb2f26330302256bdc5639a8","replayed":true}

POST /v1/orders (same key, changed quantity)
HTTP 409
{"error":{"code":"idempotency_conflict","message":"idempotency key reused with a different payload"}}

GET /v1/webhook-deliveries?limit=1
{"status":"delivered","attempts":3,"last_error":null}
```

Order IDs, sequences, timestamps, and timing are run-specific synthetic evidence. This report makes no context-free throughput or latency claim.
