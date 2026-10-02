# Step 3.2 — LedgerPay: Financial Invariants

> Bagian dari [Step 3 — Domain Design](00-index.md). ID invariant LI-01…LI-15 dan kategori mekanisme mengikuti glosarium kanonik.
> Prinsip: **setiap invariant P0 minimal punya satu penjaga di level database atau transaksi**. `APP` saja tidak cukup, karena validasi aplikasi bisa dilewati oleh bug.

Format per invariant: **Pernyataan · Kenapa penting · Penjaga · Deteksi di production · Test yang membuktikan**.

---

## LI-01 — Journal selalu seimbang
| | |
|---|---|
| **Pernyataan** | Untuk setiap journal J: Σ amount entry `D` = Σ amount entry `C`; jumlah entry ≥ 2; setiap `amount > 0`; semua entry ber-currency sama dengan journal |
| **Kenapa** | Journal tidak seimbang = uang tercipta atau hilang. Ini pelanggaran paling fatal di sistem keuangan |
| **Penjaga** | `DB-CONSTRAINT`: `CHECK (amount > 0)`, `CHECK (direction IN ('D','C'))`, `CONSTRAINT TRIGGER … DEFERRABLE INITIALLY DEFERRED` pada `journal_entries` yang menghitung Σ D, Σ C, dan count per journal **saat commit** (deferred, karena entries di-insert satu per satu); FK currency entry = currency journal (atau CHECK via trigger yang sama). `APP`: `ledger.PostJournal` memvalidasi sebelum insert sehingga error-nya ramah. `DB-TX`: journal + entries dalam satu Tx |
| **Deteksi** | `ledger_unbalanced_journal_total` (job verifikasi; target 0, alert jika > 0); `ledger_imbalance_total` (Σ D global − Σ C global) |
| **Test** | Property test (journal acak valid selalu lolos, journal tak seimbang selalu ditolak di DB walaupun validasi APP dimatikan); integration test commit journal 1 entry → gagal |

## LI-02 — Saldo wallet tidak negatif
| | |
|---|---|
| **Pernyataan** | Untuk setiap account dengan `is_system = false`: `balance ≥ 0` di setiap titik commit |
| **Kenapa** | Saldo negatif = user membelanjakan uang yang tidak ada (double-spend). Kerugian langsung perusahaan |
| **Penjaga** | `LOCK`: `LockAccountsOrdered` (`FOR UPDATE ORDER BY id`) sebelum cek saldo. `APP`: validasi saldo cukup → 422 `INSUFFICIENT_BALANCE`. `DB-CONSTRAINT`: `CHECK (is_system OR balance >= 0)` sebagai jaring terakhir. `DB-TX`: update saldo + entries atomik |
| **Deteksi** | Constraint violation `23514` pada `accounts` → metrik `db_constraint_violation_total{constraint="accounts_balance_nonneg"}` (seharusnya 0; > 0 berarti ada bug di jalur APP) |
| **Test** | Concurrency test Demo B (100 goroutine × 10.000 dari saldo 100.000 → tepat 10 sukses); test pembanding naif (hanya di test code) menunjukkan overdraft; test "validasi APP dihapus" (§Defense in depth) |

## LI-03 — Saldo materialized = Σ entries
| | |
|---|---|
| **Pernyataan** | Untuk setiap wallet: `accounts.balance = Σ C − Σ D` atas seluruh `journal_entries` akun tersebut |
| **Kenapa** | Saldo yang ditampilkan dan dipakai untuk validasi harus sama dengan bukti ledger. Drift = saldo salah tanpa jejak |
| **Penjaga** | `DB-TX`: update `balance` hanya di dalam `PostJournal`, dalam Tx yang sama dengan insert entries. `APP`: tidak ada jalur lain yang menulis `balance` (batas modul + depguard; sqlc query `UpdateBalance` hanya ada di package `ledger`). `DB-CONSTRAINT` (opsional, dipertimbangkan di Step 4): role aplikasi tanpa grant UPDATE kolom `balance` di luar fungsi SQL tertentu. `JOB`: verifikasi tiap 5 menit |
| **Deteksi** | `ledger_balance_drift_total` (jumlah akun drift) + `ledger_balance_drift_amount`; alert jika > 0 |
| **Test** | Property test: urutan operasi acak → setiap akhir langkah `balance == Σ entries`; test job verifikasi mendeteksi drift yang disuntikkan secara manual |

