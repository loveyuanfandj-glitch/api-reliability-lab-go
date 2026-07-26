# Upwork Project Catalog copy

## Title

Go API Reliability Lab: Idempotency, Recovery & Observability

## Role

Backend & Reliability Engineer

## Project URL

https://github.com/loveyuanfandj-glitch/api-reliability-lab-go

## Description

I built a clean-room Go service showing how to stabilize multi-tenant order, booking, payment, and webhook-style APIs. It coordinates concurrent duplicates, isolates noisy tenants, bounds dependency failures with timeout, retry, and circuit-breaker policies, and replays retained WebSocket events after reconnect. The project includes REST and WebSocket APIs, k6 acceptance scenarios, Prometheus/Grafana observability, structured logs, Docker Compose, CI tests, and an operator runbook. All data and fault scenarios are synthetic and reproducible.

## Skills

- Go
- Backend Development
- RESTful API
- WebSocket
- API Integration
- Distributed Systems
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

## Upload order

1. `artifacts/screenshots/upwork-cover.png` — 4:3 cover image showing the live control center.
2. `artifacts/screenshots/failure-mode.png` — circuit open, dependency unavailable, and requests failing fast.
3. `artifacts/screenshots/recovery-mode.png` — circuit closed after three successful recovery probes.
4. `artifacts/video/northstar-reliability-demo.mp4` — 7-second healthy → incident → recovery walkthrough.
