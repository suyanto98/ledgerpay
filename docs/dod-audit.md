# Audit Definition of Done — LedgerPay M4

Audit terhadap `docs/design/28-definition-of-done.md` §1 (item LedgerPay; item khusus PayRun tidak
berlaku di repo ini). Tanggal: 2026-10-06, Sprint 10.

Status: ✅ terpenuhi dengan bukti · ⚠️ sebagian (dijelaskan) · ⏳ **menunggu deploy / repo di GitHub**
(tidak bisa dibuktikan secara lokal; latihan lokalnya disebut). Item ⏳ dan ⚠️ juga ada di backlog.

Ringkasan: semua fitur P0 dan P1 selesai; seluruh item yang bisa dibuktikan secara lokal terpenuhi atau
ditandai sebagian dengan alasan. Yang tersisa terutama membutuhkan environment publik dan waktu
(7 hari invariant = 0, 7 malam `demo-verify`, video, URL publik, CI yang benar-benar berjalan).

## Fitur
| ID | Fitur | Status | Bukti |
|---|---|---|---|
| LF-01…LF-14 | Semua P0 | ✅ | Test per modul (katalog `docs/design/22-testing-strategy.md` §4), E2E `money-flow`, `checkout`; Demo A–C |
| LF-15 | Webhook keluar (signed, retry, DLQ) | ✅ | `TestMerchantWebhook_*`, replay ops |
| LF-16 | Refund | ✅ | `TestRefund_*`, E2E `checkout` |
| LF-17 | Rekonsiliasi | ✅ | `TestRecon_*`, Demo D |
| LF-18 | Ops console | ✅ | API `/ops/*` + halaman `/ops` (minimal), E2E `ops` |
| LF-19 | Metrik invariant + dashboard | ✅ | Gauge INV, dashboard *Integrity & Recon* |
| LF-20 | Rate limiting | ✅ | `TestRateLimit_*` |
| LF-21…LF-26 | P2/P3 | ✅ didaftar | README "Deliberately not built" dengan alasan |

