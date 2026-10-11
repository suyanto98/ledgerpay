package identity

import (
	"context"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/identity/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
)

// Directory adalah sisi baca identity untuk modul lain (contoh: transfer mencari penerima).
// Modul lain memakai Directory lewat interface miliknya sendiri, tidak lewat tabel users (ADR-001).
type Directory struct{}

func NewDirectory() Directory { return Directory{} }

// Party adalah informasi user yang boleh dilihat pihak lain dalam transaksi.
type Party struct {
	ID          uuid.UUID
	DisplayName string
}

// ActiveUserByEmail mencari user AKTIF berdasarkan email (case-insensitive). Email tak terdaftar dan
// user nonaktif sama-sama ErrNotFound: pemanggil tidak bisa (dan tidak perlu) membedakannya.
func (Directory) ActiveUserByEmail(ctx context.Context, q db.DBTX, email string) (Party, error) {
	if !validEmail(email) {
		return Party{}, apperr.ErrNotFound
	}
	u, err := store.New(q).GetUserByEmail(ctx, email)
	if err != nil {
		return Party{}, db.MapError(err)
	}
	if u.Status != "ACTIVE" {
		return Party{}, apperr.ErrNotFound
	}
	return Party{ID: u.ID, DisplayName: u.DisplayName}, nil
}

// DisplayName mengembalikan nama tampilan user (untuk menampilkan pihak lawan transaksi).
func (Directory) DisplayName(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error) {
	name, err := store.New(q).GetDisplayName(ctx, id)
	return name, db.MapError(err)
}

// DisplayNames: nama tampilan banyak user sekaligus (riwayat wallet). ID yang tidak ada tidak ikut.
func (Directory) DisplayNames(ctx context.Context, q db.DBTX, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := store.New(q).ListDisplayNames(ctx, ids)
	if err != nil {
		return nil, db.MapError(err)
	}
	for _, r := range rows {
		out[r.ID] = r.DisplayName
	}
	return out, nil
}
