# ADR 001: Keep reliability boundaries explicit and locally reproducible

- Status: Accepted
- Date: 2026-07-26

## Context

The lab must demonstrate production engineering judgment without reproducing
an employer system or requiring proprietary infrastructure. Its acceptance
scenarios need deterministic failures, visible recovery, and a setup that a
reviewer can run on a laptop.

Several hazards cross the HTTP handler boundary: duplicate submissions,
tenant-level overload, slow or unavailable dependencies, and disconnected
WebSocket consumers. Allowing each handler to improvise its own retry,
deduplication, or recovery policy would make these guarantees difficult to test
and easy to bypass.

## Decision

Use explicit, composable reliability boundaries around the synthetic order
operation:

1. authenticate the synthetic tenant before accessing owned resources;
2. admit requests through bounded tenant, API-key, and IP rate limits;
3. coordinate duplicates through a tenant-scoped idempotency record;
4. execute dependency calls through a timeout, bounded-retry, and
   circuit-breaker boundary;
5. append successful state transitions to a bounded, monotonically sequenced
   event buffer;
6. expose low-cardinality metrics and structured logs at each boundary.

Keep order/idempotency state, replay events, rate-limit state, and the
inventory simulator in process for this demo. Make the limitation explicit in
architecture documentation rather than implying distributed durability.

The administrative fault and reset endpoints exist only in demo mode. They use
synthetic state and are not patterns for an unauthenticated production control
plane.

## Consequences

### Positive

- Each guarantee has a focused acceptance scenario; operational boundaries also
  expose bounded metrics.
- Dependency failure remains bounded and cannot trigger unbounded retries.
- Concurrent duplicate handling is visible rather than delegated to an opaque
  platform component.
- The complete system is reproducible with Docker Compose.
- The design contains no employer code, configuration, data, or private
  architecture.

### Negative

- Process restart loses orders, idempotency records, breaker state, and replay
  history.
- Multiple API replicas would not share rate limits or duplicate coordination.
- The replay buffer cannot recover a client whose cursor is older than retained
  history.
- The local Grafana authentication model is unsuitable for public deployment.

## Production migration criteria

Before horizontal scaling or handling real transactions:

- move orders and idempotency records to one transactional durable store;
- use a durable event log/outbox and define retention/reconciliation behavior;
- coordinate distributed rate limits or enforce them at a trusted edge;
- protect administrative actions with authenticated authorization and audit;
- define service-level objectives, alert policies, data recovery objectives,
  and secret rotation procedures;
- repeat failure and load tests against the deployed topology.

These are conscious migration boundaries. They are not functionality claimed
by this portfolio lab.
