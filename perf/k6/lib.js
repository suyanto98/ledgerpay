// Helper bersama skrip k6 LedgerPay (docs/design/23-performance-plan.md §5).
import http from 'k6/http';
import { check } from 'k6';

export const BASE = __ENV.BASE_URL || 'http://localhost:18080/api/v1';
export const PASSWORD = 'correct horse battery'; // app.SeedPassword
export const seedEmail = (i) => `seed-${i}@seed.ledgerpay.test`;

export function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

export const json = (token, extra = {}) => ({
  headers: Object.assign({ 'Content-Type': 'application/json', Authorization: `Bearer ${token}` }, extra),
});

// loginMany: login paralel dalam batch kecil. Hashing argon2 di server dibatasi semaphore (4 paralel,
// antre ≤ 2 dtk): batch besar = 503, bukan lebih cepat.
export function loginMany(emails, batch = 6) {
  const tokens = [];
  for (let i = 0; i < emails.length; i += batch) {
    const reqs = emails.slice(i, i + batch).map((email) => ({
      method: 'POST', url: `${BASE}/auth/login`,
      body: JSON.stringify({ email, password: PASSWORD }), params: { headers: { 'Content-Type': 'application/json' } },
    }));
    for (const r of http.batch(reqs)) {
      check(r, { 'login 200': (x) => x.status === 200 }) || console.error(`login failed: ${r.status} ${r.body}`);
      tokens.push(r.json('access_token'));
    }
  }
  return tokens;
}

export const code = (r) => {
  try { return r.json('code') || String(r.status); } catch (_) { return String(r.status); }
};
