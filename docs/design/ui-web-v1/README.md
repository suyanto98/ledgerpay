# UI Web v1 — LedgerPay (desktop)

Desain web/desktop untuk SPA Vue, belum diimplementasikan. Pasangan versi mobile: `../ui-mobile-v1/`.

- Canvas (baris kedua, bisa diklik/Play): (tidak dipublikasikan)
- Design tokens (warna, font, radius): sama dengan `../ui-mobile-v1/README.md`.

## Layout

- Sidebar navy `#0B2559` lebar 248px: Beranda, Transfer, Top-up, Tarik dana, Riwayat, lalu grup OPERASIONAL (Ops console), dan Keluar di bawah. Item aktif: latar `rgba(255,255,255,0.12)`, teks putih bold.
- Konten fluid, padding 28px 32px, max-width 1200px (Ops 1240px).
- Responsif: baris `flex-wrap`, sidebar `flex: 1 1 248px`, konten `flex: 999 1 560px`, jadi sidebar menumpuk di atas di layar sempit. Tabel dibungkus `overflow-x: auto`.

## Halaman → route

| File | Route Vue | Isi |
|---|---|---|
| `Dashboard.dc.html` | `/` — `WalletView.vue` | kartu saldo + aksi, kartu "Sedang diproses", tabel transaksi terakhir |
| `Login.dc.html` | `/login` — `LoginView.vue` | split screen: panel brand + 3 jaminan, form login |
| `Transfer.dc.html` | `/transfer` — `TransferView.vue` | form (penerima, nominal, catatan) + panel ringkasan dengan tombol Kirim |
| `History.dc.html` | route baru (mis. `/history`) | filter (cari, jenis, status, periode), tabel + paginasi, Unduh CSV |
| `Ops.dc.html` | `/ops` — `OpsView.vue` | jalankan recon, ringkasan hasil, antrean recon, payout menggantung, panel resolve (bukti + alasan) |

## Belum ada / perlu dicek saat implementasi

- Route & endpoint daftar transaksi dengan filter untuk halaman Riwayat.
- Fitur Unduh CSV (hanya ada di desain).
- Halaman web untuk Top-up, Tarik dana, Daftar, dan Checkout belum didesain.
