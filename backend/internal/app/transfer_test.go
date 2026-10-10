package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/db/dbtest"
)

// ---------------------------------------------------------------- helpers

func (e env) transfer(t *testing.T, token, key string, body map[string]any) (result, map[string]any) {
	t.Helper()
	h := map[string]string{}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return e.doH(t, http.MethodPost, "/api/v1/transfers", token, h, body)
}

func transferBody(to string, amount string) map[string]any {
	return map[string]any{"recipient_email": to, "amount": amount, "currency": "IDR"}
}

func balanceOf(t *testing.T, e env, token string) string {
	t.Helper()
	_, body := e.do(t, http.MethodGet, "/api/v1/wallet", token, nil)
	b, _ := body["balance"].(string)
	return b
}

// assertLedgerHealthy: invariant checker setelah setiap test uang (docs/design/22-testing-strategy.md).
func assertLedgerHealthy(t *testing.T, e env) {
	t.Helper()
	rep, err := ledger.Verify(context.Background(), e.pool)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("ledger invariant violated: %s", rep)
	}
}

func countTransfers(t *testing.T, e env) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM transfers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newKey() string { return "test-" + uuid.NewString() }

// ---------------------------------------------------------------- functional

// LF-04: transfer sukses memindahkan saldo; pengirim dan penerima bisa membaca, orang lain 404.
func TestTransfer_CreateAndGet(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := loginAs(t, e, "rina@example.com", "Rina")
	andi, _ := loginAs(t, e, "andi@example.com", "Andi S.")
	eve, _ := loginAs(t, e, "eve@example.com", "Eve")
	seedTopup(t, e, rinaID, 100_000)

	resp, tr := e.transfer(t, rina, newKey(), map[string]any{
		"recipient_email": "andi@example.com", "amount": "30000", "currency": "IDR", "note": "Patungan makan"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d %v", resp.StatusCode, tr)
	}
	recv, _ := tr["receiver"].(map[string]any)
	if tr["status"] != "COMPLETED" || tr["amount"] != "30000" || tr["direction"] != "OUT" || recv["display_name"] != "Andi S." || tr["note"] != "Patungan makan" {
		t.Fatalf("unexpected transfer body: %v", tr)
	}
	if resp.Header.Get("Idempotent-Replayed") != "" {
		t.Fatal("first execution must not be marked as replayed")
	}
	if got := balanceOf(t, e, rina); got != "70000" {
		t.Fatalf("sender balance = %s, want 70000", got)
	}
	if got := balanceOf(t, e, andi); got != "30000" {
		t.Fatalf("receiver balance = %s, want 30000", got)
	}

	id := tr["id"].(string)
	_, asReceiver := e.do(t, http.MethodGet, "/api/v1/transfers/"+id, andi, nil)
	sender, _ := asReceiver["sender"].(map[string]any)
	if asReceiver["direction"] != "IN" || sender["display_name"] != "Rina" || asReceiver["receiver"] != nil {
		t.Fatalf("receiver view wrong: %v", asReceiver)
	}
	// BOLA: pihak ketiga, UUID acak, dan ID rusak → 404 yang sama.
	for _, path := range []string{id, uuid.NewString(), "not-a-uuid"} {
		r, body := e.do(t, http.MethodGet, "/api/v1/transfers/"+path, eve, nil)
		if r.StatusCode != http.StatusNotFound || body["code"] != "NOT_FOUND" {
			t.Fatalf("GET %s as third party: want 404, got %d %v", path, r.StatusCode, body)
		}
	}
	assertLedgerHealthy(t, e)
}

// Demo A sebagai test otomatis (LF-05 AC1–3).
func TestTransfer_IdempotentReplay(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	key := newKey()

	r1, b1 := e.transfer(t, rina, key, transferBody("andi@example.com", "50000"))
	r2, b2 := e.transfer(t, rina, key, transferBody("andi@example.com", "50000"))
	if r1.StatusCode != http.StatusCreated || r2.StatusCode != http.StatusCreated {
		t.Fatalf("want 201 twice, got %d and %d", r1.StatusCode, r2.StatusCode)
	}
	if r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay must carry Idempotent-Replayed: true")
	}
	j1, _ := json.Marshal(b1)
	j2, _ := json.Marshal(b2)
	if string(j1) != string(j2) {
		t.Fatalf("replay body differs:\n first:  %s\n replay: %s", j1, j2)
	}

	// Key sama, payload beda → 422, tidak ada efek.
	r3, b3 := e.transfer(t, rina, key, transferBody("andi@example.com", "60000"))
	if r3.StatusCode != http.StatusUnprocessableEntity || b3["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("want 422 IDEMPOTENCY_KEY_REUSED, got %d %v", r3.StatusCode, b3)
	}
	if got := balanceOf(t, e, rina); got != "50000" {
		t.Fatalf("balance = %s, want 50000 (debited exactly once)", got)
	}
	if n := countTransfers(t, e); n != 1 {
		t.Fatalf("transfers = %d, want 1", n)
	}

	// Urutan key di body tidak mengubah hash (canonical JSON) → replay, bukan REUSED.
	resp, _ := e.doRaw(t, rina, key, `{"currency":"IDR","amount":"50000","recipient_email":"andi@example.com"}`)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("reordered JSON keys must replay, got %d", resp.StatusCode)
	}
	assertLedgerHealthy(t, e)
}

