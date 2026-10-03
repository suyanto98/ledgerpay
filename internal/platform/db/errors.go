package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ledgerpay/internal/platform/apperr"
)

// constraintErrors memetakan nama constraint (atau unique index) → error domain.
// Nama constraint eksplisit dan stabil adalah kontrak (konvensi Step 4; katalog error §4).
// Constraint yang pelanggarannya berarti BUG (append-only, journal tidak seimbang) sengaja
// TIDAK dipetakan: ia jatuh ke INTERNAL_ERROR dan harus berbunyi di log/alert.
// Test TestDBErrorMapping_AllCatalogueConstraints memastikan setiap nama di sini benar-benar ada di DB.
var constraintErrors = map[string]*apperr.Error{
	"uq_users_email_lower": apperr.ErrEmailRegistered,
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
