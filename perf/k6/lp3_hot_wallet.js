// LP-3: 2.000 transfer @ 500/dtk dari SATU wallet bersaldo 100 × Rp10.000 (Demo B skala besar).
// Lulus: TEPAT 100 sukses, sisanya INSUFFICIENT_BALANCE (atau RESOURCE_BUSY), saldo akhir 0, 0 deadlock/5xx.
import http from 'k6/http';
import { Counter } from 'k6/metrics';
import { BASE, json, loginMany, seedEmail, uuid, code } from './lib.js';

const outcomes = new Counter('lp3_outcomes');
const HOT = __ENV.HOT_USER || '0'; // hot-0 … hot-4: sekali pakai per seed

export const options = {
  scenarios: {
    hot: { executor: 'constant-arrival-rate', rate: 500, timeUnit: '1s', duration: '4s', preAllocatedVUs: 500, maxVUs: 1500 },
  },
  thresholds: { 'http_reqs{name:transfer,status:201}': ['count>=0'], 'http_reqs{name:transfer,status:409}': ['count>=0'], 'http_reqs{name:transfer,status:422}': ['count>=0'], 'http_reqs{name:transfer,status:500}': ['count>=0'], 'http_reqs{name:transfer,status:503}': ['count>=0'], 'http_req_duration{name:transfer}': ['p(95)<100000'] },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  const [hot] = loginMany([`hot-${HOT}@seed.ledgerpay.test`]);
  return { hot };
}

export default function (data) {
  const r = http.post(`${BASE}/transfers`, JSON.stringify({ recipient_email: seedEmail(1), amount: '1000000', currency: 'IDR' }),
    Object.assign(json(data.hot, { 'Idempotency-Key': uuid() }), { tags: { name: 'transfer' }, responseCallback: http.expectedStatuses(201, 422, 409) }));
  outcomes.add(1, { outcome: r.status === 201 ? '201' : `${r.status} ${code(r)}` });
}
