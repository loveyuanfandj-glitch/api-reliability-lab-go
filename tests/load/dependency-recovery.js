import http from 'k6/http';
import { check, sleep } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://localhost:8080';

export const options = {
  scenarios: {
    open_then_recover: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '60s',
    },
  },
  thresholds: {
    checks: ['rate>0.95'],
  },
};

export function setup() {
  const reset = http.post(`${baseURL}/v1/demo/reset`);
  check(reset, { 'demo reset accepted': (r) => r.status >= 200 && r.status < 300 });
}

export default function () {
  const unavailable = setFault('unavailable');
  check(unavailable, { 'dependency switched to unavailable': success });

  const failures = [];
  for (let index = 0; index < 12; index += 1) {
    failures.push(createOrder(`outage-${Date.now()}-${index}`));
  }
  check(failures, {
    'outage is surfaced to callers': (items) => items.some((r) => r.status >= 500),
    'circuit eventually fails fast': (items) => items.slice(3).some((r) => r.status === 503 && r.timings.duration < 50),
  });

  const openSnapshot = http.get(`${baseURL}/v1/demo/snapshot`);
  check(openSnapshot, {
    'snapshot reports an open circuit': (r) => r.status === 200 && r.json().dependency.circuit_state === 'open',
  });

  const healthy = setFault('healthy');
  check(healthy, { 'dependency switched to healthy': success });

  // The default demo breaker cooldown is short enough for an interactive run.
  sleep(Number(__ENV.RECOVERY_WAIT_SECONDS || 6));
  const recovered = createOrder(`recovered-${Date.now()}`);
  check(recovered, { 'order succeeds after recovery': (r) => r.status === 200 || r.status === 201 });
  const recoveredSnapshot = http.get(`${baseURL}/v1/demo/snapshot`);
  check(recoveredSnapshot, {
    'snapshot reports a closed circuit after probe': (r) => r.status === 200 && r.json().dependency.circuit_state === 'closed',
  });
}

function setFault(mode) {
  return http.post(
    `${baseURL}/v1/demo/fault`,
    JSON.stringify({ mode }),
    { headers: { 'Content-Type': 'application/json' } },
  );
}

function createOrder(idempotencyKey) {
  return http.post(
    `${baseURL}/v1/orders`,
    JSON.stringify({ event_id: 'event-recovery-lab', quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-alpha-key',
        'Idempotency-Key': idempotencyKey,
      },
      tags: { scenario: 'dependency-recovery' },
    },
  );
}

function success(response) {
  return response.status >= 200 && response.status < 300;
}
