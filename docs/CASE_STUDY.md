# Case study: making API failure safe and observable

## Executive summary

Northstar is an independently built Go reliability lab for a fictional multi-tenant ticketing service. It demonstrates how I approach a backend that must stay correct when clients retry, one tenant creates an overload, a critical dependency fails, or a real-time consumer disconnects.

The deliverable is not only an API implementation. It includes executable acceptance scenarios, fault injection, structured logs, Prometheus metrics, a provisioned Grafana dashboard, an operator runbook, and explicit production migration boundaries. A reviewer can reproduce each result locally instead of trusting an architecture diagram or an unverifiable benchmark.

## Problem

Order, booking, payment, and webhook systems commonly encounter four linked failure modes:

- a timed-out client retries and creates the same business action twice;
- a noisy tenant consumes shared request capacity;
- a slow or unavailable third-party dependency holds workers and amplifies retries;
- a disconnected event consumer misses state transitions and cannot reconcile.

These risks become expensive when behavior is implicit. Without a stable idempotency contract, a retry can corrupt business state. Without a bounded dependency policy, an upstream outage can spread into the application. Without meaningful signals and a runbook, the customer often becomes the monitoring system.

The project therefore treats reliability properties as acceptance criteria, not as framework choices.

## Approach

### 1. Define the public behavior first

The service exposes a small REST and WebSocket contract around synthetic ticket orders. Each create request carries a tenant API key and an idempotency key. Successful state changes receive monotonic sequence numbers. The inventory dependency supports deterministic healthy, slow, flaky, and unavailable modes.

This creates controlled conditions for testing correctness before discussing scale.

### 2. Put explicit boundaries around the critical path

The create-order path applies:

1. tenant authentication;
2. separate IP, tenant, and API-key token-bucket admission limits;
3. tenant-scoped coordination of concurrent duplicates;
4. a per-attempt dependency timeout;
5. bounded retry with backoff and jitter;
6. a circuit breaker that opens after repeated failures;
7. order persistence and event publication only after the protected operation succeeds.

A failed dependency attempt releases the pending idempotency entry instead of permanently caching an error. A completed duplicate receives the original order. This keeps the idempotency lifecycle aligned with the business outcome.

### 3. Make recovery testable

The service keeps a bounded tenant-scoped replay buffer. A WebSocket consumer reconnects with its last processed sequence and receives retained events after that cursor before joining the live stream.

Fault injection is part of the local demo, not a hidden test mock. The operator can open the control center, make the dependency unavailable, drive traffic, observe the open circuit, restore health, and confirm a recovery probe.

### 4. Deliver operating evidence with the code

The repository includes:

- unit tests for idempotency, rate limiting, retry, breaker, store, and service behavior;
- HTTP and WebSocket integration tests;
- k6 scenarios for healthy traffic, duplicate concurrency, overload shedding, dependency recovery, and reconnect replay;
- low-cardinality Prometheus metrics and a provisioned Grafana dashboard;
- structured JSON request logs, liveness, and readiness endpoints;
- an operator runbook and an architecture decision record;
- a multi-stage, non-root Docker image and a reproducible Compose topology.

## Evidence

| Question | Reproducible evidence | Expected proof |
| --- | --- | --- |
| Can concurrent retries create duplicates? | `make idempotency` | Twenty concurrent responses resolve to one order ID |
| Does dependency failure remain bounded? | `make dependency-recovery` | Requests surface `503`, the circuit opens, and a later probe succeeds after recovery |
| Is overload deliberate and visible? | `make rate-limit` | Requests are either handled or rejected with `429`; rejection counters increase |
| Can a disconnected client recover its gap? | `make websocket-replay` | Retained sequences after the cursor arrive in order without gaps |
| Is the Go implementation concurrency-safe under its tests? | `make test-race` | The suite completes under Go's race detector |
| Can an operator understand the incident? | `make demo`, Grafana, and `docs/RUNBOOK.md` | Failure state, fast rejection, recovery, and the next diagnostic action are visible |

The project does not claim a context-free requests-per-second or latency number. To make a performance run reviewable, I report the commit, hardware, Docker/runtime configuration, k6 scenario parameters, complete output, and dashboard window. Correctness checks are stable; performance results remain tied to the stated environment.

## Design judgment and limits

The lab deliberately uses in-process storage and a synthetic dependency. This keeps the mechanisms understandable and the failure scenarios reproducible, but it does not claim distributed durability. A production implementation would move idempotency and business state into a transactional store, publish through an outbox or durable log, coordinate limits across replicas, secure administrative actions, and attach alerts to agreed SLOs.

Naming those limits is part of the deliverable. Reliability work is not complete when a demo succeeds; stakeholders need to know what is guaranteed, what is not, and which condition triggers the next architecture investment.

## Client relevance

The same approach applies directly to client systems such as:

- payment, booking, checkout, and order APIs that must tolerate retries;
- webhook consumers and third-party integrations that need safe retry policy;
- multi-tenant SaaS backends that need overload isolation;
- WebSocket or event-driven applications that need reconnect reconciliation;
- inherited services that lack failure tests, monitoring, and operating documentation.

For an existing system, I would begin with its real critical paths and incident history, document current behavior, add observability, and build a regression harness before changing policies. I would then stabilize one boundary at a time and leave the team with executable tests, dashboards, and a plain-language runbook—not only a code patch.

## Project integrity

Northstar is a clean-room portfolio project built with synthetic data and public specifications. It contains no source code, data, configuration, metrics, screenshots, or private design material from any employer. See [`PROVENANCE.md`](../PROVENANCE.md) for the complete statement.
