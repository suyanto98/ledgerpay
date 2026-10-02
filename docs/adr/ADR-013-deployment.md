# ADR-013 — Deployment: Docker Compose di Satu VM + CI/CD GitHub Actions

Status: Accepted · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay, PayRun

## Context / Problem

Project harus bisa diakses publik untuk demo, murah (biaya pribadi), bisa di-deploy ulang dengan satu perintah, dan menunjukkan praktik production: image immutable, migrasi aman, health check, dan rollback. Kandidat bekerja solo, jadi operasional harus minimal.

## Options

| Opsi | Pros | Cons |
|---|---|---|
| **Docker Compose di satu VM** + reverse proxy | Murah; environment sama dengan lokal; mudah dijelaskan | Single point of failure; downtime singkat saat restart; scaling manual |
| PaaS (Fly.io, Render, Railway) | Tanpa urus server; deploy dari git | Kontrol lebih sedikit (worker, Postgres extension); biaya naik dengan beberapa service; kurang menunjukkan skill ops |
| Kubernetes (k3s / managed) | Menunjukkan skill k8s; rolling update, self-healing | Berlebihan; biaya dan waktu besar; kompleksitas tanpa kebutuhan |
| Serverless (Cloud Run + managed PG) | Scale to zero | Worker long-running dan `SKIP LOCKED` polling kurang cocok; cold start |

## Decision

**Docker Compose di satu VM** per lingkungan demo. Penyedia hosting dipilih di Step 9 (cost).

| Aspek | Keputusan |
|---|---|
| Image | Multi-stage build → **distroless static, non-root**; satu image per project, dijalankan sebagai `api` / `worker` / `migrate`; tag = **git SHA** (immutable); `latest` tidak dipakai untuk deploy |
| Proxy | **Caddy** (TLS otomatis Let's Encrypt), menyajikan SPA statis + reverse proxy ke `api` |
| Registry | GHCR |
| Secrets | File `.env` di server dengan permission 600, tidak di-commit; CI secrets untuk SSH/registry |
| Migrasi | Dijalankan sebagai langkah deploy tersendiri (`<app> migrate up`) **sebelum** restart `api`; wajib expand/contract (ADR-005) |
| Health | `/healthz` (liveness: proses hidup) dan `/readyz` (readiness: DB dapat dijangkau, migrasi versi sesuai) |
| Rollback | Deploy ulang SHA sebelumnya. Karena migrasi expand/contract, kode lama kompatibel dengan skema baru. Down-migration tidak dipakai di production |

Pipeline:

```text
push/PR ─▶ lint (golangci-lint, eslint) ─▶ unit test (-race) ─▶ integration test (testcontainers)
        ─▶ sqlc/openapi generate diff check ─▶ build image ─▶ (main) push GHCR :<sha>
        ─▶ deploy (SSH): docker compose pull ─▶ migrate up ─▶ up -d api worker
        ─▶ health gate: /readyz 200 dalam 60s? ── tidak ──▶ rollback ke SHA sebelumnya + gagal pipeline
                                               └─ ya ──▶ smoke test (login + 1 transaksi demo)
```

Contoh expand/contract (rename `wallets.name` → `wallets.label`):
1. Rilis A: tambah `label` nullable; kode menulis ke keduanya, membaca `name`.
2. Backfill `label = name` (job batch).
3. Rilis B: kode membaca `label`.
4. Rilis C: hapus `name`.

## Reason

- Satu VM + Compose cukup untuk demo dan membuat environment lokal = environment deploy (`make up`).
- Image immutable + tag SHA membuat rollback deterministik.
- Health gate + rollback otomatis menunjukkan praktik deployment yang aman tanpa orkestrator.
- **Kubernetes sengaja hanya V3**: tidak ada masalah di V1/V2 yang membutuhkannya. Membahasnya di arsitektur V3 sudah cukup untuk diskusi interview.

## Trade-offs

- Single point of failure; downtime beberapa detik saat restart `api` (diterima untuk demo, dibahas sebagai keterbatasan di README).
- Postgres di VM yang sama: backup harus diatur sendiri (`pg_dump` terjadwal ke object storage, uji restore). Opsi managed Postgres dibandingkan di Step 9.
- Deploy via SSH lebih rapuh dibanding platform terkelola.

## Future Migration

| Trigger | Perubahan |
|---|---|
| Butuh zero-downtime deploy | Dua instance `api` di belakang Caddy + rolling restart, atau blue/green dengan dua stack Compose |
| Data penting / butuh PITR | Managed Postgres dengan point-in-time recovery |
| Banyak service + kebutuhan autoscaling/self-healing | Kubernetes (managed) dengan Helm/Kustomize, HPA untuk worker — **V3** |
| Secret rotation & audit | Secret manager (Vault / cloud secret manager) |

## Interview angle

- *"Bagaimana kamu rollback jika migrasi sudah jalan?"* → Expand/contract: skema baru kompatibel dengan kode lama, jadi rollback = deploy SHA lama tanpa down-migration.
- *"Kenapa tidak pakai Kubernetes?"* → Tidak ada kebutuhan yang memerlukannya. Sebutkan trigger konkret kapan akan memakainya.
- *"Bagaimana kamu tahu deploy berhasil?"* → Readiness gate + smoke test + rollback otomatis.

## Related

- [ADR-005](ADR-005-data-access-sqlc.md) · [ADR-001](ADR-001-modular-monolith.md) · [ADR-004](ADR-004-postgresql.md)