## LI-04 — Ledger append-only; koreksi lewat reversal
| | |
|---|---|
| **Pernyataan** | Baris `journals` dan `journal_entries` tidak pernah di-UPDATE atau di-DELETE. Koreksi = journal baru dengan `reversal_of`. Satu journal maksimal di-reverse sekali |
| **Kenapa** | Ledger yang bisa diedit tidak bisa diaudit. Edit diam-diam = jejak penipuan hilang |
| **Penjaga** | `DB-CONSTRAINT`: trigger `BEFORE UPDATE OR DELETE` → `RAISE EXCEPTION`; grant role aplikasi hanya `INSERT, SELECT`; `UNIQUE (reversal_of)`; FK `reversal_of → journals.id`. `APP`: reversal harus berisi entry yang merupakan cermin journal asal (validasi di `ledger`) |
| **Deteksi** | Exception trigger tercatat di log error + metrik `ledger_mutation_attempt_total` (target 0) |
| **Test** | Integration test: `UPDATE journal_entries` dan `DELETE journals` dengan role aplikasi → gagal; reversal kedua atas journal yang sama → `23505` |

## LI-05 — Satu idempotency key = maksimal satu efek
| | |
|---|---|
| **Pernyataan** | Untuk setiap `(client_id, endpoint, key)` terdapat maksimal satu efek finansial. Key yang sama dengan `request_hash` berbeda ditolak (422), dan key yang sama dengan hash sama mengembalikan response yang tersimpan |
| **Kenapa** | Retry client (timeout, double-click) adalah perilaku normal. Tanpa ini, retry yang benar menghasilkan double-debit |
| **Penjaga** | `DB-CONSTRAINT`: `UNIQUE (client_id, endpoint, key)`. `DB-TX`: key di-insert **dalam Tx yang sama** dengan efek (ADR-L02 pola a), sehingga commit key ⇔ commit efek. `LOCK`: request kedua menunggu di unique index sampai Tx pertama selesai. `IDEMPOTENCY`: replay response; pola recovery point untuk top-up |
| **Deteksi** | `idempotency_replay_total`, `idempotency_conflict_total` (422 reuse), `idempotency_wait_timeout_total` (409) |
| **Test** | Demo A: request identik 2× → satu journal; hash beda → 422; dua request bersamaan dengan key sama → tepat satu efek; Tx pertama di-rollback paksa → request kedua diproses normal |

## LI-06 — Satu objek bisnis, satu journal per tujuan
| | |
|---|---|
| **Pernyataan** | Untuk setiap `(source_type, source_id, purpose)` terdapat maksimal satu journal |
| **Kenapa** | Jaring pengaman di bawah idempotency HTTP: walaupun handler/worker memproses ulang objek yang sama (inbox diproses 2×, job diambil ulang), posting ganda mustahil |
| **Penjaga** | `DB-CONSTRAINT`: `UNIQUE (source_type, source_id, purpose)` pada `journals`. `LOCK`: guard status objek bisnis (transisi kedua mendapat 0 row sehingga tidak memanggil `PostJournal`) |
| **Deteksi** | Pelanggaran unique → metrik `db_constraint_violation_total{constraint="journals_source_purpose_uniq"}` (seharusnya 0; > 0 berarti guard status bocor) |
| **Test** | Proses inbox event yang sama dua kali secara paralel → satu journal `TOPUP_CREDIT`; jalankan handler `payout.submit` dua kali untuk payout yang sama → satu journal settle |

