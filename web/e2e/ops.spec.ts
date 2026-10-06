import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

// Demo D lewat ops console: payout macet UNKNOWN (bank mengirim, respons hilang, status query bank mati)
// → recon hari ini → STATUS_MISMATCH → ops menyelesaikan dengan bukti → payout SUCCEEDED.
const compose = (process.env.COMPOSE ?? 'docker compose -f ../deploy/compose.yml').split(' ')
const cli = (...args: string[]) =>
  execFileSync(compose[0], [...compose.slice(1), 'exec', '-T', 'api', '/app/ledgerpay', ...args], { encoding: 'utf8' }).trim()
const MOCK = `http://localhost:${process.env.LP_MOCK_PORT ?? '9091'}`

test('ops console: reconcile a stuck payout and resolve it with evidence', async ({ page, request }) => {
  test.setTimeout(120_000) // bank menahan respons ±10 dtk sebelum payout menjadi UNKNOWN
  const run = Date.now()
  const password = 'correct horse battery'
  const ops = `ops-ui-${run}@example.com`
  const rina = `rina-ops-${run}@example.com`
  for (const [email, name] of [[ops, 'Ops'], [rina, 'Rina']]) {
    expect((await request.post('/api/v1/auth/register', { data: { email, password, display_name: name } })).status()).toBe(201)
  }
  cli('grant-ops', ops)
  cli('demo-credit', rina, '10000000')
  await request.put(`${MOCK}/admin/scenario`, { data: { charges: 'ok', payout_status_query: 'disabled' } })
  try {
    const login = await (await request.post('/api/v1/auth/login', { data: { email: rina, password } })).json()
    const po = await (await request.post('/api/v1/payouts', {
      headers: { Authorization: `Bearer ${login.access_token}`, 'Idempotency-Key': `po-${run}` },
      data: { amount: '2000000', currency: 'IDR', destination: { bank_code: 'MOCKBANK', account_number: '9900000408', holder_name: 'Rina' } },
    })).json()
    await expect.poll(async () => (await (await request.get(`/api/v1/payouts/${po.id}`, {
      headers: { Authorization: `Bearer ${login.access_token}` } })).json()).status, { timeout: 30_000 }).toBe('UNKNOWN')

    await page.goto('/login')
    await page.getByLabel('Email').fill(ops)
    await page.getByLabel('Password').fill(password)
    await page.getByRole('button', { name: 'Masuk' }).click()
    await page.getByRole('link', { name: 'Ops', exact: true }).click()
    await page.getByRole('button', { name: 'Jalankan recon' }).click()
    await expect(page.getByTestId('recon-run')).toContainText('COMPLETED')
    const row = page.getByTestId('recon-item').filter({ hasText: po.id.slice(0, 8) })
    await expect(row).toContainText('STATUS_MISMATCH')
    await row.getByRole('button', { name: 'APPLY_PAYOUT_SETTLE' }).click()
    await page.getByLabel(/Referensi bukti/).fill(`settlement#${po.id.slice(0, 8)}`)
    await page.getByLabel(/Alasan/).fill('Settlement bank menunjukkan SUCCEEDED')
    await page.getByRole('button', { name: 'Selesaikan' }).click()
    await expect(page.getByTestId('ops-done')).toBeVisible()
    await expect(row).toHaveCount(0)

    const after = await (await request.get(`/api/v1/payouts/${po.id}`, { headers: { Authorization: `Bearer ${login.access_token}` } })).json()
    expect(after.status).toBe('SUCCEEDED')
    expect((await (await request.get(`${MOCK}/admin/transfers?idempotency_key=${po.id}`)).json()).transfers_executed).toBe(1)
  } finally {
    await request.put(`${MOCK}/admin/scenario`, { data: { charges: 'ok' } })
  }
})