func (e env) doRaw(t *testing.T, token, key, raw string) (result, map[string]any) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	// Kirim persis string raw (urutan key terjaga) lewat http.Request manual.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/api/v1/transfers", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, out
}

// ADR-L02: penolakan domain (422) DISIMPAN. Key yang sama tetap 422 walaupun saldo sudah cukup;
// percobaan baru harus memakai key baru. Tanpa ini, retry bisa sukses diam-diam.
func TestTransfer_DomainRejectionIsReplayed(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	key := newKey()

	r1, b1 := e.transfer(t, rina, key, transferBody("andi@example.com", "10000"))
	if r1.StatusCode != http.StatusUnprocessableEntity || b1["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("want 422 INSUFFICIENT_BALANCE, got %d %v", r1.StatusCode, b1)
	}
	seedTopup(t, e, rinaID, 100_000)
	r2, b2 := e.transfer(t, rina, key, transferBody("andi@example.com", "10000"))
	if r2.StatusCode != http.StatusUnprocessableEntity || b2["code"] != "INSUFFICIENT_BALANCE" || r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same key must replay the stored 422, got %d %v", r2.StatusCode, b2)
	}
	r3, _ := e.transfer(t, rina, newKey(), transferBody("andi@example.com", "10000"))
	if r3.StatusCode != http.StatusCreated {
		t.Fatalf("new key must execute, got %d", r3.StatusCode)
	}
	assertLedgerHealthy(t, e)
}

// Validasi 400 TIDAK disimpan: request yang diperbaiki boleh memakai key yang sama.
func TestTransfer_ValidationNotStored(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)

	if r, b := e.transfer(t, rina, "", transferBody("andi@example.com", "1000")); r.StatusCode != http.StatusBadRequest || b["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("missing key: got %d %v", r.StatusCode, b)
	}
	if r, b := e.transfer(t, rina, "bad key!", transferBody("andi@example.com", "1000")); r.StatusCode != http.StatusBadRequest || b["code"] != "IDEMPOTENCY_KEY_INVALID" {
		t.Fatalf("invalid key: got %d %v", r.StatusCode, b)
	}
	cases := map[string]map[string]any{
		"amount not a number": transferBody("andi@example.com", "abc"),
		"amount below Rp1":    transferBody("andi@example.com", "99"),
		"amount above limit":  transferBody("andi@example.com", "1000000001"),
		"bad email":           transferBody("not-an-email", "1000"),
		"note too long":       {"recipient_email": "andi@example.com", "amount": "1000", "currency": "IDR", "note": strings.Repeat("x", 141)},
	}
	key := newKey()
	for name, body := range cases {
		if r, b := e.transfer(t, rina, key, body); r.StatusCode != http.StatusBadRequest || b["code"] != "VALIDATION_ERROR" {
			t.Errorf("%s: want 400 VALIDATION_ERROR, got %d %v", name, r.StatusCode, b)
		}
	}
	if r, b := e.transfer(t, rina, key, map[string]any{"recipient_email": "andi@example.com", "amount": "1000", "currency": "USD"}); r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "CURRENCY_NOT_SUPPORTED" {
		t.Fatalf("USD: got %d %v", r.StatusCode, b)
	}
	if r, _ := e.transfer(t, rina, key, transferBody("andi@example.com", "1000")); r.StatusCode != http.StatusCreated {
		t.Fatalf("corrected request with the same key must execute, got %d", r.StatusCode)
	}
}

// INVALID_RECIPIENT generik: email tak terdaftar dan wallet frozen tidak bisa dibedakan (anti-enumerasi).
func TestTransfer_RecipientErrorsAreIndistinguishable(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	_, frozenID := login(t, e, "frozen@example.com")
	seedTopup(t, e, rinaID, 100_000)
	if _, err := e.pool.Exec(context.Background(), `UPDATE accounts SET status = 'FROZEN' WHERE user_id = $1`, frozenID); err != nil {
		t.Fatal(err)
	}

	_, unknown := e.transfer(t, rina, newKey(), transferBody("nobody@example.com", "1000"))
	_, frozen := e.transfer(t, rina, newKey(), transferBody("frozen@example.com", "1000"))
	for _, b := range []map[string]any{unknown, frozen} {
		delete(b, "request_id")
	}
	a, _ := json.Marshal(unknown)
	b, _ := json.Marshal(frozen)
	if string(a) != string(b) || unknown["code"] != "INVALID_RECIPIENT" {
		t.Fatalf("recipient errors differ or wrong code:\n unknown: %s\n frozen:  %s", a, b)
	}

	if r, body := e.transfer(t, rina, newKey(), transferBody("rina@example.com", "1000")); r.StatusCode != http.StatusUnprocessableEntity || body["code"] != "SELF_TRANSFER" {
		t.Fatalf("self transfer: got %d %v", r.StatusCode, body)
	}
	if got := balanceOf(t, e, rina); got != "100000" {
		t.Fatalf("rejected transfers must not move money, balance = %s", got)
	}
}

