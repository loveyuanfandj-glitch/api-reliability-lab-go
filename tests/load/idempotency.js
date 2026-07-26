import http from 'k6/http';
import { check } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://localhost:8080';
const parallelRequests = Number(__ENV.PARALLEL_REQUESTS || 20);

export const options = {
  scenarios: {
    duplicate_burst: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '30s',
    },
  },
  thresholds: {
    checks: ['rate==1'],
  },
};

export function setup() {
  const reset = http.post(`${baseURL}/v1/demo/reset`);
  check(reset, { 'demo reset accepted': (r) => r.status >= 200 && r.status < 300 });
}

export default function () {
  const idempotencyKey = `duplicate-burst-${Date.now()}`;
  const request = {
    method: 'POST',
    url: `${baseURL}/v1/orders`,
    body: JSON.stringify({ event_id: 'event-idempotency-lab', quantity: 2 }),
    params: {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-alpha-key',
        'Idempotency-Key': idempotencyKey,
      },
      tags: { scenario: 'duplicate-suppression' },
    },
  };

  const responses = http.batch(Array.from({ length: parallelRequests }, () => request));
  const ids = responses.map(orderID).filter(Boolean);

  check(responses, {
    'all duplicate requests succeed': (items) => items.every((r) => r.status === 200 || r.status === 201),
    'all responses contain an order id': () => ids.length === parallelRequests,
    'all responses resolve to one order': () => new Set(ids).size === 1,
  });
}

function orderID(response) {
  try {
    const body = response.json();
    return body.id || (body.order && body.order.id);
  } catch (_) {
    return null;
  }
}
