// LP-2: 10.000 transfer, open model ramping 10 → 200/dtk antara 1.000 user aktif (saldo awal Rp500.000).
// Lulus: 0 respons 5xx; invariant dicek DI LUAR k6 oleh perf/run.sh (Σ saldo user tetap, journal = 201).
import http from 'k6/http';
import { Counter } from 'k6/metrics';
import { BASE, json, loginMany, seedEmail, uuid, code } from './lib.js';

const outcomes = new Counter('lp2_outcomes');
const USERS = Number(__ENV.USERS || 1000);
// RATE_MAX=200 (default) = skenario desain (≈ 10.000 iterasi). Nilai lebih tinggi = eksplorasi titik jenuh.
const MAX = Number(__ENV.RATE_MAX || 200);

export const options = {
  setupTimeout: '10m',
  scenarios: {
    transfers: {
      executor: 'ramping-arrival-rate', startRate: 10, timeUnit: '1s', preAllocatedVUs: 200, maxVUs: 2000,
      stages: [ // RATE_MAX=200: Σ ≈ 10.000 iterasi
        { target: MAX / 4, duration: '20s' }, { target: MAX / 2, duration: '20s' }, { target: (3 * MAX) / 4, duration: '20s' },
        { target: MAX, duration: '20s' }, { target: MAX, duration: '10s' },
      ],
    },
  },
  thresholds: { 'http_reqs{name:transfer,status:201}': ['count>=0'], 'http_reqs{name:transfer,status:409}': ['count>=0'], 'http_reqs{name:transfer,status:422}': ['count>=0'], 'http_reqs{name:transfer,status:500}': ['count>=0'], 'http_reqs{name:transfer,status:503}': ['count>=0'], 'http_req_failed{expected_response:true}': ['rate<1'], 'http_req_duration{name:transfer}': ['p(95)<100000'] },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  return { tokens: loginMany([...Array(USERS).keys()].map(seedEmail)) };
}

export default function (data) {
  const from = Math.floor(Math.random() * USERS);
  let to = Math.floor(Math.random() * USERS);
  if (to === from) to = (to + 1) % USERS;
  const r = http.post(`${BASE}/transfers`, JSON.stringify({ recipient_email: seedEmail(to), amount: '100000', currency: 'IDR' }),
    Object.assign(json(data.tokens[from], { 'Idempotency-Key': uuid() }), { tags: { name: 'transfer' }, responseCallback: http.expectedStatuses(201, 422, 409) }));
  outcomes.add(1, { outcome: r.status === 201 ? '201' : `${r.status} ${code(r)}` });
}
