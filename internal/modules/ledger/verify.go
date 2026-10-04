package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger/store"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/metrics"
)

// Drift adalah akun yang saldo materialized-nya berbeda dari Σ entries (LI-03).
type Drift struct {
	AccountID string
	Stored    int64
	Computed  int64
}

// Report adalah hasil invariant checker. Kosong = sehat.
type Report struct {
	BalanceDrift       []Drift
	UnbalancedJournals []string
	GlobalImbalance    int64 // Σ debit − Σ kredit seluruh ledger (minor unit); normal 0
}

func (r Report) OK() bool {
	return len(r.BalanceDrift) == 0 && len(r.UnbalancedJournals) == 0 && r.GlobalImbalance == 0
}

func (r Report) String() string {
	return fmt.Sprintf("balance drift: %v; unbalanced journals: %v; global imbalance: %d", r.BalanceDrift, r.UnbalancedJournals, r.GlobalImbalance)
}

// Verify menjalankan invariant checker dalam SATU snapshot (Repeatable Read, read only), supaya
// saldo dan entries dibaca pada titik waktu yang sama. Dipakai test (setiap test ledger) dan
// nanti job terjadwal `ledger.verify` yang mengisi metrik invariant (docs/design/21-observability.md).
func Verify(ctx context.Context, b db.Beginner) (Report, error) {
	var rep Report
	err := db.WithTxOptions(ctx, b, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		q := store.New(tx)
		drift, err := q.BalanceDrift(ctx)
		if err != nil {
			return err
		}
		for _, d := range drift {
			var stored int64
			if d.Balance != nil {
				stored = *d.Balance
			}
			rep.BalanceDrift = append(rep.BalanceDrift, Drift{AccountID: d.ID.String(), Stored: stored, Computed: d.Computed})
		}
		unbalanced, err := q.UnbalancedJournals(ctx)
		if err != nil {
			return err
		}
		for _, j := range unbalanced {
			rep.UnbalancedJournals = append(rep.UnbalancedJournals, j.String())
		}
		rep.GlobalImbalance, err = q.GlobalImbalance(ctx)
		return err
	})
	return rep, err
}

// Publish mengisi gauge INVARIANT dari laporan verifikasi (job ledger.verify). Gauge diperbarui
// SETIAP verifikasi (termasuk kembali ke 0), dan timestamp sukses memungkinkan alert "verifikasi basi".
func (r Report) Publish(now float64) {
	metrics.LedgerDriftAccounts.WithLabelValues().Set(float64(len(r.BalanceDrift)))
	metrics.LedgerUnbalanced.WithLabelValues().Set(float64(len(r.UnbalancedJournals)))
	metrics.LedgerGlobalImbalance.WithLabelValues().Set(float64(r.GlobalImbalance))
	metrics.LedgerVerifiedAt.WithLabelValues().Set(now)
}
