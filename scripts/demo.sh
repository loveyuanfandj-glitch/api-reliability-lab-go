#!/usr/bin/env bash
set -euo pipefail

API_URL="${API_URL:-http://localhost:8080}"
API_KEY="tenant-alpha-key"
DEMO_DIR="$(mktemp -d -t northstar-demo.XXXXXX)"

cleanup() {
  rm -rf "${DEMO_DIR}"
}
trap cleanup EXIT

section() {
  printf '\n\033[1;36m%s\033[0m\n' "$1"
}

request_order() {
  local key="$1"
  local output="$2"
  curl --silent --show-error \
    --output "${output}" \
    --write-out '%{http_code}' \
    -X POST "${API_URL}/v1/orders" \
    -H 'Content-Type: application/json' \
    -H "X-API-Key: ${API_KEY}" \
    -H "Idempotency-Key: ${key}" \
    -d '{"event_id":"event-live-demo","quantity":2}'
}

order_id() {
  local file="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '.id // .order.id // empty' "${file}"
    return
  fi
  sed -nE 's/.*"id"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "${file}"
}

section '1/5 Waiting for the API'
for attempt in {1..30}; do
  if curl --silent --fail "${API_URL}/readyz" >/dev/null; then
    printf 'API is ready at %s\n' "${API_URL}"
    break
  fi
  if [[ "${attempt}" -eq 30 ]]; then
    printf 'API did not become ready. Run `make up` and inspect `make logs`.\n' >&2
    exit 1
  fi
  sleep 1
done

curl --silent --fail -X POST "${API_URL}/v1/demo/reset" >/dev/null
curl --silent --fail -X POST "${API_URL}/v1/demo/fault" \
  -H 'Content-Type: application/json' \
  -d '{"mode":"healthy"}' >/dev/null

section '2/5 Proving duplicate suppression with 20 concurrent requests'
duplicate_key="demo-duplicate-$(date +%s)"
pids=()
for index in $(seq 1 20); do
  (
    request_order "${duplicate_key}" "${DEMO_DIR}/duplicate-${index}.json" \
      >"${DEMO_DIR}/duplicate-${index}.status"
  ) &
  pids+=("$!")
done
for pid in "${pids[@]}"; do
  wait "${pid}"
done

successful=0
for status_file in "${DEMO_DIR}"/duplicate-*.status; do
  status="$(<"${status_file}")"
  if [[ "${status}" == "200" || "${status}" == "201" ]]; then
    successful=$((successful + 1))
  fi
done
unique_ids="$(for file in "${DEMO_DIR}"/duplicate-*.json; do order_id "${file}"; done | sort -u | sed '/^$/d' | wc -l | tr -d ' ')"
printf 'Successful responses: %s/20\nUnique order IDs: %s (expected: 1)\n' "${successful}" "${unique_ids}"
if [[ "${successful}" -ne 20 || "${unique_ids}" -ne 1 ]]; then
  printf 'Duplicate-suppression acceptance check failed.\n' >&2
  exit 1
fi

section '3/5 Injecting an inventory outage and opening the circuit'
curl --silent --fail -X POST "${API_URL}/v1/demo/fault" \
  -H 'Content-Type: application/json' \
  -d '{"mode":"unavailable"}' >/dev/null

for index in $(seq 1 12); do
  status="$(request_order "demo-outage-$(date +%s)-${index}" "${DEMO_DIR}/outage-${index}.json")"
  printf 'outage request %02d -> HTTP %s\n' "${index}" "${status}"
done
printf 'Later requests should fail fast once the circuit is open.\n'

section '4/5 Restoring the dependency and observing recovery'
curl --silent --fail -X POST "${API_URL}/v1/demo/fault" \
  -H 'Content-Type: application/json' \
  -d '{"mode":"healthy"}' >/dev/null
recovery_wait="${RECOVERY_WAIT_SECONDS:-6}"
printf 'Waiting %ss for the breaker cooldown...\n' "${recovery_wait}"
sleep "${recovery_wait}"
recovery_status="$(request_order "demo-recovery-$(date +%s)" "${DEMO_DIR}/recovery.json")"
printf 'Recovery probe -> HTTP %s\n' "${recovery_status}"
if [[ "${recovery_status}" != "200" && "${recovery_status}" != "201" ]]; then
  printf 'Recovery probe failed. Inspect the snapshot and API logs.\n' >&2
  exit 1
fi

section '5/5 Snapshot and observability links'
if command -v jq >/dev/null 2>&1; then
  curl --silent --fail "${API_URL}/v1/demo/snapshot" | jq .
else
  curl --silent --fail "${API_URL}/v1/demo/snapshot"
  printf '\n'
fi
printf '\nGrafana:   http://localhost:3000/d/northstar-reliability\n'
printf 'Prometheus: http://localhost:9090/targets\n'
printf '\nDemo complete: duplicates were suppressed and dependency failure/recovery was observable.\n'
