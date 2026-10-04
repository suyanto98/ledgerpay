// Package idempotency menerapkan ADR-L02 pola (a): key idempotency ditulis di transaksi YANG SAMA
// dengan efek finansialnya, sehingga "key tersimpan" ⇔ "efek terjadi" (atau penolakan domain
// tersimpan). Tidak ada keadaan setengah jadi yang perlu dipulihkan.
//
// Alur Execute (satu transaksi):
//
//	INSERT key ON CONFLICT DO NOTHING ─┬─ baris baru → SAVEPOINT → fn(tx) ─┬─ sukses → RELEASE, simpan respons
//	                                   │                                   ├─ 422 domain → ROLLBACK TO SAVEPOINT,
//	                                   │                                   │   simpan problem 422 (tanpa efek)
//	                                   │                                   └─ lainnya → seluruh tx rollback (tidak disimpan)
//	                                   └─ konflik → baca key: hash beda → 422 IDEMPOTENCY_KEY_REUSED
//	                                                          hash sama → replay respons tersimpan
//
// Request duplikat yang datang bersamaan: INSERT kedua menunggu di unique index sampai transaksi
// pertama selesai. Commit → replay; rollback → INSERT kedua berhasil dan diproses sebagai yang
// pertama; menunggu > lock_timeout → 409 REQUEST_IN_PROGRESS.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency/store"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/testhook"
)

const (
	HeaderKey      = "Idempotency-Key"
	HeaderReplayed = "Idempotent-Replayed"

	ClientUser = "USER"

	// Retention: setelah 7 hari key boleh dihapus (job cleanup, Sprint 6) dan dianggap baru.
	Retention = 7 * 24 * time.Hour
)

// keyPattern: kontrak API (docs/design/13-api-endpoints.md §5). Karakter dibatasi supaya key
// aman ditulis ke log dan tidak bisa dipakai log injection.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_\-:.]{8,255}$`)

// KeyFrom membaca dan memvalidasi header Idempotency-Key.
func KeyFrom(r *http.Request) (string, error) {
	k := r.Header.Get(HeaderKey)
	if k == "" {
		return "", apperr.ErrIdempotencyKeyRequired
	}
	if !keyPattern.MatchString(k) {
		return "", apperr.ErrIdempotencyKeyInvalid
	}
	return k, nil
}

// ReadBody membaca body mentah (untuk hash) lalu memasangnya kembali supaya httpx.Decode tetap
// bisa dipakai. Batas ukuran tetap dijaga middleware BodyLimit.
func ReadBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, apperr.ErrPayloadTooLarge
		}
		return nil, apperr.ErrMalformedJSON.WithCause(err)
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// Hash = SHA-256(method, path, canonical JSON body). Canonical: key objek diurutkan dan whitespace
// dibuang, sehingga {"a":1, "b":2} dan {"b":2,"a":1} dianggap request yang sama. Angka disalin
// apa adanya (json.Number) supaya 1.0 dan 1 tidak diam-diam disamakan.
func Hash(method, path string, body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, apperr.ErrMalformedJSON.WithCause(err)
	}
	canon, err := json.Marshal(v) // encoding/json mengurutkan key map
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(canon)
	return h.Sum(nil), nil
}

// Request mengidentifikasi satu niat client.
type Request struct {
	ClientType string
	ClientID   uuid.UUID
	Endpoint   string // template rute, contoh "POST /v1/transfers"
	Key        string
	Hash       []byte
}

// Outcome adalah hasil sukses fn yang akan disimpan dan dikirim.
type Outcome struct {
	Status int
	Body   any
	// Stored (opsional) adalah body yang disimpan untuk REPLAY bila berbeda dari Body. Contoh:
	// pembuatan API key menampilkan secret sekali; replay mengembalikan versi tanpa secret,
	// karena secret tidak boleh tersimpan plaintext di mana pun (termasuk idempotency_keys).
	Stored       any
	ResourceType string
	ResourceID   *uuid.UUID
}

// Response adalah apa yang dikirim ke client (eksekusi pertama atau replay).
type Response struct {
	Status   int
	Body     []byte
	Replayed bool
}

// Func adalah efek bisnis. keyID adalah id baris idempotency (referensi longgar di objek bisnis).
type Func func(ctx context.Context, tx pgx.Tx, keyID uuid.UUID) (Outcome, error)

