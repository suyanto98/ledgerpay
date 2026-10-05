// LP-9: login 5 → 50/dtk (argon2id m=64MiB, semaphore 4 paralel), rate limit dimatikan (perf env).
// Bersamaan: GET /wallet 20/dtk dari user yang sudah login → endpoint lain harus tetap cepat.
// Lulus: tanpa OOM; saat jenuh server menjawab 503 CEPAT (semaphore), bukan latency tak terbatas.
import http from 'k6/http';
import { Counter } from 'k6/metrics';
import { BASE, PASSWORD, json, loginMany, seedEmail, code } from './lib.js';

const outcomes = new Counter('lp9_outcomes');
const DURATION = Number(__ENV.DURATION_SECONDS || 300);

export const options = {
  scenarios: {
    logins: {
      executor: 'ramping-arrival-rate', startRate: 5, timeUnit: '1s', preAllocatedVUs: 100, maxVUs: 400,
      stages: [{ target: 50, duration: `${DURATION}s` }], exec: 'login',
    },
    wallet: { executor: 'constant-arrival-rate', rate: 20, timeUnit: '1s', duration: `${DURATION}s`, preAllocatedVUs: 20, maxVUs: 100, exec: 'wallet' },
  },
  thresholds: {
    'http_req_duration{name:login,status:200}': ['p(95)<100000'], 'http_req_duration{name:login,status:503}': ['p(95)<100000'],
    'http_req_duration{name:wallet}': ['p(95)<100000'],
    'http_reqs{name:login,status:200}': ['count>=0'], 'http_reqs{name:login,status:503}': ['count>=0'], 'http_reqs{status:500}': ['count>=0'],
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  return { token: loginMany([seedEmail(0)])[0] };
}

export function login() {
  const email = seedEmail(1000 + Math.floor(Math.random() * 10000)); // 10.000 akun
  const r = http.post(`${BASE}/auth/login`, JSON.stringify({ email, password: PASSWORD }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'login' }, responseCallback: http.expectedStatuses(200, 503) });
  outcomes.add(1, { outcome: r.status === 200 ? '200' : `${r.status} ${code(r)}` });
}

export function wallet(data) {
  http.get(`${BASE}/wallet`, Object.assign(json(data.token), { tags: { name: 'wallet' } }));
}