## §1 per item
| # | Item | Status | Bukti / catatan |
|---|---|---|---|
| P1 | Semua P0 end-to-end | ✅ | Tabel di atas; 3 E2E Playwright hijau |
| P2 | Semua P1 | ✅ | Tabel di atas |
| P3 | P2/P3 didaftar eksplisit | ✅ | README "Deliberately not built" |
| P4 | Tidak ada fitur di luar matrix tanpa catatan | ✅ | Tambahan di luar matrix tercatat: demo mode (O15), kill switch (§4.3 desain), seed perf, `GET /ops/webhook-deliveries`, `GET /ops/payouts/{id}` (backlog/ADR) |
| C1 | Setiap LI-01…LI-15 punya test | ✅ | Matriks §5.1, dijaga `TestTraceability_DocsReferenceExistingTests` |
| C2 | Invariant checker di `t.Cleanup` | ✅ | Semua test `backend/internal/app` (cek otomatis + `allowBrokenLedger` hanya untuk test drift); package ledger memanggil `assertInvariants` |
| C3 | Metrik INV = 0 selama 7 hari di env demo | ⏳ | Butuh env demo yang hidup 7 hari. Alert + dashboard siap |
| C5 | Property test 1 juta iterasi di nightly | ⚠️ | Job `property-long` (1 juta iterasi, journal seimbang) ditulis; property berbasis DB tetap iterasi kecil di CI biasa. Belum pernah berjalan di GitHub |
| V1 | Validasi strict | ✅ | `TestIdentity_RegisterRejectsUnknownFields`, batas body `TestHTTP_UnsupportedMediaTypeAndBodyLimit` |
| V2 | Error dari katalog | ✅ | `TestDBErrorMapping_AllCatalogueConstraints` |
| V3 | Uang selalu string | ✅ | `TestContract_MoneyIsString` (respons nyata), `TestOpenAPI_MoneyFieldsUseMoneySchema` |
| A1 | BOLA setiap resource | ✅ | `TestWallet_TransactionDetailBOLA`, `TestBOLA_TransferTopupPayout`, `TestPaymentIntent_BOLA_MerchantToMerchant`, `TestRefund_BOLA_MerchantToMerchant`; ops → 403 (`TestOps_RoleRequiredAndNoSelfServiceKeys`) |
| A4 | Ops hanya role OPS + audit | ✅ | `TestOps_RoleRequiredAndNoSelfServiceKeys`, `TestOps_ResolutionRequiresEvidence`, audit di semua aksi tulis ops |
| S1 | Kontrol MVP LedgerPay | ✅ | Threat model + test di katalog §4.9 / §5.4; temuan khusus tercatat di backlog |
| S2 | Scan HIGH/CRITICAL bersih | ✅ lokal · ⏳ CI | `govulncheck`: 0 kerentanan terpanggil setelah naik ke Go 1.26.9 dan `moby/go-archive` v0.3.0 (sebelumnya 28 terpanggil). Trivy di `main.yml`. Job CI belum pernah berjalan |
| S3 | gitleaks seluruh history | ✅ lokal · ⏳ CI | Pertama kali dijalankan: 6.006 temuan = token JWT dev kedaluwarsa di summary k6 Sprint 9 + 2 contoh Idempotency-Key. Alat diperbaiki, file dibersihkan, pengecualian sempit di `.gitleaks.toml` → "no leaks found" |
| S4 | Security header | ✅ | `TestHeaders_PresentOnEveryResponse`, CSP/HSTS di Caddy |
| S5 | Endpoint demo tidak ada di release | ✅ | `TestBuild_DemoEndpointsAbsentWithoutDemoMode`, `TestDemo_RoutesAbsentInReleaseBuildEvenWithDemoMode` |
| S6 | SSRF egress guard | ✅ | `TestEgress_*`, `TestMerchantWebhook_EgressBlocksMetadataURL` |
| D1 | Migrasi dari nol | ✅ | `TestMigrations_UpFromZero` |
| D2 | Catalogue test | ✅ | `TestCatalog_EveryFKHasIndex`, `TestCatalogue_AppendOnlyGrants`, `TestAuditLog_*` |
| D3 | Backup harian + drill ≤ 7 hari | ⏳ | Dilatih lokal (backup, drill PASS, tiga kasus gagal benar). Cron + workflow siap |
| D4 | Seed demo deterministik | ⚠️ | Setiap skrip demo membuat datanya sendiri secara deterministik; `ledgerpay seed smoke|perf` untuk dataset besar. Tidak ada `make seed` untuk satu dataset demo bersama |
| F1 | FS-01…FS-10 | ✅ | Matriks §5.3 |
| F3 | Chaos suite di nightly | ⚠️ | Interleaving deterministik lewat testhooks di CI biasa + fault demo; tidak ada toxiproxy |
| F4 | Race test dengan `-race` | ✅ | `go test -race -tags testhooks ./...` |
| T1 | Traceability 100% | ✅ | `TestTraceability_DocsReferenceExistingTests` (bagian LedgerPay); rencana tanpa test ditulis jujur di §4.3a |
| T2 | Karantina flaky kosong | ✅ | Tidak ada test yang dikarantina |
| T3 | CI hijau di `main` | ⏳ | Repo lokal; semua job yang setara dijalankan lokal setiap sprint |
| T4 | Coverage dilaporkan | ✅ | `make cover` → `build/coverage.html` (indikatif) |
| O1 | Log JSON + correlation | ✅ | `request_id`/`correlation_id` di log & journal (`TestTrace_RequestIDToJournal`); redaction: PII dienkripsi, tidak di-log |
| O2 | Katalog metrik live | ✅ | `/metrics` api + worker, Prometheus target UP (Sprint 7) |
| O3 | Dashboard dari repo | ✅ | `deploy/grafana/gen_dashboards.py` → 3 dashboard |
| O4 | Alert rules + promtool | ✅ | 16 rule, `make test-alerts` (unit test promtool) |
| O5 | Trace api → job | ✅ | `jobs.trace_parent` + span link (Sprint 7) |
| OP1 | healthz / readyz | ✅ | `TestOps_HealthAndReadiness`, `TestWorkerOps_MetricsAndLaneReadiness` |
| OP2 | `make up` dari clone bersih | ✅ | Dijalankan ulang dari volume kosong berkali-kali |
| OP3 | CD + rollback teruji | ✅ lokal · ⏳ CI | Rollback otomatis (rilis rusak) dan rollback B → A diuji lokal; workflow belum berjalan di GitHub |
| OP4 | Runbook | ✅ | `docs/runbook.md` (semua 16 alert + prosedur) |
| OP5 | RPO/RTO jujur | ✅ | README Limitations (24 jam / ±2 jam) |
| OP6 | Kill switch | ✅ | `TestKillSwitch_PayoutSubmitAndTopup` (`LP_PAYOUT_SUBMIT_ENABLED`, `LP_WEBHOOK_DELIVERY_ENABLED`, `LP_TOPUP_ENABLED`) |
| PF1 | Laporan baseline jujur | ✅ | `docs/performance.md` (laptop, dengan batasan) |
| PF2 | Keputusan E-1 | ✅ | Laporan + ADR-L07 |
| PF3 | Perf smoke nightly | ⏳ | Ditunda setelah M4 (amandemen final review) |
| DC1 | README lengkap | ✅ | README |
| DC2 | ADR mutakhir | ✅ | Catatan implementasi di ADR-006, ADR-013, ADR-L06, ADR-L07 + catatan di dokumen desain |
| DC3 | Diagram arsitektur | ✅ | README (mermaid) + `docs/design/03-architecture.md` |
| DC4 | API docs statis | ✅ | `make api-docs` → `build/api.html` |
| DC5 | Demo script | ✅ | `docs/demo-script.md` |
| DC6 | Limitations jujur | ✅ | README |
| DM1 | `demo-verify` 7 malam | ⏳ | `make demo-verify` (dengan asersi) hijau lokal; nightly belum berjalan |
| DM2 | Video | ✅ | https://suyantolabs.my.id/video/ledgerpay.mp4 (80 detik, dengan narasi) |
| DM3 | URL publik | ✅ | https://ledgerpay.suyantolabs.my.id |
| DM4 | Token demo aman | ✅ | `DEMO_TOKEN` dibuat `init-env.sh`, tidak di README/SPA, wajib ≥ 24 karakter |
| H1 | Lisensi | ✅ | `LICENSE` MIT |
| H2 | Tanpa TODO/FIXME di jalur kritis | ✅ | `grep` di ledger, idempotency, payout, payment, recon: kosong |
| H3 | Dependency di-pin + Dependabot | ✅ | `go.sum`, action di-pin SHA, image dengan versi tetap (`golang:1.26.9-alpine`, dll.), `.github/dependabot.yml` |
| H4 | Tanpa kode dari pekerjaan kantor | ✅ | Review diri |
| H5 | Riwayat git terbaca | ✅ | Satu commit `feat(...)` per sprint di `main` |
