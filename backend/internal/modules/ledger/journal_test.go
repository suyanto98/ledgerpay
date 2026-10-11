package ledger

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"
)

func validTransfer() Journal {
	a, b := uuid.New(), uuid.New()
	uid := uuid.New()
	return Journal{
		ID: uuid.New(), Purpose: PurposeTransfer, SourceType: "transfer", SourceID: uuid.New(),
		Currency: "IDR", CorrelationID: "req-1", Actor: Actor{Type: "USER", ID: &uid},
		Entries: []Entry{{AccountID: a, Direction: Debit, Amount: 5000}, {AccountID: b, Direction: Credit, Amount: 5000}},
	}
}

// TestJournal_Validate: aturan struktural LI-01 (seimbang, ≥2 entry, amount > 0) dan pasangan
// purpose ↔ source, aktor, currency.
func TestJournal_Validate(t *testing.T) {
	opsID := uuid.New()
	cases := map[string]struct {
		mutate func(*Journal)
		want   error
	}{
		"valid":                {func(*Journal) {}, nil},
		"unbalanced":           {func(j *Journal) { j.Entries[1].Amount = 4999 }, ErrUnbalanced},
		"single entry":         {func(j *Journal) { j.Entries = j.Entries[:1] }, ErrTooFewEntries},
		"zero amount":          {func(j *Journal) { j.Entries[0].Amount, j.Entries[1].Amount = 0, 0 }, ErrInvalidAmount},
		"negative amount":      {func(j *Journal) { j.Entries[0].Amount = -5000 }, ErrInvalidAmount},
		"bad direction":        {func(j *Journal) { j.Entries[0].Direction = "X" }, ErrBadDirection},
		"unknown purpose":      {func(j *Journal) { j.Purpose = "MINT_MONEY" }, ErrUnknownPurpose},
		"source mismatch":      {func(j *Journal) { j.SourceType = "payout" }, ErrSourceMismatch},
		"reversal not allowed": {func(j *Journal) { r := uuid.New(); j.ReversalOf = &r }, ErrReversalInvalid},
		"wrong currency":       {func(j *Journal) { j.Currency = "USD" }, ErrUnsupportedCurrency},
		"system with actor id": {func(j *Journal) { j.Actor.Type = "SYSTEM" }, ErrMissingActor},
		"user without id":      {func(j *Journal) { j.Actor.ID = nil }, ErrMissingActor},
		"manual without reason": {func(j *Journal) {
			j.Purpose, j.SourceType, j.Actor = PurposeAdjustmentManual, "ops_action", Actor{Type: "OPS", ID: &opsID}
		}, ErrManualReason},
		"overflow": {func(j *Journal) {
			j.Entries = []Entry{
				{AccountID: uuid.New(), Direction: Debit, Amount: 1<<62 + 1<<61},
				{AccountID: uuid.New(), Direction: Debit, Amount: 1<<62 + 1<<61},
				{AccountID: uuid.New(), Direction: Credit, Amount: 1},
			}
		}, ErrOverflow},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			j := validTransfer()
			c.mutate(&j)
			err := j.Validate()
			if c.want == nil && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

// TestJournalPurpose_Templates: tabel template desain domain §6 sebagai test. Ini mitigasi risiko utama
// Sprint 2: salah arah debit/kredit. Wallet = liability → kredit menambah saldo user.
func TestJournalPurpose_Templates(t *testing.T) {
	cases := []struct {
		purpose       Purpose
		debit, credit AccountKind
	}{
		{PurposeTopupCredit, KindProviderClearing, KindUserWallet},
		{PurposeTransfer, KindUserWallet, KindUserWallet},
		{PurposePayment, KindUserWallet, KindMerchantWallet},
		{PurposeRefund, KindMerchantWallet, KindUserWallet},
		{PurposePayoutHold, KindUserWallet, KindPayoutInTransit},
		{PurposePayoutSettle, KindPayoutInTransit, KindBankClearing},
		{PurposePayoutReversal, KindPayoutInTransit, KindUserWallet},
		{PurposeProviderSettlement, KindBankClearing, KindProviderClearing},
	}
	for _, c := range cases {
		tpl := templates[c.purpose]
		if !tpl.allows(Debit, c.debit) || !tpl.allows(Credit, c.credit) {
			t.Errorf("%s: expected D=%s C=%s to be allowed", c.purpose, c.debit, c.credit)
		}
		// Sisi terbalik harus ditolak (kecuali transfer, yang kedua sisinya sama-sama wallet user).
		if c.debit != c.credit && (tpl.allows(Debit, c.credit) || tpl.allows(Credit, c.debit)) {
			t.Errorf("%s: reversed sides must be rejected", c.purpose)
		}
	}
	if len(cases) != len(templates)-1 { // -1: ADJUSTMENT_MANUAL (bebas akun, diuji terpisah)
		t.Fatalf("every purpose except ADJUSTMENT_MANUAL must have a template case (%d cases, %d templates)", len(cases), len(templates))
	}
	if !templates[PurposeAdjustmentManual].allows(Debit, KindBankClearing) {
		t.Error("ADJUSTMENT_MANUAL allows any account")
	}
}

func TestParseAmount(t *testing.T) {
	good := map[string]int64{"1": 1, "5000000": 5_000_000, "999999999999999999": 999_999_999_999_999_999}
	for s, want := range good {
		if got, err := ParseAmount(s); err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "1.5", "1e6", " 1", "+1", "abc", "9223372036854775808", "1000000000000000000000"} {
		if _, err := ParseAmount(bad); err == nil {
			t.Errorf("ParseAmount(%q) must fail", bad)
		}
	}
}

// TestProperty_JournalAlwaysBalanced: untuk journal acak yang dibangun seimbang, Validate selalu
// menerima; mengubah SATU amount (tambah 1) selalu ditolak sebagai tidak seimbang.
// rapid menjalankan ribuan kasus dan, kalau gagal, mengecilkan contoh ke kasus minimal.
func TestProperty_JournalAlwaysBalanced(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		nDebit := rapid.IntRange(1, 5).Draw(rt, "debits")
		var entries []Entry
		var total int64
		for i := 0; i < nDebit; i++ {
			a := rapid.Int64Range(1, 1_000_000_000).Draw(rt, "debit")
			total += a
			entries = append(entries, Entry{AccountID: uuid.New(), Direction: Debit, Amount: a})
		}
		// Jumlah kredit ditentukan SETELAH total diketahui: total 1 tidak bisa dipecah jadi 2 kredit
		// positif. (Bug generator ini ditemukan rapid sendiri, dengan contoh yang sudah dikecilkan.)
		nCredit := rapid.IntRange(1, int(min(5, total))).Draw(rt, "credits")
		remaining := total
		for i := 0; i < nCredit; i++ {
			left := nCredit - i - 1
			part := remaining - int64(left)
			if left > 0 {
				part = rapid.Int64Range(1, remaining-int64(left)).Draw(rt, "credit")
			}
			entries = append(entries, Entry{AccountID: uuid.New(), Direction: Credit, Amount: part})
			remaining -= part
		}
		uid := uuid.New()
		j := Journal{ID: uuid.New(), Purpose: PurposeTransfer, SourceType: "transfer", SourceID: uuid.New(),
			Currency: "IDR", CorrelationID: "p", Actor: Actor{Type: "USER", ID: &uid}, Entries: entries}
		if err := j.Validate(); err != nil {
			rt.Fatalf("balanced journal rejected: %v (entries=%v)", err, entries)
		}
		idx := rapid.IntRange(0, len(entries)-1).Draw(rt, "mutate")
		j.Entries[idx].Amount++
		if err := j.Validate(); !errors.Is(err, ErrUnbalanced) {
			rt.Fatalf("mutated journal must be unbalanced, got %v", err)
		}
	})
}
