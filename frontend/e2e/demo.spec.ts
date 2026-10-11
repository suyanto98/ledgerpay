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
