//go:build demo

package faults

import (
	"context"
	"log/slog"
	"os"

	"ledgerpay/internal/platform/db"
)

// exit dapat diganti test (proses test tidak boleh benar-benar mati).
var exit = os.Exit

// Check: kalau ada fault yang masih tersisa untuk point ini, kurangi sisanya (commit seketika lewat
// q non-transaksi) lalu matikan proses TANPA defer/graceful shutdown, persis seperti kill -9.
// Error membaca tabel diabaikan: titik demo tidak boleh membuat jalur uang gagal.
func Check(ctx context.Context, q db.DBTX, point string) {
	var id int64
	err := q.QueryRow(ctx, `UPDATE demo_faults SET remaining = remaining - 1, fired_at = now()
		WHERE id = (SELECT id FROM demo_faults WHERE point = $1 AND remaining > 0 ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`, point).Scan(&id)
	if err != nil {
		return
	}
	slog.Default().Warn("demo.fault_fired", "point", point, "fault_id", id) // log terakhir sebelum mati
	exit(137)
}

// Enabled melaporkan apakah binary ini dibangun dengan tag `demo`.
func Enabled() bool { return true }

// SetExitForTest mengganti fungsi exit (hanya test build demo).
func SetExitForTest(fn func(int)) (restore func()) {
	old := exit
	exit = fn
	return func() { exit = old }
}
