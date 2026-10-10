package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/metrics"
)

// constraintErrors memetakan nama constraint (atau unique index) → error domain.
// Nama constraint eksplisit dan stabil adalah kontrak (konvensi Step 4; katalog error §4).
// Constraint yang pelanggarannya berarti BUG (append-only, journal tidak seimbang) sengaja
// TIDAK dipetakan: ia jatuh ke INTERNAL_ERROR dan harus berbunyi di log/alert.
// Test TestDBErrorMapping_AllCatalogueConstraints memastikan setiap nama di sini benar-benar ada di DB.
var constraintErrors = map[string]*apperr.Error{
	"uq_users_email_lower":             apperr.ErrEmailRegistered,
	"ck_accounts_balance_non_negative": apperr.ErrInsufficientBalance, // jaring terakhir LI-02
	"uq_journals_reversal_of":          apperr.ErrAlreadyReversed,     // LI-04
	"uq_merchant_api_keys_active_slot": apperr.ErrAPIKeyLimitReached,  // dua key aktif (C-42)
	// Jalur normal menangani duplikat order di modul payment (agar existing_resource_id ikut);
	// pemetaan ini jaring terakhir.
	"uq_payment_intents_merchant_order": apperr.ErrDuplicateMerchantOrder, // LI-13
	"ck_payment_intents_refund_bound":   apperr.ErrRefundExceedsPayment,   // LI-08 (guard aplikasi lebih dulu)
	// Sengaja TIDAK dipetakan (bug → 500 + alert): ck_journals_balanced, uq_journals_source,
	// trigger append-only. Pelanggaran ini berarti kode aplikasi salah, bukan input user salah.
}

// ConstraintNames mengembalikan nama constraint yang dipetakan (dipakai test katalog).
func ConstraintNames() []string {
	names := make([]string, 0, len(constraintErrors))
	for n := range constraintErrors {
		names = append(names, n)
	}
	return names
}

// MapError mengubah error Postgres menjadi error domain. Error yang tidak dikenal dikembalikan
// apa adanya; lapisan HTTP akan menjadikannya 500 generik (pesan Postgres tidak bocor ke client).
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.ErrUnavailable.WithCause(err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if e, ok := constraintErrors[pgErr.ConstraintName]; ok {
		return e.WithCause(err)
	}
	switch pgErr.Code {
	case "55P03": // lock_not_available (lock_timeout)
		metrics.DBLockTimeouts.WithLabelValues("resource").Inc()
		return apperr.ErrResourceBusy.WithCause(err)
	case "40001", "40P01": // retry WithTx sudah habis
		return apperr.ErrResourceBusy.WithCause(err)
	case "57014": // query_canceled (statement_timeout)
		return apperr.ErrUnavailable.WithCause(err)
	case "23503": // FK: referensi tidak ada; tidak membedakan "tidak ada" vs "bukan milikmu"
		return apperr.ErrNotFound.WithCause(err)
	}
	return err
}

// IsUniqueViolation melaporkan apakah err adalah unique violation pada constraint/index bernama itu.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
