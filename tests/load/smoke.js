import http from 'k6/http';
import { check, sleep } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://localhost:8080';

export const options = {
  vus: 2,
  duration: '10s',
  thresholds: {
    checks: ['rate>0.99'],
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};

export function setup() {
  const reset = http.post(`${baseURL}/v1/demo/reset`);
  check(reset, { 'demo reset accepted': (r) => r.status >= 200 && r.status < 300 });
}

export default function () {
  const suffix = `${__VU}-${__ITER}-${Date.now()}`;
  const response = http.post(
    `${baseURL}/v1/orders`,
    JSON.stringify({ event_id: `event-${__VU}`, quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-alpha-key',
        'Idempotency-Key': `smoke-${suffix}`,
      },
    },
  );

  check(response, {
    'order accepted': (r) => r.status === 200 || r.status === 201,
    'order has an id': (r) => Boolean(orderID(r)),
  });
  sleep(0.2);
}

function orderID(response) {
  try {
    const body = response.json();
    return body.id || (body.order && body.order.id);
  } catch (_) {
    return null;
  }
}
