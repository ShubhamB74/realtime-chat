// Phase 3 baseline: authenticated WebSocket clients exchange messages inside group conversations.
//
// Measures (client side, so k6 and the backend should run on the same host for the delivery
// latency to be meaningful: it compares timestamps from one clock):
//   accept_latency_ms    message.send -> message.accepted   (NFR-LAT-1, target p95 < 150 ms)
//   delivery_latency_ms  send timestamp -> message.received at another member (NFR-LAT-2, p95 < 300 ms)
//
// Prerequisites: `make up && make migrate-up && make run`, then `make load-seed`.
// Run: `make load-baseline`, or e.g. `k6 run -e VUS=300 -e DURATION=5m tests/load/k6/chat.js`.
//
// All numbers in the thresholds are hypothetical targets from docs/non-functional-requirements.md.
import ws from 'k6/ws';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { Counter, Rate, Trend } from 'k6/metrics';

const users = new SharedArray('users', () => JSON.parse(open('../data/seed.json')).users);

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const WS_URL = BASE_URL.replace(/^http/, 'ws') + '/v1/ws';
const SEND_INTERVAL_MS = Number(__ENV.SEND_INTERVAL_MS || 2000); // per VU
const MSG_BYTES = Number(__ENV.MSG_BYTES || 100);
const SESSION_SECONDS = Number(__ENV.SESSION_SECONDS || 60);

const acceptLatency = new Trend('accept_latency_ms', true);
const deliveryLatency = new Trend('delivery_latency_ms', true);
const connectFailed = new Rate('ws_connect_failed');
const sent = new Counter('messages_sent');
const accepted = new Counter('messages_accepted');
const received = new Counter('messages_received');
const serverErrors = new Counter('server_error_events');
const abnormalCloses = new Counter('abnormal_closes');

export const options = {
  scenarios: {
    chat: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 100),
      duration: __ENV.DURATION || '2m',
    },
  },
  thresholds: {
    accept_latency_ms: ['p(95)<150', 'p(99)<300'],
    delivery_latency_ms: ['p(95)<300', 'p(99)<800'],
    ws_connect_failed: ['rate<0.01'],
    server_error_events: ['count==0'],
  },
};

export function setup() {
  if (users.length === 0) {
    throw new Error('tests/load/data/seed.json has no users: run `make load-seed` first');
  }
}

export default function () {
  const me = users[(__VU - 1) % users.length];
  const pending = {}; // request_id -> send time
  let counter = 0;
  let closedByUs = false;

  const res = ws.connect(WS_URL, { headers: { Authorization: 'Bearer ' + me.access_token } }, (socket) => {
    socket.on('open', () => {
      // Jitter the first send so VUs do not fire in lockstep.
      socket.setTimeout(() => {
        socket.setInterval(() => {
          counter += 1;
          const rid = `k6-${__VU}-${__ITER}-${counter}`;
          const sentAt = Date.now();
          pending[rid] = sentAt;
          socket.send(JSON.stringify({
            v: 1,
            type: 'message.send',
            request_id: rid,
            payload: {
              conversation_id: me.conversation_id,
              idempotency_key: `${rid}-${Math.random().toString(36).slice(2, 8)}`,
              body: `k6:${sentAt}:` + 'x'.repeat(MSG_BYTES),
            },
          }));
          sent.add(1);
        }, SEND_INTERVAL_MS);
      }, Math.random() * SEND_INTERVAL_MS);
    });

    socket.on('message', (data) => {
      const ev = JSON.parse(data);
      switch (ev.type) {
        case 'message.accepted': {
          const t0 = pending[ev.request_id];
          if (t0 !== undefined) {
            acceptLatency.add(Date.now() - t0);
            accepted.add(1);
            delete pending[ev.request_id];
          }
          break;
        }
        case 'message.received': {
          const body = ev.payload && ev.payload.body;
          if (body && body.startsWith('k6:')) {
            deliveryLatency.add(Date.now() - Number(body.split(':')[1]));
            received.add(1);
          }
          break;
        }
        case 'error':
          serverErrors.add(1);
          break;
        default:
      }
    });

    socket.on('close', (code) => {
      if (!closedByUs && code !== 1000) abnormalCloses.add(1);
    });
    socket.on('error', () => serverErrors.add(1));

    socket.setTimeout(() => {
      closedByUs = true;
      socket.close(1000);
    }, SESSION_SECONDS * 1000);
  });

  const ok = !!res && res.status === 101;
  connectFailed.add(!ok);
  check(res, { 'upgraded (HTTP 101)': (r) => !!r && r.status === 101 });
  sleep(1);
}