## LI-07 — Transisi status hanya yang sah
| | |
|---|---|
| **Pernyataan** | Status TopUp, PaymentIntent, Payout, InboxEvent, WebhookDelivery, dan ReconItem hanya berpindah sesuai state machine ([01 §5](01-ledgerpay-domain-model.md#5-state-machines)). Status final tidak berubah, kecuali `EXPIRED` TopUp (status lunak, ADR-L08) |
| **Kenapa** | Transisi ilegal (misalnya `FAILED → SUCCEEDED` pada payout yang sudah di-reverse) menghasilkan efek finansial ganda atau berlawanan |
| **Penjaga** | `LOCK`: `UPDATE … SET status = $to WHERE id = $id AND status = $from` (RowsAffected = 0 → abort tanpa efek). `APP`: tabel transisi eksplisit di domain (fungsi `CanTransition(from, to)`). `DB-CONSTRAINT`: `CHECK (status IN (...))`; trigger opsional yang menolak UPDATE dari status terminal |
| **Deteksi** | `invalid_state_transition_total{entity}`; konflik event provider → `webhook_status_conflict_total` + item recon |
| **Test** | Unit test tabel transisi (semua pasangan from×to); integration test dua worker menerapkan hasil berbeda pada payout yang sama → tepat satu menang |

## LI-08 — Σ refund ≤ amount pembayaran
| | |
|---|---|
| **Pernyataan** | Untuk setiap PaymentIntent: `refunded_amount = Σ refund.amount ≤ amount` |
| **Kenapa** | Refund berlebih = merchant mengembalikan uang lebih dari yang diterima, atau user mendapat uang gratis |
| **Penjaga** | `LOCK`: `FOR UPDATE` pada intent sebelum menghitung sisa. `DB-CONSTRAINT`: `CHECK (refunded_amount <= amount)` pada `payment_intents`; `refunded_amount` di-update di Tx yang sama dengan insert refund. `APP`: validasi status `SUCCEEDED`/`PARTIALLY_REFUNDED` |
| **Deteksi** | Job verifikasi `refunded_amount == Σ refunds` → `payment_refund_drift_total` |
| **Test** | Concurrency test: amount 100.000, 5 refund paralel × 30.000 → tepat 3 sukses |

## LI-09 — Satu webhook event, maksimal satu efek
| | |
|---|---|
| **Pernyataan** | Untuk setiap `(provider, provider_event_id)` efek bisnis diterapkan maksimal sekali |
| **Kenapa** | Provider mengirim ulang webhook (at-least-once). Tanpa dedupe, top-up dikredit berulang |
| **Penjaga** | `DB-CONSTRAINT`: `UNIQUE (provider, provider_event_id)` pada `webhook_inbox`. `LOCK`: transisi TopUp monoton dengan guard status. `DB-CONSTRAINT` (lapis kedua): LI-06 `UNIQUE (source_type='topup', source_id, 'TOPUP_CREDIT')`. `DB-TX`: inbox → `PROCESSED` di Tx yang sama dengan efek |
| **Deteksi** | `webhook_duplicate_total` (normal, informatif); `webhook_inbox_dead_total` (alert) |
| **Test** | Failure scenario #6: webhook yang sama dikirim 3× (sebagian paralel) → satu journal, saldo naik sekali |

## LI-10 — Satu payout, maksimal satu pengiriman ke bank
| | |
|---|---|
| **Pernyataan** | Dana payout keluar ke bank maksimal sekali, walaupun ada retry, crash, atau timeout |
| **Kenapa** | Payout ganda = uang perusahaan keluar dua kali dan sulit ditarik kembali |
| **Penjaga** | `DB-TX`: `SUBMITTED` di-commit **sebelum** memanggil bank, sehingga crash setelahnya terdeteksi. `APP`: payout `SUBMITTED`/`UNKNOWN` tidak pernah di-resubmit tanpa status query; resubmit hanya jika `NOT_FOUND` setelah safe window. `IDEMPOTENCY`: idempotency key ke bank = `payout_id` (lapis eksternal). `JOB`: reconciliation menangkap sisa kasus |
| **Deteksi** | `payout_unknown_total`, `payout_unknown_oldest_age_seconds` (alert jika > 6 jam); recon `STATUS_MISMATCH` |
| **Test** | Demo C (bank timeout-after-success + kill worker → mock bank mencatat 1 transfer); test resubmit hanya setelah `NOT_FOUND` + window |

> LI-10 adalah satu-satunya invariant yang **tidak bisa** dijaga sepenuhnya oleh database kita, karena efeknya ada di sistem lain. Penjaganya adalah kombinasi protokol (commit-before-call + idempotency key di provider) dan deteksi (recon). Ini poin interview yang penting.

## LI-11 — Nominal integer, positif, tanpa float
| | |
|---|---|
| **Pernyataan** | Semua nominal disimpan dan diproses sebagai integer minor unit (`BIGINT` / `int64`), positif untuk entry/transaksi, dan tidak pernah melewati float di Go, SQL, atau JSON |
| **Kenapa** | Float menghasilkan selisih pembulatan yang terakumulasi, sehingga LI-01/LI-03 rusak secara halus |
| **Penjaga** | `DB-CONSTRAINT`: kolom `BIGINT` + `CHECK (amount > 0)`. `APP`: value object `Money` tanpa konstruktor float; JSON `amount` = string (ADR-L04); OpenAPI schema `type: string, pattern: ^[0-9]+$`. `TEST`: linter/test yang melarang `float32/float64` di package domain uang |
| **Deteksi** | Request dengan amount non-integer → 400 (metrik validasi) |
| **Test** | Unit test `Money` (overflow, beda currency); contract test menolak `"amount": 10.5` dan JSON number |

## LI-12 — Liability cocok dengan aset clearing (eventual)
| | |
|---|---|
| **Pernyataan** | Setelah reconciliation hari D: Σ saldo wallet + `PAYOUT_IN_TRANSIT` dapat dijelaskan oleh `PROVIDER_CLEARING` + `BANK_CLEARING` serta laporan provider. Semua mismatch terklasifikasi dan berstatus `RESOLVED` atau berada di antrean investigasi |
| **Kenapa** | Inilah pertanyaan regulator dan finance: "apakah uang user benar-benar ada?" |
| **Penjaga** | `JOB`: recon harian (LF-17). `DB-CONSTRAINT`: `UNIQUE (provider, date, file_hash)` agar recon idempoten. `APP`: resolusi hanya lewat journal (tidak ada edit saldo) |
| **Deteksi** | `recon_mismatch_total{category}`, `recon_open_items_oldest_age_seconds`, `recon_run_failed_total` |
| **Test** | Demo D; integration test file settlement dengan kasus tiap kategori → klasifikasi benar; recon diulang → hasil sama |

## LI-13 — Payment intent dibayar maksimal sekali
| | |
|---|---|
| **Pernyataan** | Setiap PaymentIntent maksimal menghasilkan satu journal `PAYMENT`. `merchant_order_id` hanya punya satu intent aktif per merchant |
| **Kenapa** | Order dibayar dua kali = user rugi, dan merchant harus refund manual |
| **Penjaga** | `LOCK`: `FOR UPDATE` intent + guard status `REQUIRES_CONFIRMATION`. `DB-CONSTRAINT`: LI-06 (`payment_intent`, id, `PAYMENT`); partial unique index `UNIQUE (merchant_id, merchant_order_id) WHERE status IN ('REQUIRES_CONFIRMATION','SUCCEEDED','PARTIALLY_REFUNDED','REFUNDED')`. `IDEMPOTENCY`: confirm wajib key |
| **Deteksi** | `payment_double_confirm_rejected_total` (informatif) |
| **Test** | Konfirmasi paralel dari dua tab dengan key berbeda → satu sukses, satu `409 INVALID_STATE_TRANSITION` |

## LI-14 — Setiap efek finansial bisa ditelusuri
| | |
|---|---|
| **Pernyataan** | Setiap journal memiliki `source_type`, `source_id`, `purpose`, `correlation_id`; journal dari API juga memiliki `idempotency_key_id`; aksi ops memiliki `AuditEntry` dengan actor dan alasan |
| **Kenapa** | Menjawab "kenapa saldo ini segini?" dan "siapa yang melakukan ini?" tanpa menebak |
| **Penjaga** | `DB-CONSTRAINT`: kolom `NOT NULL` (`source_type`, `source_id`, `purpose`, `correlation_id`); FK `idempotency_key_id` nullable (hanya untuk journal dari worker). `DB-TX`: `AuditEntry` ditulis di Tx yang sama dengan aksi ops. `APP`: middleware memastikan correlation ID selalu ada |
| **Deteksi** | Log terstruktur dengan correlation ID; ops console menampilkan timeline per correlation ID |
| **Test** | E2E: dari satu response transfer, telusuri `request_id` → journal → entries → audit di ops console |

## LI-15 — Actor hanya mengakses resource miliknya
| | |
|---|---|
| **Pernyataan** | User hanya membaca/mengubah wallet, transfer, top-up, dan payout miliknya; merchant hanya intent, refund, dan delivery miliknya; ops hanya lewat `/v1/ops/*` dengan role `OPS` |
| **Kenapa** | BOLA/IDOR adalah kerentanan API nomor satu (OWASP API1). Di sistem uang dampaknya langsung |
| **Penjaga** | `APP`: query selalu di-scope (`WHERE id = $id AND owner_id = $principal`), resource bukan milik → 404 (ADR-010). `DB-CONSTRAINT`: tidak ada RLS di LedgerPay (keputusan sadar: satu jenis owner per tabel, scoping cukup eksplisit di query sqlc). `TEST`: authorization test per endpoint |
| **Deteksi** | `authz_denied_total{endpoint}`; lonjakan 404 per principal → indikasi enumerasi |
| **Test** | BOLA test suite: user A ke resource B (setiap endpoint) → 404; merchant A ke intent merchant B → 404 |

> LI-15 dijaga oleh `APP` + `TEST` saja, tanpa penjaga DB. Ini pengecualian sadar terhadap prinsip di atas: RLS untuk ownership per user menambah kompleksitas (set context per user di setiap Tx) dengan manfaat kecil, karena setiap query sqlc ditulis eksplisit dengan filter owner dan diuji otomatis. Kontras dengan PayRun, di mana kebocoran lintas tenant bersifat eksistensial sehingga RLS diwajibkan (ADR-P01).

---

## Summary Matrix

| Invariant | DB-CONSTRAINT | DB-TX | LOCK | APP | IDEMPOTENCY | JOB | TEST | OBS |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| LI-01 Journal seimbang | ✓ | ✓ | | ✓ | | ✓ | ✓ | ✓ |
| LI-02 Saldo ≥ 0 | ✓ | ✓ | ✓ | ✓ | | | ✓ | ✓ |
| LI-03 Balance = Σ entries | ○ | ✓ | | ✓ | | ✓ | ✓ | ✓ |
| LI-04 Append-only | ✓ | | | ✓ | | | ✓ | ✓ |
| LI-05 Idempotency | ✓ | ✓ | ✓ | | ✓ | | ✓ | ✓ |
| LI-06 Satu journal per tujuan | ✓ | | ✓ | | | | ✓ | ✓ |
| LI-07 Transisi sah | ✓ | | ✓ | ✓ | | | ✓ | ✓ |
| LI-08 Σ refund ≤ amount | ✓ | ✓ | ✓ | ✓ | | ✓ | ✓ | ✓ |
| LI-09 Webhook sekali | ✓ | ✓ | ✓ | | | | ✓ | ✓ |
| LI-10 Payout sekali | | ✓ | | ✓ | ✓ (eksternal) | ✓ | ✓ | ✓ |
| LI-11 Integer, tanpa float | ✓ | | | ✓ | | | ✓ | ✓ |
| LI-12 Liability = aset (eventual) | ✓ | | | ✓ | | ✓ | ✓ | ✓ |
| LI-13 Intent dibayar sekali | ✓ | | ✓ | | ✓ | | ✓ | ✓ |
| LI-14 Traceability | ✓ | ✓ | | ✓ | | | ✓ | ✓ |
| LI-15 Ownership | | | | ✓ | | | ✓ | ✓ |

✓ = penjaga utama · ○ = dipertimbangkan di Step 4 (grant kolom).

**Cek prinsip:** semua invariant P0 punya penjaga DB-CONSTRAINT atau DB-TX, **kecuali** LI-15 (pengecualian sadar, dijelaskan di atas). LI-10 dijaga oleh DB-TX (commit-before-call) plus protokol eksternal.

---

## Defense in Depth: LI-02 ketika ada bug

**Skenario:** sebuah refactor tidak sengaja menghapus baris `if balance < amount { return ErrInsufficientBalance }` di `transfer.Service`. Code review melewatkannya.

```text
Saldo Rina 50.000. Request: transfer 80.000.

1. LockAccountsOrdered → FOR UPDATE wallet Rina & Andi            (masih berjalan normal)
2. Validasi saldo di APP                                          ✗ HILANG (bug)
3. ledger.PostJournal → UPDATE accounts SET balance = 50.000 − 80.000 = −30.000
4. CHECK (is_system OR balance >= 0)                               ✓ MENOLAK → SQLSTATE 23514
5. Seluruh Tx rollback: tidak ada journal, idempotency key, maupun transfer yang tercatat
6. Mapper error: 23514 pada constraint accounts_balance_nonneg → 422 INSUFFICIENT_BALANCE
   (dipetakan secara eksplisit; tetap aman, client boleh retry dengan key yang sama)
7. Metrik db_constraint_violation_total{constraint="accounts_balance_nonneg"} naik → alert
   "jalur validasi APP tidak berfungsi", engineer menemukan bug dari alert, bukan dari keluhan user
```

| Lapisan | Peran saat bug | Hasil |
|---|---|---|
| Row lock | Tetap menserialisasi request ke wallet yang sama | Tidak ada race tambahan |
| Validasi APP | Hilang | — |
| CHECK constraint | Menolak commit | **Tidak ada uang tercipta** |
| Atomisitas Tx | Rollback semua efek parsial | Ledger tetap seimbang, idempotency tidak "terbakar" |
| Metrik/alert | Mendeteksi bahwa lapisan APP bocor | Bug diperbaiki sebelum berdampak lebih luas |
| Concurrency test di CI | Seharusnya gagal (test "saldo kurang → 422" lolos, tetapi metrik/asersi "tidak ada 23514" gagal) | Bug tertangkap sebelum merge |

> Untuk interview: *"Validasi aplikasi menentukan pesan error yang bagus. Constraint database menentukan kebenaran. Saya ingin bug di lapisan pertama menghasilkan alert, bukan kerugian."*

---

## Invariant yang Sengaja Tidak Ditegakkan Ketat di V1

| Invariant / properti | Perlakuan V1 | Alasan | Kapan diperketat |
|---|---|---|---|
| **LI-12** Liability = aset clearing | **Eventual**, diverifikasi recon harian | Uang di provider/bank berada di sistem lain dan bergerak dengan jadwal settlement mereka. Kesamaan real-time mustahil tanpa integrasi real-time | Recon intraday saat volume besar (V2) |
| Saldo akun sistem | **Derived** dari entries; tidak ada `CHECK` maupun lock | Akun sistem sah bernilai negatif sesuai tipenya; materialisasi = hot row (ADR-L07) | Snapshot + delta jika query lambat (ADR-L07 opsi C) |
| LI-03 untuk akun sistem | Tidak berlaku (tidak ada `balance`) | Konsekuensi baris di atas | — |
| Batas saldo/transaksi regulasi (BR9) | P2, `APP` saja | Angka regulasi belum diverifikasi; bukan invariant uang inti | Saat LF-21 diimplementasikan; tetap dapat dikonfigurasi |
| Ownership via RLS (LI-15) | `APP` + `TEST` | Lihat catatan LI-15 | Jika jumlah jalur query membengkak atau ada akses data langsung oleh tim lain |
| Pengiriman webhook keluar tepat sekali | **At-least-once** + event ID stabil | Exactly-once delivery ke sistem eksternal mustahil; merchant wajib dedupe (didokumentasikan di API docs) | Tidak akan berubah. Ini sifat sistem terdistribusi |
| Urutan event ke merchant | Tidak dijamin | Merchant menerima status terkini via polling sebagai cadangan | Sequence number per intent jika merchant membutuhkannya |
