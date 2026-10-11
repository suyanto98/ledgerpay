package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// MerchantWalletOf mengembalikan wallet milik merchant.
func (s *Service) MerchantWalletOf(ctx context.Context, q store.DBTX, merchantID uuid.UUID) (Wallet, error) {
	w, err := store.New(q).GetWalletByMerchant(ctx, store.GetWalletByMerchantParams{MerchantID: &merchantID, Currency: SupportedCurrency})
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
	// Objek bisnis sumber journal (contoh transfer/<id>): pemanggil memakainya untuk menampilkan pihak
	// lawan dari modul pemilik objek itu; ledger sendiri tidak mengenal transfer atau merchant.
	SourceType string
	SourceID   uuid.UUID
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
			Direction: Direction(r.Direction), Amount: r.Amount, Currency: r.Currency, CreatedAt: r.CreatedAt,
			SourceType: r.SourceType, SourceID: r.SourceID})
	}
	return items, next, nil
}

// JournalView adalah detail journal dari sudut pandang pemilik wallet.
type JournalView struct {
	ID         uuid.UUID
	Purpose    Purpose
	SourceType string
	SourceID   uuid.UUID
	Currency   string
	CreatedAt  time.Time
	Entries    []EntryView
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
	v := JournalView{ID: j.ID, Purpose: Purpose(j.Purpose), SourceType: j.SourceType, SourceID: j.SourceID, Currency: j.Currency, CreatedAt: j.CreatedAt}
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

// JournalBySource mengembalikan journal untuk (source, purpose), atau false kalau belum ada.
func (s *Service) JournalBySource(ctx context.Context, q store.DBTX, sourceType string, sourceID uuid.UUID, purpose Purpose) (uuid.UUID, bool, error) {
	id, err := store.New(q).JournalIDBySource(ctx, store.JournalIDBySourceParams{SourceType: sourceType, SourceID: sourceID, Purpose: string(purpose)})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, db.MapError(err)
	}
	return id, true, nil
}

// Flow adalah uang masuk (kredit) dan keluar (debit) sebuah wallet dalam satu rentang.
type Flow struct {
	In, Out int64
	Entries int32
}

// FlowSince menjumlah entry wallet sejak from (ringkasan bulan berjalan di Beranda).
func (s *Service) FlowSince(ctx context.Context, q store.DBTX, accountID uuid.UUID, from time.Time) (Flow, error) {
	r, err := store.New(q).AccountFlowSince(ctx, store.AccountFlowSinceParams{AccountID: accountID, Currency: SupportedCurrency, FromAt: from})
	if err != nil {
		return Flow{}, db.MapError(err)
	}
	return Flow{In: r.Credit, Out: r.Debit, Entries: r.Entries}, nil
}
