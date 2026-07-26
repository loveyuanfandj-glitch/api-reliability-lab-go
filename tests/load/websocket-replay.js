import http from 'k6/http';
import ws from 'k6/ws';
import { check } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://localhost:8080';
const streamURL = baseURL.replace(/^http/, 'ws');

export const options = {
  scenarios: {
    reconnect_and_replay: {
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
  createOrder(`stream-seed-${Date.now()}`);
  const initial = receiveEvents(0, 1);
  check(initial, {
    'initial replay returns a sequence': (events) => events.length >= 1 && events[events.length - 1] > 0,
  });
  if (initial.length === 0) return;

  const lastSeen = initial[initial.length - 1];
  for (let index = 0; index < 3; index += 1) {
    createOrder(`stream-gap-${Date.now()}-${index}`);
  }

  const replayed = receiveEvents(lastSeen, 3);
  check(replayed, {
    'reconnect receives retained events': (events) => events.length >= 3,
    'replayed sequences are ordered without gaps': (events) =>
      events.every((sequence, index) => sequence === lastSeen + index + 1),
  });
}

function createOrder(idempotencyKey) {
  const response = http.post(
    `${baseURL}/v1/orders`,
    JSON.stringify({ event_id: 'event-stream-lab', quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': 'tenant-alpha-key',
        'Idempotency-Key': idempotencyKey,
      },
    },
  );
  check(response, { 'stream fixture order accepted': (r) => r.status === 200 || r.status === 201 });
}

function receiveEvents(since, minimum) {
  const sequences = [];
  const response = ws.connect(
    `${streamURL}/v1/stream?since=${since}`,
    { headers: { 'X-API-Key': 'tenant-alpha-key' } },
    (socket) => {
      socket.on('message', (message) => {
        for (const sequence of extractSequences(message)) {
          if (sequence > since) sequences.push(sequence);
        }
        if (sequences.length >= minimum) socket.close();
      });
      socket.setTimeout(() => socket.close(), 5000);
    },
  );
  check(response, { 'WebSocket upgrade succeeds': (r) => r && r.status === 101 });
  return sequences;
}

function extractSequences(message) {
  try {
    const payload = JSON.parse(message);
    const items = Array.isArray(payload) ? payload : payload.events || [payload];
    return items
      .map((item) => Number(item.sequence || (item.event && item.event.sequence)))
      .filter((sequence) => Number.isFinite(sequence));
  } catch (_) {
    return [];
  }
}
