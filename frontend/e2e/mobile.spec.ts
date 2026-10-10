import { execFileSync } from 'node:child_process'
import { devices, expect, test } from '@playwright/test'

// Tampilan mobile (≤768px): bottom tab bar, alur transfer 3 langkah (isi → konfirmasi → bukti),
// dan keluar lewat halaman Akun. Saldo awal lewat CLI, sama seperti checkout.spec.ts.
const compose = (process.env.COMPOSE ?? 'docker compose -f ../deploy/compose.yml').split(' ')
const cli = (...args: string[]) =>
  execFileSync(compose[0], [...compose.slice(1), 'exec', '-T', 'api', '/app/ledgerpay', ...args], { encoding: 'utf8' }).trim()

test.use({ ...devices['Pixel 7'] })

test('mobile: tab bar → transfer with confirmation step → account → logout', async ({ page, request }) => {
  const run = Date.now()
  const password = 'correct horse battery'
  const rina = `rina-m-${run}@example.com`
  const budi = `budi-m-${run}@example.com`
  for (const [email, name] of [[rina, 'Rina'], [budi, 'Budi M.']]) {
    expect((await request.post('/api/v1/auth/register', { data: { email, password, display_name: name } })).status()).toBe(201)
  }
  cli('demo-credit', rina, '5000000') // Rp50.000

  await page.goto('/login')
  await page.getByLabel('Email').fill(rina)
  await page.getByLabel('Kata sandi').fill(password)
  await page.getByRole('button', { name: 'Masuk' }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp50.000')

  // Desktop sidebar tersembunyi; navigasi lewat tab bar bawah.
  await expect(page.getByRole('navigation', { name: 'Menu utama' })).toBeHidden()
  const tabs = page.getByRole('navigation', { name: 'Navigasi bawah' })
  await tabs.getByRole('link', { name: 'Transfer' }).click()

  await page.getByLabel('Email penerima').fill(budi)
  await page.getByLabel('Nominal (Rp)').fill('20000')
  await page.getByRole('button', { name: 'Lanjutkan' }).click()
  await expect(page.getByText('Kamu akan mengirim')).toBeVisible()
  await page.getByRole('button', { name: 'Kirim sekarang' }).click()
  await expect(page.getByTestId('transfer-done')).toHaveText('Terkirim Rp20.000 ke Budi M.')

  await page.getByRole('link', { name: 'Ke Beranda' }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp30.000')

  await page.getByRole('navigation', { name: 'Navigasi bawah' }).getByRole('link', { name: 'Akun' }).click()
  await page.getByTestId('account-logout').click()
  await expect(page).toHaveURL(/\/login/)
})
