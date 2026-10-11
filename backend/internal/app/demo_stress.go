//go:build demo

package app

// "Uji ketahanan" untuk pengunjung sandbox: versi tombol dari Demo B (scripts/demo/b_concurrency.sh). Server
// membuat Dompet Uji bersaldo TEPAT Rp100.000, lalu 100 transfer Rp10.000 dimulai bersamaan ke kontak Siti.
// Janji yang dibuktikan: tepat 10 berhasil, 90 ditolak INSUFFICIENT_BALANCE, saldo akhir 0, tidak pernah minus.
//
// Kenapa lewat server, bukan 100 fetch dari browser: rate limit transfer (30/menit per user) sengaja tidak
// dilonggarkan di VM publik. Transfer tetap memakai transfer.Service.Create + ledger.PostJournal yang sama
// dengan POST /transfers (lock wallet ORDER BY id, cek saldo SETELAH lock, ADR-L03).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
)

const (
	stressAttempts = 100
	// Transfer yang berjalan paralel dibatasi supaya satu pengunjung tidak menghabiskan pool koneksi API
	// milik pengunjung lain. Tetap paralel: 8 transaksi berebut lock wallet yang sama pada saat bersamaan.
	stressParallel = 8
)

var (
	stressFunds  = rp(100_000)
	stressAmount = rp(10_000)
)

type stressAttempt struct {
	N          int    `json:"n"`     // nomor permintaan (urutan dikirim)
	Order      int    `json:"order"` // urutan selesai
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"` // error code katalog kalau ditolak
	DurationMS int64  `json:"duration_ms"`
	finished   time.Time
}

type stressResult struct {
	Attempts     int             `json:"attempts"`
	Succeeded    int             `json:"succeeded"`
	Rejected     int             `json:"rejected"`
	StartBalance string          `json:"start_balance"`
	EndBalance   string          `json:"end_balance"`
	Amount       string          `json:"amount"`
	Recipient    string          `json:"recipient"`
	Parallel     int             `json:"parallel"`
	ElapsedMS    int64           `json:"elapsed_ms"`
	Consistent   bool            `json:"consistent"` // saldo akhir = saldo awal − berhasil × nominal, dan ≥ 0
	Results      []stressAttempt `json:"results"`
}

func (s *sandboxer) stressTransfers(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var (
		sandboxID    uuid.UUID
		contactsJSON []byte
	)
	err := s.d.Pool.QueryRow(r.Context(), `SELECT id, contacts FROM demo_sandboxes WHERE user_id = $1 AND expired_at IS NULL`, p.UserID).
		Scan(&sandboxID, &contactsJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, apperr.ErrNotFound) // hanya pengunjung sandbox (Budi)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var contacts []sandboxContact
	if err := json.Unmarshal(contactsJSON, &contacts); err != nil || len(contacts) == 0 {
		httpx.WriteError(w, r, fmt.Errorf("demo stress: sandbox has no contacts: %w", err))
		return
	}
	recipient := contacts[0]
	res, err := s.runStress(r.Context(), sandboxID, recipient)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *sandboxer) runStress(ctx context.Context, sandboxID uuid.UUID, to sandboxContact) (stressResult, error) {
	meta := identity.RequestMeta{RequestID: httpx.RequestIDFrom(ctx)}
	correlationID := meta.RequestID
	secret, err := randomSecret() // Dompet Uji tidak bisa login: password acak yang langsung dibuang
	if err != nil {
		return stressResult{}, err
	}
	hash, err := s.w.identity.HashPassword(ctx, secret)
	if err != nil {
		return stressResult{}, err
	}
	tag, err := randomHex(3)
	if err != nil {
		return stressResult{}, err
	}
	domain := to.Email[strings.Index(to.Email, "@")+1:] // domain sandbox yang sama dengan kontak
	fundingID, err := s.w.sv.ledger.SystemAccountID(ctx, s.d.Pool, demoFundingCode)
	if err != nil {
		return stressResult{}, err
	}
	var tester uuid.UUID
	// [1] Dompet Uji + saldo TEPAT Rp100.000, dan masuk daftar anggota sandbox (ikut dinonaktifkan saat kedaluwarsa).
	err = db.WithTx(ctx, s.d.Pool, func(tx pgx.Tx) error {
		u, err := s.w.identity.ProvisionInTx(ctx, tx, "uji-"+tag+"@"+domain, "Dompet Uji", hash, "demo_stress", meta)
		if err != nil {
			return err
		}
		tester = u.ID
		if err := s.fund(ctx, tx, fundingID, u.ID, stressFunds, correlationID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE demo_sandboxes SET member_user_ids = array_append(member_user_ids, $1) WHERE id = $2`, u.ID, sandboxID)
		return err
	})
	if err != nil {
		return stressResult{}, db.MapError(err)
	}

	// [2] 100 permintaan dimulai bersamaan (gerbang `start`), paralel sebanyak stressParallel.
	results := make([]stressAttempt, stressAttempts)
	start := make(chan struct{})
	sem := make(chan struct{}, stressParallel)
	var wg sync.WaitGroup
	for i := range stressAttempts {
		wg.Go(func() {
			<-start
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now() //nolint:forbidigo // durasi teknis untuk tampilan
			note := fmt.Sprintf("Uji ketahanan #%d", i+1)
			err := db.WithTx(ctx, s.d.Pool, func(tx pgx.Tx) error {
				_, err := s.w.transfers.Create(ctx, tx, transfer.Command{SenderID: tester, RecipientEmail: to.Email,
					Amount: stressAmount, Currency: ledger.SupportedCurrency, Note: &note, CorrelationID: correlationID})
				return err
			})
			a := stressAttempt{N: i + 1, OK: err == nil, finished: time.Now()} //nolint:forbidigo // urutan selesai
			a.DurationMS = a.finished.Sub(t0).Milliseconds()
			if err != nil {
				a.Code = apperr.As(db.MapError(err)).Code
			}
			results[i] = a
		})
	}
	began := time.Now() //nolint:forbidigo // durasi teknis untuk tampilan
	close(start)
	wg.Wait()
	elapsed := time.Since(began)

	sort.SliceStable(results, func(i, j int) bool { return results[i].finished.Before(results[j].finished) })
	res := stressResult{Attempts: stressAttempts, StartBalance: ledger.FormatAmount(stressFunds), Amount: ledger.FormatAmount(stressAmount),
		Recipient: to.Name, Parallel: stressParallel, ElapsedMS: elapsed.Milliseconds(), Results: results}
	for i := range results {
		results[i].Order = i + 1
		if results[i].OK {
			res.Succeeded++
		} else {
			res.Rejected++
		}
	}
	wl, err := s.w.sv.ledger.WalletOf(ctx, s.d.Pool, tester)
	if err != nil {
		return stressResult{}, err
	}
	res.EndBalance = ledger.FormatAmount(wl.Balance)
	res.Consistent = wl.Balance >= 0 && wl.Balance == stressFunds-int64(res.Succeeded)*stressAmount
	s.d.Logger.Info("demo.stress_done", "succeeded", res.Succeeded, "rejected", res.Rejected, "elapsed_ms", res.ElapsedMS, "consistent", res.Consistent)
	return res, nil
}
