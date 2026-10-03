package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/ledger/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
)

// Wallet adalah ringkasan wallet milik user.
type Wallet struct {
	AccountID uuid.UUID
	Currency  string
	Balance   int64
	Status    string
}

// WalletOf mengembalikan wallet milik user. Selalu dibaca dari primary (saldo = data strong consistency).
func (s *Service) WalletOf(ctx context.Context, q store.DBTX, userID uuid.UUID) (Wallet, error) {
	w, err := store.New(q).GetWalletByUser(ctx, store.GetWalletByUserParams{UserID: &userID, Currency: SupportedCurrency})
	if err != nil {
		return Wallet{}, db.MapError(err)
	}
	return Wallet{AccountID: w.ID, Currency: w.Currency, Balance: *w.Balance, Status: w.Status}, nil
}

// HistoryFilter adalah filter riwayat wallet (docs/design/13-api-endpoints.md §4).
type HistoryFilter struct {
	Limit     int32
	Cursor    *Cursor
	Direction *Direction // nil = semua
	Purposes  []Purpose  // kosong = semua
	From, To  *time.Time // [From, To)
}

// Cursor menandai posisi terakhir yang sudah dikirim (created_at DESC, id DESC).
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// Movement adalah satu baris riwayat dari sudut pandang wallet.
type Movement struct {
	EntryID   uuid.UUID
	JournalID uuid.UUID
	Purpose   Purpose
	Direction Direction
	Amount    int64
	Currency  string
	CreatedAt time.Time
}

// History mengembalikan riwayat wallet (limit+1 baris diambil untuk mengetahui has_more).
func (s *Service) History(ctx context.Context, q store.DBTX, accountID uuid.UUID, f HistoryFilter) (items []Movement, next *Cursor, err error) {
	p := store.ListAccountEntriesParams{AccountID: accountID, Currency: SupportedCurrency, Lim: f.Limit + 1, FromAt: f.From, ToAt: f.To, Purposes: []string{}}
	if f.Cursor != nil {
		p.CursorAt, p.CursorID = &f.Cursor.At, &f.Cursor.ID
	}
	if f.Direction != nil {
		d := string(*f.Direction)
		p.Direction = &d
	}
	for _, pu := range f.Purposes {
		p.Purposes = append(p.Purposes, string(pu))
	}
	rows, err := store.New(q).ListAccountEntries(ctx, p)
	if err != nil {
		return nil, nil, db.MapError(err)
	}
	for i, r := range rows {
		if int32(i) == f.Limit { //nolint:gosec // i < limit+1 ≤ 101
			last := items[len(items)-1]
			next = &Cursor{At: last.CreatedAt, ID: last.EntryID}
			break
		}
		items = append(items, Movement{EntryID: r.EntryID, JournalID: r.JournalID, Purpose: Purpose(r.Purpose),
			Direction: Direction(r.Direction), Amount: r.Amount, Currency: r.Currency, CreatedAt: r.CreatedAt})
	}
	return items, next, nil
}

// JournalView adalah detail journal dari sudut pandang pemilik wallet.
type JournalView struct {
	ID        uuid.UUID
	Purpose   Purpose
	Currency  string
	CreatedAt time.Time
	Entries   []EntryView
	// Mine adalah entry pada wallet pemanggil (arah + jumlah yang ia lihat sebagai transaksinya).
	Mine EntryView
}

// EntryView menyembunyikan identitas akun pihak lain (hanya label generik).
type EntryView struct {
	Direction Direction
	Amount    int64
	Label     string // "Wallet Anda" | "Wallet pengguna lain" | "Sistem"
}

// JournalFor mengembalikan journal HANYA jika journal itu punya entry di wallet ownerAccountID.
// Kalau tidak → NOT_FOUND (bukan 403): pemanggil tidak boleh tahu journal milik orang lain ada (BOLA).
func (s *Service) JournalFor(ctx context.Context, q store.DBTX, ownerAccountID, journalID uuid.UUID) (JournalView, error) {
	st := store.New(q)
	j, err := st.GetJournal(ctx, journalID)
	if err != nil {
		return JournalView{}, db.MapError(err)
	}
	rows, err := st.ListJournalEntriesWithAccounts(ctx, journalID)
	if err != nil {
		return JournalView{}, db.MapError(err)
	}
	v := JournalView{ID: j.ID, Purpose: Purpose(j.Purpose), Currency: j.Currency, CreatedAt: j.CreatedAt}
	found := false
	for _, r := range rows {
		ev := EntryView{Direction: Direction(r.Direction), Amount: r.Amount}
		switch {
		case r.AccountID == ownerAccountID:
			ev.Label = "Wallet Anda"
			if !found {
				v.Mine, found = ev, true
			}
		case r.SystemCode != nil:
			ev.Label = "Sistem"
		default:
			ev.Label = "Wallet pengguna lain"
		}
		v.Entries = append(v.Entries, ev)
	}
	if !found {
		return JournalView{}, apperr.ErrNotFound
	}
	return v, nil
}
