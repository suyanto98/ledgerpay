# Naskah video demo LedgerPay (90 detik)

Untuk recruiter yang tidak sempat mencoba demo. Satu pesan: **uang tidak pernah salah hitung, bahkan saat
jaringan atau server bermasalah**. Semua yang ditampilkan memakai demo publik ("Coba demo"), jadi penonton bisa
mengulangnya sendiri.

Setelah diunggah: isi `VITE_VIDEO_URL` saat build SPA (tautan muncul di landing) dan `[LINK]` video di README.
Tiga tautan opsional SPA adalah build arg image web: `VITE_VIDEO_URL`, `VITE_REPO_URL` ("Lihat kode"), dan
`VITE_CONTACT_URL` ("Hubungi pembuatnya" di rangkuman demo, contoh profil LinkedIn). Kosong = tautan disembunyikan.

## Persiapan
- Browser jendela 1280×800, zoom 110%, mode terang, tanpa bookmark bar dan notifikasi.
- Buka landing (`/welcome`); jangan klik apa pun sebelum mulai merekam.
- Rekam layar + suara. Narasi santai, ±130 kata per menit. Ulangi per segmen kalau salah.

## Storyboard

| Waktu | Di layar | Narasi | Teks di layar |
|---|---|---|---|
| 0:00–0:08 | Landing LedgerPay | "Ini LedgerPay, dompet digital yang saya bangun dengan pembukuan dua sisi seperti bank. Saya tunjukkan kenapa saldonya tidak pernah salah." | LedgerPay · proyek portofolio |
| 0:08–0:15 | Klik **Coba demo** → Beranda | "Satu klik, tanpa daftar: saya masuk sebagai Budi, lengkap dengan saldo, kontak, dan riwayat contoh." | Tanpa daftar |
| 0:15–0:27 | Kartu tagihan Kopi Senja → **Bayar** → layar sukses | "Membayar tagihan kafe seperti di toko online. Walau halaman ini dibuka di dua tab, tagihan hanya bisa terbayar sekali." | Bayar sekali, terpotong sekali |
| 0:27–0:42 | Menu **Uji ketahanan** → **Kirim dua kali sekaligus** | "Apa yang terjadi kalau tombol kirim tertekan dua kali karena koneksi lambat? Dua permintaan, tetapi saldo hanya terpotong sekali. Permintaan kedua hanya mengulang hasil pertama." | 1. Klik ganda ≠ potong ganda |
| 0:42–1:00 | **Jalankan uji 100 transfer**; biarkan grid terisi | "Sekarang yang lebih berat: dompet berisi seratus ribu, menerima seratus permintaan transfer sepuluh ribu di detik yang sama. Hasil yang benar hanya satu: tepat sepuluh berhasil, saldo nol, tidak pernah minus. Dan itulah hasilnya." | 2. 100 transfer bersamaan → tepat 10 lolos |
| 1:00–1:15 | Riwayat → klik satu transaksi → tabel debit/kredit; lalu **Periksa buku besar** | "Setiap rupiah dicatat di dua sisi, dan harus seimbang. Saldo yang saya lihat dihitung ulang dari semua catatan, dan cocok sampai rupiah terakhir." | 3. Cocok sampai rupiah terakhir |
| 1:15–1:30 | Kartu rangkuman di Beranda | "Semua yang barusan Anda lihat diuji otomatis setiap kali kodenya berubah, termasuk demo ini. Silakan coba sendiri lewat tautan di deskripsi." | Coba sendiri: [URL demo] |

## Catatan
- Jangan menyebut istilah teknis di narasi (idempotency, lock, double-entry). Untuk penonton teknis, tautkan README.
- Angka di video harus sama dengan yang tampil di layar.
- Uji 100 transfer dibatasi 3 kali per menit per pengguna; latihan dulu, lalu tunggu sebentar sebelum merekam.