type Service struct {
	pool  db.Beginner
	ids   idgen.Generator
	clock clock.Clock
}

func NewService(pool db.Beginner, ids idgen.Generator, c clock.Clock) *Service {
	return &Service{pool: pool, ids: ids, clock: c}
}

// Execute menjalankan fn tepat sekali per (client, endpoint, key). Lihat komentar package.
// r dipakai untuk membangun body problem 422 yang disimpan (request_id dan instance asli).
func (s *Service) Execute(r *http.Request, req Request, fn Func) (Response, error) {
	ctx := r.Context()
	var resp Response
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		resp = Response{}
		q := store.New(tx)
		keyID, err := q.InsertKey(ctx, store.InsertKeyParams{
			ID: s.ids.New(), ClientType: req.ClientType, ClientID: req.ClientID, Endpoint: req.Endpoint,
			Key: req.Key, RequestHash: req.Hash, ExpiresAt: s.clock.Now().Add(Retention),
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Key sudah ada DAN sudah commit (INSERT tadi menunggu transaksi pemiliknya selesai).
			resp, err = replay(ctx, q, req)
			return err
		case isLockTimeout(err):
			// Transaksi lain dengan key yang sama masih berjalan > lock_timeout.
			return apperr.ErrRequestInProgress.WithCause(err)
		case err != nil:
			return db.MapError(err)
		}

		if err := testhook.Pause(ctx, "idempotency.after_insert"); err != nil {
			return err
		}

		// SAVEPOINT: efek bisnis bisa dibatalkan tanpa membatalkan baris key, supaya penolakan
		// domain (422) tetap bisa disimpan untuk replay.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		out, ferr := fn(ctx, sp, keyID)
		if ferr != nil {
			_ = sp.Rollback(ctx) // ROLLBACK TO SAVEPOINT: efek hilang, baris key tetap
			e := apperr.As(ferr)
			if !storable(e) {
				return ferr // 400/409/5xx: tidak disimpan, seluruh transaksi rollback, retry aman
			}
			out = Outcome{Status: e.Status, Body: httpx.NewProblem(r, e)}
		} else if err := sp.Commit(ctx); err != nil { // RELEASE SAVEPOINT
			return err
		}

		resp, err = complete(ctx, q, keyID, out)
		return err
	})
	return resp, err
}

// complete menulis respons ke baris key (di tx pemanggil) dan mengembalikan respons untuk client.
func complete(ctx context.Context, q *store.Queries, keyID uuid.UUID, out Outcome) (Response, error) {
	body, err := json.Marshal(out.Body)
	if err != nil {
		return Response{}, err
	}
	stored := body
	if out.Stored != nil {
		if stored, err = json.Marshal(out.Stored); err != nil {
			return Response{}, err
		}
	}
	status := int16(out.Status) //nolint:gosec // G115: status HTTP 100–599 muat di int16
	if err := q.CompleteKey(ctx, store.CompleteKeyParams{
		ID: keyID, ResponseStatus: &status, ResponseBody: stored,
		ResourceType: nonEmpty(out.ResourceType), ResourceID: out.ResourceID,
	}); err != nil {
		return Response{}, db.MapError(err)
	}
	return Response{Status: out.Status, Body: body}, nil
}

// ---------------------------------------------------------------- pola (b): recovery point

// Reservation adalah hasil tx1 pola (b).
type Reservation struct {
	KeyID      uuid.UUID
	ResourceID uuid.UUID
	// Done terisi kalau tidak ada yang perlu dilanjutkan: respons sudah tersimpan (replay), atau
	// pembuatan resource ditolak 422 (disimpan seperti pola a).
	Done *Response
}

