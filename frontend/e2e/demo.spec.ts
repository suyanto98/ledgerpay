import { expect, test } from '@playwright/test'

// "Coba demo tanpa daftar" (image demo, DEMO_MODE=true di compose dev): satu klik → Budi dengan saldo,
// riwayat berlabel, kontak, dan tagihan menunggu. Alur pengunjung awam: bayar tagihan, kirim ke kontak.
test('one-click demo → labelled history → pay pending bill → transfer to a contact', async ({ page }) => {
  await page.goto('/login')
  await page.getByTestId('start-demo').click()
  await expect(page).toHaveURL('/')
  await expect(page.getByTestId('demo-banner')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Budi')
  await expect(page.getByTestId('balance')).toHaveText('Rp2.692.000')
  // Riwayat menyebut orang dan merchant, bukan hanya "Transfer".
  await expect(page.getByText('Dari Siti Rahma')).toBeVisible()
  await expect(page.getByText('Transfer · Patungan makan siang')).toBeVisible()

  // Tagihan Kopi Senja → checkout → bayar dari saldo.
  const bill = page.getByTestId('demo-bill')
  await expect(bill).toContainText('Kopi Senja')
  await bill.getByRole('link', { name: 'Bayar' }).click()
  await expect(page.getByTestId('checkout-amount')).toHaveText('Rp58.000')
  await page.getByRole('button', { name: /Bayar Rp58\.000/ }).click()
  await expect(page.getByTestId('checkout-status')).toBeVisible()
  await page.goto('/')
  await expect(page.getByTestId('balance')).toHaveText('Rp2.634.000')
  await expect(page.getByTestId('demo-bill')).toHaveCount(0)

  // Langkah "Kirim uang ke Siti": penerima terisi dari kontak demo.
  await page.getByRole('link', { name: /Kirim uang ke Siti Rahma/ }).click()
  await expect(page.getByLabel('Email penerima')).toHaveValue(/^siti@.+\.demo\.ledgerpay\.test$/)
  await page.getByLabel('Nominal (Rp)').fill('10.000')
  await page.getByRole('button', { name: /^Kirim Rp10\.000/ }).click()
  await expect(page.getByTestId('transfer-done')).toContainText('Siti Rahma')
})

// Uji ketahanan: klik ganda = satu potongan; 100 transfer bersamaan = tepat 10 berhasil, saldo akhir Rp0.
test('lab: double send charges once, 100 parallel transfers → exactly 10 succeed', async ({ page }) => {
  await page.goto('/login')
  await page.getByTestId('start-demo').click()
  await expect(page.getByTestId('demo-banner')).toBeVisible()
  await page.getByTestId('try-lab').click()
  await expect(page).toHaveURL('/lab')

  await page.getByTestId('lab-double').click()
  const dbl = page.getByTestId('lab-double-result')
  await expect(dbl).toContainText('Dua kali ditekan, satu kali terpotong.')
  await expect(dbl).toContainText('Diulang dari hasil pertama')

  await page.getByTestId('lab-stress').click()
  const res = page.getByTestId('lab-stress-result')
  await expect(res).toContainText('10 transfer', { timeout: 20_000 })
  await expect(res).toContainText('90 transfer')
  await expect(page.getByText('Tepat 10 lolos, saldo berakhir Rp0, tidak pernah minus.')).toBeVisible()
})
