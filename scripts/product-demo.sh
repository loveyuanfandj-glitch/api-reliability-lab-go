#!/usr/bin/env sh
set -eu

base_url="${PRODUCT_BASE_URL:-http://127.0.0.1:58081}"
api_key="${PRODUCT_API_KEY:-tenant-alpha-key}"
demo_key="product-demo-$(date +%s)"

echo "[1/4] Checking PostgreSQL and Redis readiness"
curl --fail --silent --show-error "$base_url/readyz"
echo

echo "[2/4] Creating one durable order"
first_response=$(curl --fail --silent --show-error -X POST "$base_url/v1/orders" \
  -H 'Content-Type: application/json' \
  -H "X-API-Key: $api_key" \
  -H "Idempotency-Key: $demo_key" \
  -d '{"event_id":"event-product-demo","quantity":2}')
echo "$first_response" | jq '{order_id: .order.id, replayed: .replayed}'
first_order_id=$(echo "$first_response" | jq -r '.order.id')

echo "[3/4] Replaying the same request without creating another order"
replay_response=$(curl --fail --silent --show-error -X POST "$base_url/v1/orders" \
  -H 'Content-Type: application/json' \
  -H "X-API-Key: $api_key" \
  -H "Idempotency-Key: $demo_key" \
  -d '{"event_id":"event-product-demo","quantity":2}')
replay_order_id=$(echo "$replay_response" | jq -r '.order.id')
replayed=$(echo "$replay_response" | jq -r '.replayed')
jq -n --arg first "$first_order_id" --arg replay "$replay_order_id" --argjson replayed "$replayed" \
  '{same_order: ($first == $replay), order_id: $replay, replayed: $replayed}'

echo "[4/4] Waiting for the failure-injecting receiver to accept the signed webhook"
attempt=0
while [ "$attempt" -lt 20 ]; do
  delivery=$(curl --fail --silent --show-error "$base_url/v1/webhook-deliveries?limit=1" -H "X-API-Key: $api_key")
  delivery_state=$(echo "$delivery" | jq -r '.deliveries[0].status // "waiting"')
  if [ "$delivery_state" = "delivered" ]; then
    echo "$delivery" | jq '{status: .deliveries[0].status, attempts: .deliveries[0].attempts, last_error: .deliveries[0].last_error}'
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 1
done

echo "Webhook did not reach delivered state in time" >&2
exit 1
