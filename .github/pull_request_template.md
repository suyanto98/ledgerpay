## Apa & kenapa
<!-- Ringkas perubahan dan alasannya. Rujuk fitur (LF-xx), ADR, atau issue. -->

## Invariant yang tersentuh
<!-- LI-xx yang dipengaruhi, dan bagaimana tetap terjaga. Tulis "tidak ada" jika tidak relevan. -->

## Checklist (Definition of Done fitur — docs/design `28-definition-of-done.md` §3)
- [ ] OpenAPI diperbarui + contract test (jika endpoint berubah)
- [ ] Validasi + error code dari katalog (`12-api-error-catalogue.md`)
- [ ] Authorization policy + test (BOLA → 404)
- [ ] Idempotency di endpoint yang memindahkan uang
- [ ] Transaction boundary sesuai dokumen (desain transaksi & concurrency); tidak ada network call di dalam tx
- [ ] Migrasi expand-only (contract di deploy terpisah)
- [ ] Unit + integration test; invariant checker lolos
- [ ] Log/metrik ditambahkan bila perlu
- [ ] Dokumen/ADR diperbarui jika perilaku berubah

## Cara menguji
<!-- Perintah yang dijalankan reviewer. -->
