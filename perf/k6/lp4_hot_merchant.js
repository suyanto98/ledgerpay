// LP-4 / E-1: pembayaran ke SATU merchant (hot row wallet merchant), step 25 → 50 → 100 → 200 → 400/dtk.
// Kontrol: MERCHANT_IDS berisi 100 merchant → laju total sama tanpa hot row (§6 E-1).
// Satu pembayaran = create intent (API key merchant) + confirm (payer). Payer: 1.000 user aktif
// (deviasi dari 5.000 payer di desain: login 5.000 user = 5.000 hash argon2 di setup).
import http from 'k6/http';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';
import { BASE, json, loginMany, seedEmail, uuid, code } from './lib.js';

const outcomes = new Counter('lp4_outcomes');
const MERCHANTS = (__ENV.MERCHANT_IDS || '').split(',').filter(Boolean);
const PAYERS = Number(__ENV.PAYERS || 1000);
const STAGE = Number(__ENV.STAGE_SECONDS || 120);
const RATES = [25, 50, 100, 200, 400];

const stages = [];
for (const r of RATES) stages.push({ target: r, duration: '5s' }, { target: r, duration: `${STAGE - 5}s` });
const thresholds = { 'http_reqs{status:500}': ['count>=0'], 'http_reqs{name:confirm,status:200}': ['count>=0'], 'http_reqs{name:confirm,status:409}': ['count>=0'], 'http_reqs{name:confirm,status:422}': ['count>=0'], 'http_reqs{name:create_intent,status:201}': ['count>=0'] };
for (let i = 0; i < RATES.length; i++) {
  thresholds[`http_req_duration{name:confirm,stage:${i}}`] = ['p(95)<100000']; // memaksa submetric per tahap
  thresholds[`http_reqs{name:confirm,stage:${i}}`] = ['count>=0'];
  thresholds[`http_reqs{name:confirm,stage:${i},status:200}`] = ['count>=0'];
}

export const options = {
  setupTimeout: '15m',
  scenarios: {
    payments: { executor: 'ramping-arrival-rate', startRate: 25, timeUnit: '1s', preAllocatedVUs: 400, maxVUs: 2000, stages },
  },
  thresholds,
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  if (MERCHANTS.length === 0) throw new Error('MERCHANT_IDS required');
  const [ops] = loginMany(['seed-ops@seed.ledgerpay.test']);
  const issued = MERCHANTS.map((m) => {
    const r = http.post(`${BASE}/ops/merchants/${m}/api-keys`, JSON.stringify({ label: 'k6' }), json(ops, { 'Idempotency-Key': uuid() }));
    if (r.status !== 201) throw new Error(`issue key for ${m}: ${r.status} ${r.body}`);
    return { id: r.json('id'), key: r.json('api_key') };
  });
  return { ops, keyIDs: issued.map((k) => k.id), keys: issued.map((k) => k.key), payers: loginMany([...Array(PAYERS).keys()].map(seedEmail)) };
}

// Merchant maksimal punya 2 key aktif (slot, C-42): key run ini dicabut supaya run berikutnya bisa jalan.
export function teardown(data) {
  for (const id of data.keyIDs) {
    http.post(`${BASE}/ops/api-keys/${id}/revoke`, JSON.stringify({ reason: 'k6 load test finished' }), json(data.ops));
  }
}

export default function (data) {
  // Waktu sejak SKENARIO mulai (bukan sejak test mulai: setup login ±1 menit akan menggeser tahap).
  const elapsed = (Date.now() - exec.scenario.startTime) / 1000;
  const stage = String(Math.min(Math.floor(elapsed / STAGE), RATES.length - 1));
  const key = data.keys[Math.floor(Math.random() * data.keys.length)];
  const order = `k6-${uuid()}`;
  const pi = http.post(`${BASE}/merchant/payment-intents`, JSON.stringify({ merchant_order_id: order, amount: '100000', currency: 'IDR' }),
    Object.assign(json(key, { 'Idempotency-Key': uuid() }), { tags: { name: 'create_intent', stage } }));
  if (pi.status !== 201) { outcomes.add(1, { outcome: `intent ${pi.status} ${code(pi)}`, stage }); return; }
  const payer = data.payers[Math.floor(Math.random() * data.payers.length)];
  const r = http.post(`${BASE}/payment-intents/${pi.json('id')}/confirm`, '{}',
    Object.assign(json(payer, { 'Idempotency-Key': uuid() }), { tags: { name: 'confirm', stage }, responseCallback: http.expectedStatuses(200, 422, 409) }));
  outcomes.add(1, { outcome: r.status === 200 ? '200' : `${r.status} ${code(r)}`, stage });
}
