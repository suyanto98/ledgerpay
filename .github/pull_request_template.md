## Apa & kenapa
<!-- Ringkas perubahan dan alasannya. Rujuk fitur (LF-xx), ADR, atau issue. -->

## Invariant yang tersentuh
<!-- LI-xx yang dipengaruhi, dan bagaimana tetap terjaga. Tulis "tidak ada" jika tidak relevan. -->

## Checklist (Definition of Done fitur — docs/design Step 10 `04` §3)
- [ ] OpenAPI diperbarui + contract test (jika endpoint berubah)
- [ ] Validasi + error code dari katalog (Step 5 `01`)
- [ ] Authorization policy + test (BOLA → 404)
- [ ] Idempotency di endpoint yang memindahkan uang
- [ ] Transaction boundary sesuai dokumen (Step 7); tidak ada network call di dalam tx
- [ ] Migrasi expand-only (contract di deploy terpisah)
- [ ] Unit + integration test; invariant checker lolos
- [ ] Log/metrik ditambahkan bila perlu
- [ ] Dokumen/ADR diperbarui jika perilaku berubah

## Cara menguji
<!-- Perintah yang dijalankan reviewer. -->
