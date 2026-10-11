# ADR-L06 — Payout dengan Unknown Outcome

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Payout mengirim uang **keluar** dari sistem ke rekening bank. Ketika LedgerPay memanggil bank, ada tiga kemungkinan hasil, bukan dua:

1. **Sukses**: bank mengonfirmasi.
2. **Gagal definitif**: bank menolak (rekening tidak valid, dsb.).
3. **Tidak tahu**: timeout, koneksi putus, 5xx, atau worker crash setelah request terkirim. Uang **mungkin** sudah terkirim.

Kesalahan paling umum di sistem nyata adalah memperlakukan (3) sebagai (2) lalu me-*retry*, sehingga uang terkirim dua kali. Kesalahan sebaliknya adalah memperlakukan (3) sebagai gagal lalu mengembalikan saldo user, padahal uang sudah keluar.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Panggil bank secara sinkron di request API; timeout = gagal | Sederhana | Double payout atau saldo dikembalikan padahal uang keluar; request API lama |
| B. Retry otomatis sampai sukses | "Pasti terkirim" | Tanpa idempotency di sisi bank = double payout |
| C. **State machine async dengan state `UNKNOWN` + status query + reconciliation + resolusi manual** | Benar dalam semua kasus; setiap transisi bisa dijelaskan | Lebih banyak state dan komponen; dana user tertahan lebih lama di kasus UNKNOWN |
| D. Saga dengan kompensasi otomatis | Pola umum | Kompensasi tidak bisa dilakukan selama hasilnya belum diketahui. Saga tidak menyelesaikan masalah "tidak tahu" |

## Decision

**Opsi C.**

### State diagram
```text
              API (1 tx: hold journal + outbox)
                         │
                         ▼
                    ┌─────────┐   worker klaim job, commit SUBMITTED
                    │ PENDING │──────────────────────────────┐
                    └─────────┘                              ▼
                                                      ┌───────────┐
                     bank: sukses ───────────────────▶│ SUBMITTED │
                     bank: tolak definitif ──┐        └─────┬─────┘
                                             │   timeout / 5xx / crash
                                             ▼              ▼
   ┌───────────┐  status query: SUCCEEDED  ┌────────┐   ┌─────────┐
   │ SUCCEEDED │◀──────────────────────────│        │◀──│ UNKNOWN │
   └───────────┘                           │ FAILED │   └────┬────┘
        ▲            status query: FAILED  └────────┘        │
        └────────── atau resolusi manual ops ◀───────────────┘
```

### Transisi

| Dari → Ke | Pemicu | Journal |
|---|---|---|
| — → `PENDING` | API `POST /payouts` (idempotent, [ADR-L02](ADR-L02-idempotency.md)) | **Hold:** DR `WALLET:user` / CR `PAYOUT_IN_TRANSIT` |
| `PENDING` → `SUBMITTED` | Worker, **commit sebelum** memanggil bank | — |
| `SUBMITTED` → `SUCCEEDED` | Response sukses dari bank | **Settle:** DR `PAYOUT_IN_TRANSIT` / CR `BANK_CLEARING` |
| `SUBMITTED` → `FAILED` | Penolakan definitif (4xx bisnis) | **Reversal** dari hold: DR `PAYOUT_IN_TRANSIT` / CR `WALLET:user` (`reversal_of`) |
| `SUBMITTED` → `UNKNOWN` | Timeout, 5xx, error jaringan | — (dana tetap di in-transit) |
| `SUBMITTED` (job diambil ulang setelah crash) → `UNKNOWN` | Worker menemukan payout `SUBMITTED` tanpa hasil | — tidak pernah langsung submit ulang |
| `UNKNOWN` → `SUCCEEDED` / `FAILED` | Status query, reconciliation, atau resolusi manual ops | Settle / reversal seperti di atas |

Semua transisi memakai `UPDATE payouts SET status = $new WHERE id = $id AND status = $expected`. Kalau 0 row ter-update, ada pihak lain yang sudah mengubahnya, sehingga transisi dibatalkan tanpa efek. Journal dicatat dalam transaksi yang sama dengan transisi.

### Status query (backoff)
Jadwal: **30 dtk → 2 mnt → 10 mnt → 1 jam → 6 jam**. Setelah itu, payout ditandai `needs_reconciliation = true` dan masuk antrean ops.

| Jawaban status query | Tindakan |
|---|---|
| `SUCCEEDED` | → `SUCCEEDED` + settle |
| `FAILED` | → `FAILED` + reversal |
| `PROCESSING` | Tetap `UNKNOWN`, jadwalkan query berikutnya |
| `NOT_FOUND` | Lihat aturan resubmit |

### Aturan resubmit
Payout `UNKNOWN` **hanya boleh disubmit ulang** jika bank menjawab `NOT_FOUND` **setelah melewati safe window** yang didokumentasikan provider (mock: 5 menit). Submit ulang **selalu** memakai idempotency key yang sama (`payout_id`), sehingga kalau bank ternyata sudah memprosesnya, bank tidak akan memproses dua kali. Jadi ada dua lapis perlindungan: status query **dan** idempotency di sisi bank.

### Resolusi manual
Ops (Sari) melihat payout `UNKNOWN` atau mismatch dari reconciliation, lalu memilih *mark succeeded* / *mark failed* dengan alasan wajib dan bukti (referensi laporan bank). Aksi ini memakai transisi dan journal yang sama, dan tercatat di audit log (siapa, kapan, alasan, nilai sebelum/sesudah).

