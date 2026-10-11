# UI Mobile v1 — LedgerPay

Desain mobile (390×844) untuk SPA Vue, belum diimplementasikan.

- File `.dc.html` di folder ini adalah salinan sumber tiap layar.

## Design tokens

| Token | Nilai |
|---|---|
| Primary (navy) | `#0B2559` |
| Accent / tombol | `#1D5BD8` (hover `#123E8C`), tint `#EAF0FD` |
| Background | `#F3F5F9`, kartu `#FFFFFF`, border `#E3E7EF` / `#D5DBE6` |
| Teks | `#0F172A`, sekunder `#5B6478`, label `#3A4458` |
| Sukses | `#0B6640` di atas `#E3F4EC` (ikon `#16A06A`) |
| Pending | `#7A4300` di atas `#FFF1D6` |
| Font | Plus Jakarta Sans 400–800, angka `tabular-nums` |
| Radius | kartu 16–20px, input/tombol 12–14px, chip 999px |
| Tinggi tombol | 54px primer, ≥44px target sentuh |

## Layar → route

| File | Route Vue |
|---|---|
| `Main.dc.html` (Beranda) | `/` — `WalletView.vue` |
| `Login.dc.html` | `/login` — `LoginView.vue` |
| `Transfer.dc.html` | `/transfer` — `TransferView.vue` (langkah isi) |
| `TransferConfirm.dc.html` | `/transfer` — langkah konfirmasi |
| `TransferSuccess.dc.html` | `/transfer` — langkah bukti |
| `Topup.dc.html` | `/topup` — `TopupView.vue` |
| `Payout.dc.html` | `/payouts/:id` — `PayoutStatusView.vue` |

Belum didesain: form tarik dana (`/payout`), daftar (`/register`), checkout merchant (`/checkout/:id`). Ops (`/ops`) ada di versi web.

Versi web (desktop): lihat `../ui-web-v1/`.
