//go:build demo

package app

// "Periksa buku besar" untuk pengunjung sandbox: invariant LI-01 dan LI-03 dihitung ulang untuk wallet
// pengunjung saja (bukan ledger.Verify global, yang memindai seluruh database dan dijalankan job tiap jam).
//   - saldo tersimpan (accounts.balance) = Σ kredit − Σ debit dari semua entry wallet ini;
//   - setiap journal yang menyentuh wallet ini seimbang: Σ debit = Σ kredit di SEMUA akunnya.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
)

func (s *sandboxer) walletIntegrity(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var inSandbox bool
	if err := s.d.Pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM demo_sandboxes WHERE user_id = $1 AND expired_at IS NULL)`, p.UserID).Scan(&inSandbox); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !inSandbox {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	var (
		stored, computed       int64
		entries, journals, bad int
	)
	// Satu snapshot (REPEATABLE READ): saldo dan entry dibaca pada titik waktu yang sama.
	err := db.WithTxOptions(r.Context(), s.d.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		wl, err := s.w.sv.ledger.WalletOf(r.Context(), tx, p.UserID)
		if err != nil {
			return err
		}
		stored = wl.Balance
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(sum(CASE direction WHEN 'C' THEN amount ELSE -amount END), 0)::bigint, count(*)::int
			FROM journal_entries WHERE account_id = $1`, wl.AccountID).Scan(&computed, &entries); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT count(*)::int, count(*) FILTER (WHERE d <> c)::int FROM (
			SELECT sum(amount) FILTER (WHERE direction = 'D') AS d, sum(amount) FILTER (WHERE direction = 'C') AS c
			FROM journal_entries WHERE journal_id IN (SELECT journal_id FROM journal_entries WHERE account_id = $1)
			GROUP BY journal_id) j`, wl.AccountID).Scan(&journals, &bad)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"stored_balance": ledger.FormatAmount(stored), "computed_balance": ledger.FormatAmount(computed),
		"entries": entries, "journals": journals, "unbalanced_journals": bad,
		"ok": stored == computed && bad == 0,
	})
}
