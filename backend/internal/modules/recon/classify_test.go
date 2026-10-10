package recon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func row(typ, providerRef, ref, amount, status string) string {
	return strings.Join([]string{"2026-10-05", typ, providerRef, ref, amount, "IDR", status, "2026-10-05T03:00:00Z", "0"}, ",")
}

const csvHeader = "business_date,type,provider_ref,reference,amount,currency,status,settled_at,fee"

// TestRecon_ClassifiesAllCategories (LF-17 AC2): setiap kategori dari satu file + satu set data internal.
func TestRecon_ClassifiesAllCategories(t *testing.T) {
	ids := map[string]uuid.UUID{}
	for _, n := range []string{"ok_topup", "ok_payout", "amount", "status_payout", "status_topup", "missing_ext", "failed_payout"} {
		ids[n] = uuid.Must(uuid.NewV7())
	}
	internal := map[Key]Internal{}
	add := func(typ, name string, amount int64, status string) {
		internal[Key{typ, ids[name]}] = Internal{Type: typ, ID: ids[name], Amount: amount, Status: status, Currency: "IDR"}
	}
	add(Topup, "ok_topup", 10_000_000, "SUCCEEDED")
	add(Payout, "ok_payout", 2_000_000, "SUCCEEDED")
	add(Topup, "amount", 5_000_000, "SUCCEEDED")
	add(Payout, "status_payout", 3_000_000, "UNKNOWN") // bank sudah mengirim, LedgerPay belum tahu (Demo D)
	add(Topup, "status_topup", 4_000_000, "EXPIRED")   // provider menerima uang setelah top-up kedaluwarsa
	add(Payout, "missing_ext", 1_000_000, "SUCCEEDED") // LedgerPay settle, bank tidak mencatat
	add(Payout, "failed_payout", 1_500_000, "FAILED")

	file := strings.Join([]string{csvHeader,
		row("CHARGE", "ch_1", ids["ok_topup"].String(), "10000000", "SUCCEEDED"),
		row("PAYOUT", "po_1", ids["ok_payout"].String(), "2000000", "SUCCEEDED"),
		row("CHARGE", "ch_2", ids["amount"].String(), "5000100", "SUCCEEDED"),
		row("PAYOUT", "po_2", ids["status_payout"].String(), "3000000", "SUCCEEDED"),
		row("CHARGE", "ch_3", ids["status_topup"].String(), "4000000", "SUCCEEDED"),
		row("PAYOUT", "po_3", ids["failed_payout"].String(), "1500000", "FAILED"),
		row("CHARGE", "ch_x", uuid.NewString(), "700000", "SUCCEEDED"), // tidak dikenal LedgerPay
		row("CHARGE", "ch_y", "not-a-uuid", "700000", "SUCCEEDED"),     // referensi rusak = tidak dikenal
	}, "\n") + "\n"
	ext, err := Parse([]byte(file), "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	expected := []Key{{Topup, ids["ok_topup"]}, {Payout, ids["ok_payout"]}, {Payout, ids["missing_ext"]}}
	items := Classify(ext, internal, expected)

	got := map[string]string{} // nama → kategori
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Category]++
		if it.InternalID == nil {
			continue
		}
		for n, id := range ids {
			if id == *it.InternalID {
				got[n] = it.Category
			}
		}
	}
	want := map[string]string{
		"ok_topup": Matched, "ok_payout": Matched, "failed_payout": Matched, "amount": AmountMismatch,
		"status_payout": StatusMismatch, "status_topup": StatusMismatch, "missing_ext": MissingExternal,
	}
	for n, c := range want {
		if got[n] != c {
			t.Errorf("%s: category %q, want %q", n, got[n], c)
		}
	}
	if counts[MissingInternal] != 2 || len(items) != 9 {
		t.Errorf("want 2 MISSING_INTERNAL and 9 items, got %v (%d items)", counts, len(items))
	}
	for _, it := range items {
		if it.Category == MissingExternal && it.ExternalRef != nil {
			t.Error("MISSING_EXTERNAL must not carry an external reference (ck_recon_items_refs)")
		}
		if it.Category == MissingInternal && it.InternalID != nil {
			t.Error("MISSING_INTERNAL must not carry an internal id (ck_recon_items_refs)")
		}
	}
}

// File yang tidak bisa dipercaya ditolak utuh (run FAILED), bukan diterjemahkan jadi item mismatch.
func TestRecon_ParseRejectsMalformedFiles(t *testing.T) {
	id := uuid.NewString()
	cases := map[string]string{
		"wrong header":        "date,type\n",
		"duplicate reference": csvHeader + "\n" + row("CHARGE", "ch_1", id, "1000", "SUCCEEDED") + "\n" + row("CHARGE", "ch_2", id, "1000", "SUCCEEDED") + "\n",
		"other business date": csvHeader + "\n" + strings.Replace(row("CHARGE", "ch_1", id, "1000", "SUCCEEDED"), "2026-10-05", "2026-10-04", 1) + "\n",
		"negative amount":     csvHeader + "\n" + row("CHARGE", "ch_1", id, "-5", "SUCCEEDED") + "\n",
		"non-final status":    csvHeader + "\n" + row("PAYOUT", "po_1", id, "1000", "PROCESSING") + "\n",
		"missing column":      csvHeader + "\n2026-10-05,CHARGE,ch_1\n",
	}
	for name, file := range cases {
		if _, err := Parse([]byte(file), "2026-10-05"); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: want ErrMalformed, got %v", name, err)
		}
	}
	if ext, err := Parse([]byte(csvHeader+"\n"), "2026-10-05"); err != nil || len(ext) != 0 {
		t.Errorf("an empty day is a valid file: %v %v", ext, err)
	}
}

func TestRecon_CutoffWindow(t *testing.T) {
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	_, cutoff := DayWindow(day)
	if want := day.Add(24*time.Hour - CutoffTolerance); !cutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want %v (objects final in the last 15 minutes may appear in tomorrow's file)", cutoff, want)
	}
}
