import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

// Checkout merchant (LF-14) + refund (LF-16) lewat UI sungguhan. Merchant, role OPS, dan saldo awal
// hanya bisa dibuat lewat CLI (sengaja: tidak ada endpoint untuk menaikkan hak), jadi test memanggil
// binary di container api, sama seperti `make demo-payment`.
const compose = (process.env.COMPOSE ?? 'docker compose -f ../deploy/compose.yml').split(' ')
function cli(...args: string[]): string {
  return execFileSync(compose[0], [...compose.slice(1), 'exec', '-T', 'api', '/app/ledgerpay', ...args], { encoding: 'utf8' }).trim()
}

test('merchant link → login → pay from wallet → merchant refunds → balance back', async ({ page, request }) => {
  const run = Date.now()
  const password = 'correct horse battery'
  const ops = `ops-e2e-${run}@example.com`
  const rina = `rina-co-${run}@example.com`
  for (const [email, name] of [[ops, 'Ops'], [rina, 'Rina']]) {
    expect((await request.post('/api/v1/auth/register', { data: { email, password, display_name: name } })).status()).toBe(201)
  }
  cli('grant-ops', ops)
  cli('demo-credit', rina, '5000000') // Rp50.000
  const merchantId = cli('merchant-create', `Kopi Budi ${run}`)
  const opsLogin = await (await request.post('/api/v1/auth/login', { data: { email: ops, password } })).json()
  const key = await (await request.post(`/api/v1/ops/merchants/${merchantId}/api-keys`, {
    headers: { Authorization: `Bearer ${opsLogin.access_token}`, 'Idempotency-Key': `k-${run}` }, data: { label: 'e2e' },
  })).json()
  const merchant = { Authorization: `Bearer ${key.api_key}` }
  const intent = await (await request.post('/api/v1/merchant/payment-intents', {
    headers: { ...merchant, 'Idempotency-Key': `pi-${run}` },
    data: { merchant_order_id: `ORD-${run}`, amount: '3000000', currency: 'IDR', description: 'Kopi susu x3' },
  })).json()
  const checkoutPath = new URL(intent.checkout_url).pathname
  expect(checkoutPath).toBe(`/checkout/${intent.id}`)

  // Link checkout dibuka tanpa sesi → login → kembali ke checkout (query ?next=).
  await page.goto(checkoutPath)
  await expect(page).toHaveURL(/\/login\?next=/)
  await page.getByLabel('Email').fill(rina)
  await page.getByLabel('Kata sandi').fill(password)
  await page.getByRole('button', { name: 'Masuk' }).click()
  await expect(page).toHaveURL(new RegExp(checkoutPath))
  await expect(page.getByRole('heading', { name: `Bayar ke Kopi Budi ${run}` })).toBeVisible()
  await expect(page.getByTestId('checkout-amount')).toHaveText('Rp30.000')
  await page.getByRole('button', { name: 'Bayar Rp30.000' }).click()
  await expect(page.getByTestId('checkout-status')).toHaveText('Pembayaran berhasil')

  await page.getByRole('link', { name: 'Kembali ke Beranda' }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp20.000')

  // Merchant me-refund sebagian lewat API; payer melihat saldo kembali dan baris Refund di riwayat.
  const refund = await request.post(`/api/v1/merchant/payment-intents/${intent.id}/refunds`, {
    headers: { ...merchant, 'Idempotency-Key': `rf-${run}` }, data: { amount: '1000000', reason: 'Satu kopi habis' },
  })
  expect(refund.status()).toBe(201)
  await page.reload()
  await expect(page.getByTestId('balance')).toHaveText('Rp30.000')
  await expect(page.getByRole('cell', { name: 'Refund' })).toBeVisible()
  await page.goto(checkoutPath)
  await expect(page.getByTestId('checkout-status')).toHaveText('Pembayaran berhasil (sebagian dikembalikan)')
})
