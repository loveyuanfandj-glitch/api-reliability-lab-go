# Operator runbook

This runbook operates the synthetic Northstar demo. It does not contain real
credentials, customer data, or employer-specific procedures.

## Quick start

Prerequisites: Docker with Compose v2, `curl`, and optionally `jq`.

```bash
make up
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/readyz
```

Open the local interfaces:

- API snapshot: <http://localhost:8080/v1/demo/snapshot>
- Grafana: <http://localhost:3000/d/northstar-reliability>
- Prometheus targets: <http://localhost:9090/targets>

Grafana is intentionally anonymous and read-only in the local lab. Do not use
that authentication configuration on an internet-accessible deployment.

## Acceptance scenarios

Run each scenario against the Compose network:

```bash
make smoke
make idempotency
make dependency-recovery
make rate-limit
make websocket-replay
```

Or run the narrated demo and keep Grafana visible alongside the terminal:

```bash
make demo
```

Expected evidence:

- duplicate suppression: all 20 responses carry one order ID;
- dependency outage: bounded failures appear, then the circuit opens;
- recovery: after the cooldown, a probe succeeds and closes the circuit;
- rate limiting: the noisy tenant receives `429` without crashing the API;
- metrics: Grafana reflects counters and state within one or two scrape cycles.

## Initial triage

Use this order so that a monitoring failure is not confused with an API
failure.

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -s http://localhost:8080/v1/demo/snapshot | jq .
curl -s http://localhost:8080/metrics | grep '^northstar_'
docker compose ps
docker compose logs --since=10m api
curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | {health, lastError}'
```

Interpretation:

- `/healthz` failing: process or HTTP listener problem;
- `/healthz` healthy and `/readyz` failing: dependency or startup readiness
  problem; do not add traffic;
- API healthy but Prometheus target down: inspect the Compose network and
  Prometheus configuration;
- metrics present but dashboard empty: check the Grafana datasource and set the
  time range to the last 15 minutes.

## Playbook: elevated dependency failures

Symptoms include increasing dependency error outcomes, elevated order `5xx`,
and an open circuit.

1. Confirm the synthetic dependency mode in `/v1/demo/snapshot`.
2. Confirm whether calls fail slowly or fail fast. An open circuit should fail
   fast and prevent retry amplification.
3. Stop load generation before changing the dependency.
4. In the lab, restore healthy mode:

   ```bash
   curl --fail -X POST http://localhost:8080/v1/demo/fault \
     -H 'Content-Type: application/json' \
     -d '{"mode":"healthy"}'
   ```

5. Wait for the breaker cooldown, then submit one uniquely keyed probe.
6. Confirm the circuit returns to closed and dependency success increments.

Do not repeatedly restart the API to clear an open breaker. That discards
diagnostic state and can synchronize a new retry storm.

## Playbook: elevated rate limiting

1. Verify the response is `429`, not a dependency `5xx`.
2. Identify whether tenant, key, or IP admission is responsible from structured
   logs and bounded metrics.
3. Confirm healthy tenants can still create orders.
4. Reduce the synthetic request rate or stop the k6 scenario.
5. Change a limit only after establishing that legitimate baseline traffic has
   outgrown it; do not mask abusive retry behavior by increasing every limit.

## Playbook: duplicate or conflicting requests

1. Confirm clients send a stable `Idempotency-Key` for retries of one logical
   operation.
2. Scope investigation by tenant plus key; a key may safely repeat across
   different tenants.
3. Compare returned order IDs. Retried successful requests must return the
   original ID.
4. If the same key is reused with a different payload, treat the response as a
   client contract violation rather than creating a second order.
5. Capture only synthetic IDs in portfolio evidence. Never publish API keys or
   request bodies from a real service.

## Playbook: WebSocket client reports missing events

1. Record the client's last successfully processed sequence.
2. Reconnect to `/v1/stream?since=<sequence>` using the same tenant identity.
3. Verify replayed sequences are strictly increasing and tenant-scoped.
4. If `since` predates the bounded replay window, perform a REST snapshot/full
   reconciliation, then resume streaming from the current sequence.
5. Investigate repeated disconnects separately; replay is recovery, not a
   substitute for a stable connection.

## Reset and shutdown

Reset only the synthetic in-memory application state:

```bash
curl --fail -X POST http://localhost:8080/v1/demo/reset
```

Stop containers while preserving Prometheus and Grafana volumes:

```bash
make down
```

To deliberately remove local monitoring history, run
`docker compose down --volumes`. This is destructive and is not part of the
normal workflow.
