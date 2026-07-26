import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

const baseURL = __ENV.BASE_URL || 'http://localhost:8080';
const limitedRequests = new Counter('rate_limited_requests');
const healthyTenantSuccess = new Rate('healthy_tenant_success');

export const options = {
  scenarios: {
    noisy_tenant: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.REQUEST_RATE || 100),
      timeUnit: '1s',
      duration: '10s',
      preAllocatedVUs: 20,
      maxVUs: 80,
    },
    healthy_tenant: {
      executor: 'constant-arrival-rate',
      exec: 'healthyTenant',
      rate: 2,
      timeUnit: '1s',
      duration: '10s',
      preAllocatedVUs: 2,
      maxVUs: 5,
    },
  },
  thresholds: {
    checks: ['rate==1'],
    rate_limited_requests: ['count>0'],
    healthy_tenant_success: ['rate>0.95'],
  },
};

export function setup() {
  http.post(`${baseURL}/v1/demo/reset`);
}

export default function () {
  const response = http.post(
    `${baseURL}/v1/orders`,
    JSON.stringify({ event_id: 'event-rate-limit-lab', quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-beta-key',
        'Idempotency-Key': `noisy-${__VU}-${__ITER}-${Date.now()}`,
      },
      tags: { scenario: 'rate-limit' },
    },
  );

  check(response, {
    'request is handled or deliberately limited': (r) =>
      r.status === 200 || r.status === 201 || r.status === 429,
  });
  if (response.status === 429) {
    limitedRequests.add(1);
  }
}

export function healthyTenant() {
  const response = http.post(
    `${baseURL}/v1/orders`,
    JSON.stringify({ event_id: 'event-healthy-neighbor', quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-alpha-key',
        'Idempotency-Key': `healthy-${__VU}-${__ITER}-${Date.now()}`,
      },
      tags: { scenario: 'healthy-tenant' },
    },
  );
  const accepted = response.status === 200 || response.status === 201;
  healthyTenantSuccess.add(accepted);
  check(response, { 'healthy tenant remains accepted': () => accepted });
}
