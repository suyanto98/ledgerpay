import { expect, test } from '@playwright/test'

// Alur uang utama lewat UI sungguhan: daftar → top-up lewat halaman checkout provider (mock) →
// kembali ke LedgerPay → saldo bertambah SEKALI setelah webhook diproses worker → transfer →
// keluar → masuk lagi → saldo tetap benar. Juga menguji pemulihan sesi dari cookie refresh
// (redirect dari provider memuat ulang SPA; access token yang hanya di memori ikut hilang).
test('register → top-up via checkout → transfer → payout → login again', async ({ page, request }) => {
  const run = Date.now()
  const rina = `rina-e2e-${run}@example.com`
  const andi = `andi-e2e-${run}@example.com`
  const password = 'correct horse battery'
  const reg = await request.post('/api/v1/auth/register', { data: { email: andi, password, display_name: 'Andi S.' } })
  expect(reg.status()).toBe(201)

  await page.goto('/register')
  await page.getByLabel('Nama').fill('Rina')
  await page.getByLabel('Email').fill(rina)
  await page.getByLabel('Kata sandi (min. 12 karakter)').fill(password)
  await page.getByRole('button', { name: 'Daftar' }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp0')

  // Top-up: LedgerPay hanya membuat sesi; user membayar di halaman provider.
  await page.getByRole('navigation', { name: 'Menu utama' }).getByRole('link', { name: 'Top-up', exact: true }).click()
  await page.getByLabel(/Nominal/).fill('100000')
  await page.getByRole('button', { name: 'Lanjut bayar' }).click()
  await expect(page).toHaveURL(/\/checkout\/ch_/)
  await expect(page.getByRole('heading', { name: 'MockPay' })).toBeVisible()
  await page.getByRole('button', { name: 'Bayar' }).click()

  await expect(page).toHaveURL(/localhost:5173\/topups\//)
  await expect(page.getByTestId('topup-status')).toHaveText('Top-up berhasil', { timeout: 15_000 })
  await page.getByRole('navigation', { name: 'Menu utama' }).getByRole('link', { name: 'Beranda', exact: true }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp100.000')

  // Transfer.
  await page.getByRole('navigation', { name: 'Menu utama' }).getByRole('link', { name: 'Transfer', exact: true }).click()
  await page.getByLabel('Email penerima').fill(andi)
  await page.getByLabel('Nominal (Rp)').fill('30000')
  await page.getByRole('button', { name: 'Kirim' }).click()
  await expect(page.getByTestId('transfer-done')).toHaveText('Terkirim Rp30.000 ke Andi S.')

  // Tarik saldo: 202 → diproses worker + bank (mock) → Berhasil.
  await page.getByRole('navigation', { name: 'Menu utama' }).getByRole('link', { name: 'Tarik dana', exact: true }).click()
  await page.getByLabel(/Nominal/).fill('20000')
  await page.getByLabel('Nomor rekening').fill('1234567890')
  await page.getByLabel('Nama pemilik rekening').fill('Rina Wulandari')
  await page.getByRole('button', { name: 'Tarik' }).click()
  await expect(page.getByTestId('payout-status')).toHaveText('Berhasil dikirim', { timeout: 20_000 })
  await expect(page.getByText('MOCKBANK ******7890')).toBeVisible()

  // Keluar → masuk lagi (login → wallet).
  await page.getByTestId('logout').click()
  await expect(page).toHaveURL(/\/login/)
  await page.getByLabel('Email').fill(rina)
  await page.getByLabel('Kata sandi').fill(password)
  await page.getByRole('button', { name: 'Masuk' }).click()
  await expect(page.getByTestId('balance')).toHaveText('Rp50.000')
})