### Observability
- `payout_unknown_total` (counter) dan `payout_unknown_current` (gauge).
- `payout_unknown_oldest_age_seconds`: alert jika > 24 jam.
- Log setiap transisi dengan `payout_id`, `correlation_id`, dan `attempt`.

### Amandemen (threat model): respons provider yang tidak cocok
Respons sukses yang `payout_id`, amount, atau rekening tujuannya tidak cocok dengan catatan lokal **tidak** diperlakukan sebagai `SUCCEEDED`. Payout menjadi `UNKNOWN` + `needs_reconciliation = true` + alert (indikasi bug atau provider yang dikompromikan).

### Catatan implementasi (Sprint 6)
- **Guard sebelum journal tanpa melanggar FK:** `settle_journal_id`/`reversal_journal_id` adalah FK langsung (tidak deferred), sehingga `UPDATE payouts … SET settle_journal_id = $j` tidak bisa dijalankan sebelum journal ada. Urutannya: `SELECT … FROM payouts WHERE id = $p AND status = ANY($expected) FOR UPDATE` (guard + lock, pihak yang kalah mendapat 0 baris dan tidak pernah memposting) → `PostJournal` → `UPDATE` status + journal id → `CompleteInTx`.
- **Respons tidak cocok** dideteksi adapter (`SubmitResult.Mismatch`): reference, amount, currency, dan status harus sama dengan catatan lokal.
- **Safe window** dihitung dengan jam DB (`now() - submitted_at`), dikonfigurasi `PAYOUT_SAFE_WINDOW` (default 5 menit).
- Demo C (`make demo-c`): worker di-`docker kill` saat menunggu bank → lease 30 dtk → UNKNOWN → status query 30 dtk → SUCCEEDED; bank: 1 request, 1 transfer.

### Catatan implementasi (Sprint 8): resolusi manual oleh ops

- `POST /ops/payouts/{id}/resolve` memakai jalur yang SAMA dengan status check (`settleTx`/`reverseTx`): guard + lock baris payout → journal → update status. Ops tidak pernah "mengedit saldo".
- Bukti (`evidence_ref`) + alasan wajib; tanpa itu 422 `RESOLUTION_EVIDENCE_REQUIRED`. Bukti dan hasil disimpan di `audit_log.diff`, di transaksi yang sama.
- Race status check vs ops (19-tx-concurrency §3g) diuji dua arah dengan test hook (`TestPayoutStatusCheck_VsOpsResolve`, `TestOpsResolve_VsStatusCheck`). Mutasi "hapus `FOR UPDATE`" membuat keduanya gagal.
- Tambahan: `UPDATE` status yang mengenai 0 baris SETELAH journal diposting sekarang me-rollback seluruh tx (`errStatusChanged`). Sebelumnya hasilnya diabaikan; aman selama lock ada, tetapi tanpa lapis ini satu bug lock bisa menyisakan journal tanpa perubahan status.

## Reason

- Satu-satunya pendekatan yang benar untuk ketiga hasil tanpa pernah menebak.
- Commit `SUBMITTED` sebelum memanggil bank membuat crash di tengah jalan bisa **dideteksi**: payout `SUBMITTED` tanpa hasil berarti "mungkin sudah terkirim".
- Dana tetap di `PAYOUT_IN_TRANSIT` selama UNKNOWN, sehingga user tidak bisa membelanjakannya dua kali dan perusahaan tahu berapa dana yang belum pasti.

## Trade-offs

- Dalam kasus UNKNOWN, dana user tertahan hingga beberapa jam. UX harus menjelaskan status "Sedang diproses".
- Lebih banyak state dan job dibanding payout sinkron.
- Resolusi manual adalah titik rawan penyalahgunaan; dimitigasi dengan role khusus, alasan wajib, dan audit.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Volume payout tinggi | Batch payout (satu file ke bank) dengan status per item; state machine yang sama per item |
| Provider mendukung webhook payout | Tambah jalur webhook ke inbox ([ADR-L08](ADR-L08-inbound-webhook-inbox.md)); status query tetap sebagai cadangan |
| Kewajiban 4-eyes untuk resolusi manual | Resolusi butuh approval kedua dari ops lain |

## Interview angle

- **"Timeout dari bank, apa yang kamu lakukan?"** → Bukan retry, bukan refund. Tandai UNKNOWN, query status, lalu reconciliation. Resubmit hanya setelah NOT_FOUND yang pasti, dan dengan idempotency key yang sama.
- **"Worker crash tepat setelah mengirim request ke bank?"** → Status `SUBMITTED` sudah di-commit sebelum memanggil bank, jadi worker berikutnya tahu hasilnya mungkin sudah ada dan melakukan status query.
- **"Kenapa tidak saga?"** → Kompensasi butuh kepastian hasil. Masalahnya justru ketidakpastian.

## Related

[ADR-L01](ADR-L01-double-entry-ledger.md) · [ADR-L02](ADR-L02-idempotency.md) · [ADR-L05](ADR-L05-mock-provider.md) · [ADR-L08](ADR-L08-inbound-webhook-inbox.md) · [ADR-006](ADR-006-background-processing-postgres-queue.md) · [ADR-011](ADR-011-transaction-management.md)
