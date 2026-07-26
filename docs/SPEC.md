# Public project specification

## Goal

Demonstrate how a small SaaS team can harden an unreliable multi-tenant order
API without relying on proprietary infrastructure. The system must make
duplicates, overload, dependency failures, and WebSocket gaps visible and
testable.

## Synthetic business scenario

Northstar Tickets is a fictional event-ticketing platform. Tenants submit
ticket reservation orders through a REST API and watch status changes through
a WebSocket feed. A synthetic inventory dependency can be switched between
healthy, slow, flaky, and unavailable modes.

## Public HTTP contract

- `GET /healthz` - liveness
- `GET /readyz` - readiness and dependency state
- `GET /metrics` - Prometheus metrics
- `POST /v1/orders` - create a reservation; requires `X-API-Key` and
  `Idempotency-Key`
- `GET /v1/orders/{id}` - fetch an order owned by the authenticated tenant
- `GET /v1/stream?since=<sequence>` - WebSocket event stream with gap recovery
- `GET /v1/demo/snapshot` - synthetic dashboard state
- `POST /v1/demo/fault` - set the dependency mode in demo mode only
- `POST /v1/demo/reset` - reset synthetic state in demo mode only

The demo API keys are `tenant-alpha-key` and `tenant-beta-key`. They are
synthetic identifiers, not secrets.

## Reliability guarantees

1. Repeating a request with the same tenant and idempotency key returns the
   original order instead of creating a duplicate.
2. Per-tenant and per-key rate limits isolate noisy clients.
3. Dependency calls use bounded timeouts, retries with jitter, and a circuit
   breaker.
4. Every status transition receives a monotonically increasing sequence.
5. Reconnecting WebSocket clients can request missed events within the bounded
   replay window.
6. Failures produce structured logs and Prometheus counters.

## Explicit non-goals

- Real payments or ticket inventory
- Production identity management
- Multi-region consensus
- Claims about employer systems or traffic

## Acceptance scenarios

### Duplicate suppression

Submit the same order 20 times concurrently with one idempotency key. Exactly
one order is stored and all successful responses contain the same order ID.

### Dependency failure and recovery

Switch inventory to `unavailable`, generate traffic until the circuit opens,
then switch it to `healthy`. The service fails fast while open and records a
successful recovery after the cooldown.

### WebSocket gap recovery

Disconnect a client, create additional orders, then reconnect with the last
seen sequence. The client receives every retained event after that sequence in
order.
