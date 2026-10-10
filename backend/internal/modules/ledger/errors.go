package ledger

import "ledgerpay/internal/platform/apperr"

// Alias error domain ledger. Definisinya di apperr supaya pemetaan constraint di platform/db
// bisa memakainya tanpa melanggar batas modul.
var (
	ErrInsufficientBalance = apperr.ErrInsufficientBalance
	ErrAccountFrozen       = apperr.ErrAccountFrozen
	ErrAlreadyReversed     = apperr.ErrAlreadyReversed
)
