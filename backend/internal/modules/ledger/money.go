package ledger

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Money adalah nominal dalam minor unit (ADR-L04): bigint, TIDAK PERNAH float.
// IDR mengikuti ISO 4217 exponent 2, jadi "5000000" = Rp50.000,00.
type Money struct {
	Amount   int64
	Currency string
}

// SupportedCurrency: V1 single currency.
const SupportedCurrency = "IDR"

var (
	ErrInvalidAmount       = errors.New("ledger: amount must be a positive integer of minor units")
	ErrUnsupportedCurrency = errors.New("ledger: unsupported currency")
	ErrOverflow            = errors.New("ledger: amount overflow")
)

// ParseAmount membaca string minor unit dari API ("5000000") menjadi int64 positif.
// API mengirim uang sebagai string karena number JavaScript kehilangan presisi di atas 2^53.
func ParseAmount(s string) (int64, error) {
	if s == "" || len(s) > 18 {
		return 0, ErrInvalidAmount
	}
	for _, r := range s {
		if r < '0' || r > '9' { // tolak tanda, desimal, spasi, notasi ilmiah
			return 0, ErrInvalidAmount
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, ErrInvalidAmount
	}
	return v, nil
}

// addChecked menjumlahkan dengan deteksi overflow (saldo tidak boleh membungkus ke negatif).
func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// FormatAmount mengubah minor unit menjadi string untuk JSON.
func FormatAmount(v int64) string { return strconv.FormatInt(v, 10) }

func (m Money) String() string { return fmt.Sprintf("%s %d (minor)", m.Currency, m.Amount) }