// Reserve adalah langkah tx1 ADR-L02 pola (b), untuk operasi yang harus memanggil pihak luar
// (provider) dan karena itu TIDAK BOLEH berada dalam satu transaksi DB:
//
//	tx1: INSERT key (respons NULL) + create(resource) + catat resource di key   → COMMIT
//	     (di luar tx) panggil provider, idempotency key ke provider = id resource
//	tx2: simpan hasil di resource + Complete(key)                              → COMMIT
//
// Retry dengan key yang sama saat respons masih NULL TIDAK ditolak, melainkan mengembalikan
// resource yang sama, supaya pemanggil melanjutkan dari recovery point-nya.
func (s *Service) Reserve(r *http.Request, req Request, resourceType string, create func(ctx context.Context, tx pgx.Tx, keyID uuid.UUID) (uuid.UUID, error)) (Reservation, error) {
	ctx := r.Context()
	var res Reservation
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		res = Reservation{}
		q := store.New(tx)
		keyID, err := q.InsertKey(ctx, store.InsertKeyParams{
			ID: s.ids.New(), ClientType: req.ClientType, ClientID: req.ClientID, Endpoint: req.Endpoint,
			Key: req.Key, RequestHash: req.Hash, ExpiresAt: s.clock.Now().Add(Retention),
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			row, err := q.GetKey(ctx, store.GetKeyParams{ClientType: req.ClientType, ClientID: req.ClientID, Endpoint: req.Endpoint, Key: req.Key})
			if err != nil {
				return db.MapError(err)
			}
			if !bytes.Equal(row.RequestHash, req.Hash) {
				return apperr.ErrIdempotencyKeyReused
			}
			if row.ResponseStatus != nil {
				res.Done = &Response{Status: int(*row.ResponseStatus), Body: row.ResponseBody, Replayed: true}
				return nil
			}
			if row.ResourceID == nil {
				return apperr.ErrRequestInProgress
			}
			res.KeyID, res.ResourceID = row.ID, *row.ResourceID // lanjutkan dari recovery point
			return nil
		case isLockTimeout(err):
			return apperr.ErrRequestInProgress.WithCause(err)
		case err != nil:
			return db.MapError(err)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		id, cerr := create(ctx, sp, keyID)
		if cerr != nil {
			_ = sp.Rollback(ctx)
			e := apperr.As(cerr)
			if !storable(e) {
				return cerr
			}
			resp, err := complete(ctx, q, keyID, Outcome{Status: e.Status, Body: httpx.NewProblem(r, e)})
			res.Done = &resp
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		res.KeyID, res.ResourceID = keyID, id
		return q.SetKeyResource(ctx, store.SetKeyResourceParams{ResourceType: &resourceType, ResourceID: &id, ID: keyID})
	})
	return res, err
}

// Complete adalah bagian akhir tx2 pola (b): menyimpan respons final di transaksi yang sama dengan
// pembaruan resource, sehingga "respons tersimpan" ⇔ "resource sudah di recovery point akhir".
func Complete(ctx context.Context, tx pgx.Tx, keyID uuid.UUID, out Outcome) (Response, error) {
	return complete(ctx, store.New(tx), keyID, out)
}

func replay(ctx context.Context, q *store.Queries, req Request) (Response, error) {
	row, err := q.GetKey(ctx, store.GetKeyParams{ClientType: req.ClientType, ClientID: req.ClientID, Endpoint: req.Endpoint, Key: req.Key})
	if err != nil {
		return Response{}, db.MapError(err)
	}
	if !bytes.Equal(row.RequestHash, req.Hash) {
		return Response{}, apperr.ErrIdempotencyKeyReused
	}
	if row.ResponseStatus == nil {
		// Hanya mungkin di pola (b) (Sprint 5): tx1 sudah commit, respons belum diisi.
		return Response{}, apperr.ErrRequestInProgress
	}
	return Response{Status: int(*row.ResponseStatus), Body: row.ResponseBody, Replayed: true}, nil
}

// storable: penolakan domain (422) bersifat deterministik untuk key itu, jadi disimpan. Kalau
// tidak, retry setelah saldo bertambah bisa sukses diam-diam padahal user mengira gagal (ADR-L02).
func storable(e *apperr.Error) bool { return e.Status == http.StatusUnprocessableEntity }

func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Write mengirim respons (eksekusi pertama atau replay). Replay diberi header
// Idempotent-Replayed: true supaya client dan log bisa membedakannya.
func Write(w http.ResponseWriter, resp Response) {
	ct := "application/json; charset=utf-8"
	if resp.Status >= 400 {
		ct = httpx.ProblemContentType
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	if resp.Replayed {
		w.Header().Set(HeaderReplayed, "true")
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
