#!/usr/bin/env bash
# Backup harian DB ledgerpay (§5.2): pg_dump -Fc → age (dua recipient) → disk lokal → offsite.
#
#   pg_dump berjalan dalam SATU transaksi REPEATABLE READ → snapshot konsisten (journal dan saldo
#   dari titik waktu yang sama). Role ledgerpay_backup hanya punya pg_read_all_data.
#
# Env: PGHOST PGUSER PGPASSWORD [PGDATABASE=ledgerpay]
#      LP_BACKUP_AGE_RECIPIENTS  public key age, dipisah spasi (operator offline + restore drill)
#      [LP_BACKUP_DIR=/backups] [LP_BACKUP_REMOTE=offsite:bucket/ledgerpay]  (kosong = lokal saja)
#      [LP_PUSHGATEWAY_URL=http://pushgateway:9091]  metrik app_backup_* untuk alert BackupStale
set -euo pipefail
: "${LP_BACKUP_AGE_RECIPIENTS:?public key age wajib (backup TIDAK pernah ditulis tanpa enkripsi)}"
DB=${PGDATABASE:-ledgerpay}
DIR=${LP_BACKUP_DIR:-/backups}
MIN_BYTES=${LP_BACKUP_MIN_BYTES:-1024}
log() { printf '[backup %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

mkdir -p "$DIR/daily" "$DIR/weekly"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out="$DIR/daily/$DB-$stamp.dump.age"
recipients=()
for r in $LP_BACKUP_AGE_RECIPIENTS; do recipients+=(-r "$r"); done
start=$(date +%s)

log "dump $DB → $(basename "$out")"
# pipefail: pg_dump yang gagal di tengah menggagalkan seluruh pipeline (tidak ada file "setengah").
pg_dump -Fc --schema=ledgerpay -d "$DB" | age "${recipients[@]}" -o "$out.partial"
size=$(stat -c %s "$out.partial")
if [ "$size" -lt "$MIN_BYTES" ]; then # dump kosong/terpotong = gagal, bukan "sukses kecil"
  rm -f "$out.partial"
  log "FAILED: dump only $size bytes (< $MIN_BYTES)"
  exit 1
fi
mv "$out.partial" "$out"
(cd "$DIR/daily" && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")   # diverifikasi saat restore
if [ "$(date -u +%u)" = 7 ]; then cp "$out" "$out.sha256" "$DIR/weekly/"; fi      # Minggu → weekly

# Retensi lokal (offsite memakai lifecycle rule bucket: daily 7 hari, weekly 28 hari).
find "$DIR/daily" -name '*.dump.age*' -mtime +7 -delete
find "$DIR/weekly" -name '*.dump.age*' -mtime +28 -delete

if [ -n "${LP_BACKUP_REMOTE:-}" ]; then
  log "upload → $LP_BACKUP_REMOTE"
  rclone copy "$DIR" "$LP_BACKUP_REMOTE" --include '*.dump.age' --include '*.dump.age.sha256' --immutable --no-traverse
else
  log "WARNING: LP_BACKUP_REMOTE kosong → backup HANYA di disk VM (bukan offsite)"
fi

dur=$(( $(date +%s) - start ))
log "OK $size bytes in ${dur}s"
if [ -n "${LP_PUSHGATEWAY_URL:-}" ]; then
  # Batch job tidak bisa di-scrape → push (Pushgateway). Timestamp sukses dipakai alert BackupStale.
  curl -fsS --max-time 10 --data-binary @- "$LP_PUSHGATEWAY_URL/metrics/job/backup/db/$DB" <<M || log "WARNING: push metrics failed"
# TYPE app_backup_last_success_timestamp_seconds gauge
app_backup_last_success_timestamp_seconds $(date +%s)
# TYPE app_backup_size_bytes gauge
app_backup_size_bytes $size
# TYPE app_backup_duration_seconds gauge
app_backup_duration_seconds $dur
M
fi
