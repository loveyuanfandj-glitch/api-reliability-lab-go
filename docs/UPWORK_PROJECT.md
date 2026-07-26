# Upwork Project Catalog copy

## Title

Production-Ready Go Order API: PostgreSQL, Redis & Reliable Webhooks

## Role

Backend & Reliability Engineer

## Project URL

https://github.com/loveyuanfandj-glitch/api-reliability-lab-go

## Description

I built a clean-room Go backend showing how I take an order, booking, payment, or third-party integration from demo code to an operable product. The deployable runtime uses PostgreSQL for durable tenant-scoped orders, Redis for cross-instance idempotency coordination, and a transactional outbox so an order and its notification cannot drift apart. Background workers claim jobs safely across replicas, sign exact webhook payloads, retry failures with bounded backoff, recover expired leases, retain dead letters, and expose an authenticated replay workflow. Stripe- and Shopify-style sandbox adapters verify real HMAC formats without accessing live accounts.

The same repository includes a visual failure lab for timeouts, retries, circuit breaking, rate limits, WebSocket gap recovery, Prometheus/Grafana signals, and runbooks. The product path is verified against real PostgreSQL and Redis in CI, including 20 concurrent duplicate requests, Redis-unavailable fallback, tenant isolation, signature validation, and a receiver that fails twice before succeeding. All code, data, credentials, and screenshots are independently created and synthetic.

## Skills

- Go
- Backend Development
- RESTful API
- WebSocket
- API Integration
- Distributed Systems
- PostgreSQL
- Redis
- Webhook
- Stripe API
- Shopify API
- Software Architecture
- Docker
- Prometheus
- Grafana
- k6
- Performance Testing

## Portfolio captions

1. **Live reliability control center** — Create traffic, prove idempotency, inject dependency faults, and watch the service recover.
2. **Failure containment in action** — An unavailable dependency opens the circuit, bounds retries, and turns cascading delay into observable fast failure.
3. **Executable acceptance scenarios** — k6 verifies duplicate suppression, noisy-tenant shedding, dependency recovery, and ordered WebSocket replay.
4. **Operations delivered with the code** — Prometheus, Grafana, structured logs, health checks, architecture decisions, and a plain-language runbook.
5. **Product path, not only a demo** — Durable orders, transactional outbox, signed retrying webhooks, dead-letter recovery, and real dependency integration tests.

## Upload order

1. `artifacts/screenshots/upwork-cover.png` — 4:3 cover image showing the live control center.
2. `artifacts/screenshots/failure-mode.png` — circuit open, dependency unavailable, and requests failing fast.
3. `artifacts/screenshots/recovery-mode.png` — circuit closed after three successful recovery probes.
4. `artifacts/video/northstar-reliability-demo.mp4` — 7-second healthy → incident → recovery walkthrough.