// ---------------------------------------------------------------- concurrency

// Demo B / race (a) sebagai test otomatis (DoD M2): saldo 100.000, 100 transfer paralel × 10.000
// dengan key berbeda → tepat 10 sukses, 90 INSUFFICIENT_BALANCE, saldo 0, tanpa drift.
func TestTransfer_ConcurrentDoubleSpend_100x(t *testing.T) {
	e := newEnv(t, dbtest.MaxConns(20))
	rina, rinaID := login(t, e, "rina@example.com")
	andi, _ := login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)

	statuses := runConcurrently(100, func(int) int {
		r, _ := e.transfer(t, rina, newKey(), transferBody("andi@example.com", "10000"))
		return r.StatusCode
	})
	if statuses[http.StatusCreated] != 10 || statuses[http.StatusUnprocessableEntity] != 90 {
		t.Fatalf("want exactly 10×201 and 90×422, got %v", statuses)
	}
	if balanceOf(t, e, rina) != "0" || balanceOf(t, e, andi) != "100000" {
		t.Fatalf("balances wrong: rina=%s andi=%s", balanceOf(t, e, rina), balanceOf(t, e, andi))
	}
	if n := countTransfers(t, e); n != 10 {
		t.Fatalf("transfers = %d, want 10", n)
	}
	assertLedgerHealthy(t, e)
}

// Race (b): 50 pasang A→B dan B→A bersamaan. Lock ORDER BY id → tidak ada siklus → tidak ada deadlock.
func TestTransfer_OppositeDirections_NoDeadlock(t *testing.T) {
	e := newEnv(t, dbtest.MaxConns(20))
	a, aID := login(t, e, "a@example.com")
	b, bID := login(t, e, "b@example.com")
	seedTopup(t, e, aID, 1_000_000)
	seedTopup(t, e, bID, 1_000_000)
	before := db.Deadlocks()

	statuses := runConcurrently(100, func(i int) int {
		from, to := a, "b@example.com"
		if i%2 == 1 {
			from, to = b, "a@example.com"
		}
		r, _ := e.transfer(t, from, newKey(), transferBody(to, "1000"))
		return r.StatusCode
	})
	if statuses[http.StatusCreated] != 100 {
		t.Fatalf("all 100 transfers must succeed, got %v", statuses)
	}
	if d := db.Deadlocks() - before; d != 0 {
		t.Fatalf("deadlocks detected: %d (lock ordering broken)", d)
	}
	if balanceOf(t, e, a) != "1000000" || balanceOf(t, e, b) != "1000000" {
		t.Fatal("symmetric transfers must leave both balances unchanged")
	}
	assertLedgerHealthy(t, e)
}

// lock_timeout: wallet sedang dikunci transaksi lain lebih lama dari batas → 409 RESOURCE_BUSY,
// tidak disimpan di idempotency; retry dengan key yang SAMA setelah lock lepas → dieksekusi.
func TestTransfer_LockTimeout_ResourceBusy(t *testing.T) {
	e := newEnv(t, dbtest.LockTimeout(200*time.Millisecond))
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	ctx := context.Background()

	holder, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE user_id = $1 FOR NO KEY UPDATE`, rinaID); err != nil {
		t.Fatal(err)
	}
	key := newKey()
	r1, b1 := e.transfer(t, rina, key, transferBody("andi@example.com", "1000"))
	_ = holder.Rollback(ctx)
	if r1.StatusCode != http.StatusConflict || b1["code"] != "RESOURCE_BUSY" {
		t.Fatalf("want 409 RESOURCE_BUSY, got %d %v", r1.StatusCode, b1)
	}
	r2, _ := e.transfer(t, rina, key, transferBody("andi@example.com", "1000"))
	if r2.StatusCode != http.StatusCreated || r2.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry after busy must execute (not replay), got %d", r2.StatusCode)
	}
	assertLedgerHealthy(t, e)
}

// runConcurrently menjalankan n fungsi yang dilepas BERSAMAAN oleh satu barrier (channel yang
// ditutup), lalu menghitung status yang dikembalikan.
func runConcurrently(n int, fn func(i int) int) map[int]int {
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // barrier
			s := fn(i)
			mu.Lock()
			statuses[s]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}
