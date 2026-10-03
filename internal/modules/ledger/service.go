// Package ledger adalah inti LedgerPay: double-entry ledger append-only (ADR-L01).
// PostJournal adalah SATU-SATUNYA cara mengubah saldo. Modul lain memanggil Service ini;
// mereka tidak menyentuh tabel accounts/journals/journal_entries secara langsung.
// Package ini tidak meng-import modul bisnis lain (ADR-001).
package ledger

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/testhook"
)

type Service struct {
	ids idgen.Generator
}

func NewService(ids idgen.Generator) *Service { return &Service{ids: ids} }

// CreateUserWallet membuat akun wallet (liability, saldo 0) untuk user baru. Dipanggil di
// dalam transaksi register, sehingga user dan wallet-nya lahir bersama atau tidak sama sekali.
func (s *Service) CreateUserWallet(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := store.New(tx).CreateUserWallet(ctx, store.CreateUserWalletParams{
		ID: s.ids.New(), UserID: &userID, Currency: SupportedCurrency,
	})
	return db.MapError(err)
}

// PostJournal memvalidasi lalu memposting journal di dalam transaksi pemanggil (tx).
// Mengembalikan error domain yang ramah (422) sebelum database sempat menolak; constraint DB
// (CHECK saldo, composite FK currency, deferred trigger keseimbangan) tetap menjadi jaring terakhir.
func (s *Service) PostJournal(ctx context.Context, tx pgx.Tx, j Journal) error {
	if err := j.Validate(); err != nil {
		return apperr.ErrValidation.WithDetail(err.Error()).WithCause(err)
	}
	t := templates[j.Purpose]
	q := store.New(tx)

	// 0) Reversal: periksa dulu secara eksplisit, SEBELUM cek saldo. Tanpa ini, reversal kedua
	//    bisa ditolak sebagai INSUFFICIENT_BALANCE (reversal pertama sudah menarik dananya), dan
	//    operator salah paham penyebabnya. Unique constraint DB tetap menjaga race dua reversal paralel.
	if j.ReversalOf != nil {
		exists, err := q.JournalExists(ctx, *j.ReversalOf)
		if err != nil {
			return db.MapError(err)
		}
		if !exists {
			return apperr.ErrNotFound.WithDetail("journal to reverse not found")
		}
		reversed, err := q.ReversalExists(ctx, j.ReversalOf)
		if err != nil {
			return db.MapError(err)
		}
		if reversed {
			return ErrAlreadyReversed
		}
	}

	// 1) Kunci akun WALLET yang terlibat, urut id (anti-deadlock). Akun sistem dibaca tanpa
	//    lock (ADR-L07): saldonya tidak disimpan, jadi tidak ada yang perlu diserialkan, dan
	//    menguncinya akan membuat semua top-up antre di satu baris PROVIDER_CLEARING.
	ids := uniqueAccountIDs(j.Entries)
	wallets, err := q.LockWalletAccounts(ctx, ids)
	if err != nil {
		return db.MapError(err)
	}
	if err := testhook.Pause(ctx, "ledger.after_lock"); err != nil {
		return err
	}
	systems, err := q.GetSystemAccounts(ctx, ids)
	if err != nil {
		return db.MapError(err)
	}
	if len(wallets)+len(systems) != len(ids) {
		return apperr.ErrNotFound.WithDetail("account not found")
	}
	accounts := make(map[uuid.UUID]account, len(ids))
	for _, r := range wallets {
		accounts[r.ID] = r
	}
	for _, r := range systems {
		accounts[r.ID] = account(r)
	}

	// 2) Cek setiap entry terhadap akun dan template; hitung perubahan saldo wallet.
	delta := map[uuid.UUID]int64{}
	for _, e := range j.Entries {
		a := accounts[e.AccountID]
		if a.Currency != j.Currency {
			return apperr.ErrValidation.WithDetail("entry currency differs from account currency")
		}
		if a.Status != "ACTIVE" {
			return ErrAccountFrozen
		}
		if !t.allows(e.Direction, kindOf(a)) {
			return apperr.ErrValidation.WithDetail(fmt.Sprintf("purpose %s does not allow %s on %s account", j.Purpose, e.Direction, kindOf(a)))
		}
		if a.IsSystem {
			continue // saldo akun sistem diturunkan dari entries, bukan disimpan
		}
		// Wallet = liability (normal kredit): kredit menambah, debit mengurangi.
		d := e.Amount
		if e.Direction == Debit {
			d = -d
		}
		if delta[a.ID], err = addChecked(delta[a.ID], d); err != nil {
			return apperr.ErrValidation.WithCause(err)
		}
	}

	// 3) Saldo tidak boleh negatif (LI-02). Ditolak dengan error ramah sebelum CHECK DB.
	for id, d := range delta {
		bal := *accounts[id].Balance
		next, err := addChecked(bal, d)
		if err != nil {
			return apperr.ErrValidation.WithCause(err)
		}
		if next < 0 {
			return ErrInsufficientBalance
		}
	}

	// 4) Tulis journal + entries (append-only), lalu saldo.
	if err := q.InsertJournal(ctx, store.InsertJournalParams{
		ID: j.ID, Purpose: string(j.Purpose), SourceType: j.SourceType, SourceID: j.SourceID,
		Currency: j.Currency, ReversalOf: j.ReversalOf, CorrelationID: j.CorrelationID,
		ActorType: j.Actor.Type, ActorID: j.Actor.ID, Description: nonEmpty(j.Description),
	}); err != nil {
		return db.MapError(err)
	}
	for _, e := range j.Entries {
		if err := q.InsertEntry(ctx, store.InsertEntryParams{
			ID: s.ids.New(), JournalID: j.ID, AccountID: e.AccountID,
			Direction: string(e.Direction), Amount: e.Amount, Currency: j.Currency,
		}); err != nil {
			return db.MapError(err)
		}
	}
	for _, id := range sortedKeys(delta) {
		if delta[id] == 0 {
			continue
		}
		if err := q.AddToBalance(ctx, store.AddToBalanceParams{ID: id, Delta: delta[id]}); err != nil {
			return db.MapError(err)
		}
	}
	return nil
}

// SystemAccountID mengembalikan id akun sistem (contoh PROVIDER_CLEARING).
func (s *Service) SystemAccountID(ctx context.Context, q store.DBTX, code string) (uuid.UUID, error) {
	id, err := store.New(q).GetSystemAccountID(ctx, store.GetSystemAccountIDParams{SystemCode: &code, Currency: SupportedCurrency})
	return id, db.MapError(err)
}

// account adalah baris akun yang dibaca PostJournal (wallet terkunci atau akun sistem).
type account = store.LockWalletAccountsRow

func kindOf(a account) AccountKind {
	if a.IsSystem && a.SystemCode != nil {
		return kindForSystemCode(*a.SystemCode)
	}
	if a.UserID != nil {
		return KindUserWallet
	}
	return KindMerchantWallet
}

func uniqueAccountIDs(entries []Entry) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, e := range entries {
		if !seen[e.AccountID] {
			seen[e.AccountID] = true
			out = append(out, e.AccountID)
		}
	}
	return out
}

// sortedKeys: update saldo juga berurutan id, supaya pola akses selalu deterministik.
func sortedKeys(m map[uuid.UUID]int64) []uuid.UUID {
	keys := make([]uuid.UUID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
