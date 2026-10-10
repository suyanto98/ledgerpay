// Package recon: rekonsiliasi harian catatan LedgerPay vs laporan settlement provider (LF-17, Demo D).
//
//	file settlement (CSV) ──Parse──▶ []External ┐
//	top-up/payout internal ─────────────────────┼─Classify──▶ []Item (MATCHED / MISSING_* / *_MISMATCH)
//	objek yang "seharusnya ada" di file ────────┘
//
// Classify adalah fungsi MURNI (tanpa DB, tanpa jam): semua kategori diuji sebagai tabel
// (TestRecon_ClassifiesAllCategories). Service hanya memuat input dan menyimpan hasilnya.
package recon

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Kategori hasil matching (docs/design/09-db-schema.md §15).
const (
	Matched         = "MATCHED"
	MissingInternal = "MISSING_INTERNAL" // ada di provider, tidak ada di LedgerPay
	MissingExternal = "MISSING_EXTERNAL" // ada di LedgerPay, tidak ada di provider
	AmountMismatch  = "AMOUNT_MISMATCH"
	StatusMismatch  = "STATUS_MISMATCH"
)

// Jenis objek.
const (
	Topup  = "topup"
	Payout = "payout"
)

// CutoffTolerance: objek internal yang final ≤ 15 menit sebelum tengah malam boleh muncul di file
// HARI BERIKUTNYA (kontrak §1.6). Objek itu tidak diharapkan ada di file hari ini, jadi tidak pernah
// ditandai MISSING_EXTERNAL karena perbedaan jam; kalau muncul, tetap di-match lewat referensinya.
const CutoffTolerance = 15 * time.Minute

// Header file settlement, urutan tetap.
var header = []string{"business_date", "type", "provider_ref", "reference", "amount", "currency", "status", "settled_at", "fee"}

// External adalah satu baris file settlement.
type External struct {
	Line        int
	Type        string // topup | payout
	ProviderRef string
	Reference   string // ID LedgerPay (kunci matching)
	Amount      int64
	Currency    string
	Status      string // SUCCEEDED | FAILED
	SettledAt   time.Time
}

// Internal adalah top-up/payout LedgerPay yang relevan untuk satu run.
type Internal struct {
	Type     string
	ID       uuid.UUID
	Amount   int64
	Status   string
	Currency string
}

// Item adalah hasil matching satu objek.
type Item struct {
	Category       string
	Type           string
	InternalID     *uuid.UUID
	ExternalRef    *string
	ProviderRef    *string
	InternalAmount *int64
	ExternalAmount *int64
	InternalStatus *string
	ExternalStatus *string
	Currency       string
}

// ErrMalformed: file tidak bisa dipercaya sebagai laporan settlement (run FAILED, bukan item mismatch).
var ErrMalformed = errors.New("recon: malformed settlement file")

// Parse membaca file settlement untuk businessDate. Header harus persis; setiap baris divalidasi.
// Referensi ganda = file rusak (satu transaksi tidak boleh settle dua kali): seluruh file ditolak.
func Parse(raw []byte, businessDate string) ([]External, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = len(header)
	got, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: header: %w", ErrMalformed, err)
	}
	if !slices.Equal(got, header) {
		return nil, fmt.Errorf("%w: unexpected header %v", ErrMalformed, got)
	}
	var out []External
	seen := map[string]int{}
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformed, line, err)
		}
		e, err := parseRow(rec, businessDate)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformed, line, err)
		}
		e.Line = line
		key := e.Type + ":" + e.Reference
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: line %d: reference %s already on line %d", ErrMalformed, line, e.Reference, first)
		}
		seen[key] = line
		out = append(out, e)
	}
}

func parseRow(rec []string, businessDate string) (External, error) {
	if rec[0] != businessDate {
		return External{}, fmt.Errorf("business_date %q, want %q", rec[0], businessDate)
	}
	e := External{ProviderRef: rec[2], Reference: rec[3], Currency: rec[5], Status: rec[6]}
	switch rec[1] {
	case "CHARGE":
		e.Type = Topup
	case "PAYOUT":
		e.Type = Payout
	default:
		return External{}, fmt.Errorf("unknown type %q", rec[1])
	}
	amount, err := strconv.ParseInt(rec[4], 10, 64)
	if err != nil || amount <= 0 {
		return External{}, fmt.Errorf("invalid amount %q", rec[4])
	}
	e.Amount = amount
	if e.Status != "SUCCEEDED" && e.Status != "FAILED" {
		return External{}, fmt.Errorf("invalid status %q", e.Status)
	}
	if e.SettledAt, err = time.Parse(time.RFC3339, rec[7]); err != nil {
		return External{}, fmt.Errorf("invalid settled_at %q", rec[7])
	}
	if e.Reference == "" || e.ProviderRef == "" {
		return External{}, errors.New("empty reference")
	}
	return e, nil
}

// Classify mencocokkan baris file dengan objek internal.
//   - internal: objek yang dirujuk file (dicari per referensi, tanpa batas tanggal) + objek yang
//     seharusnya ada di file (expected). Kunci: (type, id).
//   - expected: objek yang final pada business_date (di luar jendela cut-off), sehingga ketiadaannya
//     di file adalah MISSING_EXTERNAL.
//
// Urutan cek per baris: dikenal? → nominal sama? → status cocok? Nominal lebih dulu karena selisih
// uang lebih serius daripada selisih status.
func Classify(ext []External, internal map[Key]Internal, expected []Key) []Item {
	items := make([]Item, 0, len(ext)+len(expected))
	seen := map[Key]bool{}
	for _, e := range ext {
		it := Item{Type: e.Type, ExternalRef: ptr(e.Reference), ProviderRef: ptr(e.ProviderRef),
			ExternalAmount: ptr(e.Amount), ExternalStatus: ptr(e.Status), Currency: e.Currency}
		id, err := uuid.Parse(e.Reference)
		in, known := internal[Key{e.Type, id}]
		if err != nil || !known {
			it.Category = MissingInternal
			items = append(items, it)
			continue
		}
		seen[Key{e.Type, id}] = true
		it.InternalID, it.InternalAmount, it.InternalStatus = ptr(in.ID), ptr(in.Amount), ptr(in.Status)
		switch {
		case in.Amount != e.Amount || in.Currency != e.Currency:
			it.Category = AmountMismatch
		case !statusAgrees(e.Type, in.Status, e.Status):
			it.Category = StatusMismatch
		default:
			it.Category = Matched
		}
		items = append(items, it)
	}
	for _, k := range expected {
		if seen[k] {
			continue
		}
		in := internal[k]
		items = append(items, Item{Category: MissingExternal, Type: k.Type, InternalID: ptr(in.ID),
			InternalAmount: ptr(in.Amount), InternalStatus: ptr(in.Status), Currency: in.Currency})
	}
	return items
}

// Key mengidentifikasi objek internal.
type Key struct {
	Type string
	ID   uuid.UUID
}

// statusAgrees: status final provider vs status LedgerPay. Payout UNKNOWN/SUBMITTED yang muncul di
// file sebagai SUCCEEDED adalah STATUS_MISMATCH: justru kasus yang dicari recon (ADR-L06, Demo D).
func statusAgrees(typ, internal, external string) bool {
	if typ == Topup {
		if external == "SUCCEEDED" {
			return internal == "SUCCEEDED"
		}
		return internal == "FAILED" || internal == "EXPIRED"
	}
	return internal == external // payout: SUCCEEDED↔SUCCEEDED, FAILED↔FAILED
}

func ptr[T any](v T) *T { return &v }
