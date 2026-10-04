// Package mockprovider adalah payment gateway TIRUAN (ADR-L05): sistem eksternal yang realistis
// dan bisa dikendalikan, supaya setiap failure scenario (timeout, webhook ganda/terlambat/tidak
// berurutan, signature salah) bisa direproduksi oleh test dan demo.
//
// Batas: package ini mensimulasikan pihak luar, jadi TIDAK boleh meng-import modul LedgerPay
// (depguard). Satu-satunya yang dibagi adalah format tanda tangan webhook (platform/webhooksig),
// karena itu memang kontrak bersama.
//
// State in-memory + id berurutan per run (ch_<epoch>_000001) dan /admin/reset, sehingga demo
// menghasilkan output yang sama setiap kali dijalankan. Kontrak: docs/design/14-api-external-contracts.md §1–2.
package mockprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerpay/internal/platform/webhooksig"
)

// Config mengatur mock. TimeScale memperkecil semua jeda (retry webhook, webhook_delay) untuk test.
type Config struct {
	APISecret     string        // Authorization: Bearer <APISecret> dari LedgerPay
	WebhookSecret string        // untuk menandatangani webhook
	PublicURL     string        // basis checkout_url yang dibuka browser user
	TimeScale     float64       // 1 = waktu nyata; 0.001 = 1000× lebih cepat (test)
	HoldTimeout   time.Duration // berapa lama skenario timeout_* menahan respons
	HTTPClient    *http.Client  // pengirim webhook (timeout 10 dtk)
	Now           func() time.Time
	Logger        *slog.Logger
}

// Charge adalah sesi pembayaran (top-up) di sisi provider.
type Charge struct {
	ID            string     `json:"id"`
	Reference     string     `json:"reference"`
	Status        string     `json:"status"` // PENDING, SUCCEEDED, FAILED
	Amount        string     `json:"amount"`
	Currency      string     `json:"currency"`
	CheckoutURL   string     `json:"checkout_url"`
	PaidAt        *time.Time `json:"paid_at"`
	FailureReason *string    `json:"failure_reason"`
	CreatedAt     time.Time  `json:"created_at"`

	callbackURL   string
	returnURL     string
	correlationID string
}

// Scenario global (PUT /admin/scenario).
type Scenario struct {
	Charges           string   `json:"charges"`             // ok | timeout_after_process | timeout_before_process | error_5xx | decline
	Webhooks          []string `json:"webhooks"`            // webhook_duplicate:n | webhook_delay:<dur> | webhook_out_of_order | webhook_bad_signature
	Payouts           string   `json:"payouts"`             // ok | timeout_after_process | timeout_before_process | error_5xx | decline | response_mismatch
	PayoutStatusQuery string   `json:"payout_status_query"` // enabled (default) | disabled
}

// bankPayout adalah transfer keluar di sisi bank.
type bankPayout struct {
	ID          string     `json:"id"`
	Reference   string     `json:"reference"`
	Status      string     `json:"status"` // SUCCEEDED, FAILED
	Amount      string     `json:"amount"`
	Currency    string     `json:"currency"`
	ProcessedAt *time.Time `json:"processed_at"`
	Reason      string     `json:"failure_reason,omitempty"`
}

type payoutStats struct {
	executed int // uang benar-benar dikirim (bukti Demo C: harus 1)
	received int // request yang masuk (termasuk retry/resubmit)
}

// Delivery mencatat setiap percobaan kirim webhook (GET /admin/deliveries: bukti di demo).
type Delivery struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	ChargeID  string    `json:"charge_id"`
	Attempt   int       `json:"attempt"`
	Status    int       `json:"status"` // 0 = gagal terhubung / timeout
	BadSig    bool      `json:"bad_signature,omitempty"`
	At        time.Time `json:"at"`
}

type idemEntry struct {
	hash   [32]byte
	status int
	body   []byte
}

type Server struct {
	cfg Config

	mu         sync.Mutex
	charges    map[string]*Charge
	byRef      map[string]string
	idem       map[string]idemEntry
	scenario   Scenario
	deliveries []Delivery
	payouts    map[string]*bankPayout // per Idempotency-Key (= payout id LedgerPay)
	sink       []SinkHit              // webhook merchant yang diterima (server merchant tiruan untuk demo)
	stats      map[string]*payoutStats
	seq        int
	epoch      string // prefix ID per "run": unik lintas restart/reset (temuan Sprint 5, lihat resetLocked)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(cfg Config) *Server {
	if cfg.TimeScale <= 0 {
		cfg.TimeScale = 1
	}
	if cfg.HoldTimeout == 0 {
		cfg.HoldTimeout = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() } //nolint:forbidigo // sistem eksternal tiruan, bukan kode LedgerPay
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.resetLocked()
	return s
}

// Close menghentikan pengiriman webhook yang masih terjadwal dan menunggu goroutine selesai.
func (s *Server) Close() {
	s.cancel()
	s.wg.Wait()
}

// resetLocked mengosongkan state. ID tetap berurutan di dalam satu run (output demo mudah dibaca),
// tetapi diberi prefix epoch: database LedgerPay TIDAK ikut di-reset, jadi ID yang dipakai ulang
// setelah restart/reset (ch_000001 lagi) akan bentrok dengan uq_topups_provider_ref. Provider
// sungguhan tidak pernah memakai ulang ID; mock juga tidak boleh.
func (s *Server) resetLocked() {
	s.charges, s.byRef, s.idem = map[string]*Charge{}, map[string]string{}, map[string]idemEntry{}
	s.scenario, s.deliveries, s.seq = Scenario{Charges: "ok"}, nil, 0
	s.payouts, s.stats, s.sink = map[string]*bankPayout{}, map[string]*payoutStats{}, nil
	s.epoch = strconv.FormatInt(s.cfg.Now().UnixNano()/1e6, 36) // milidetik, base36
}

// Handler mengembalikan router HTTP mock.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/charges", s.auth(s.createCharge))
	mux.HandleFunc("GET /v1/charges/{id}", s.auth(s.getCharge))
	mux.HandleFunc("GET /v1/charges", s.auth(s.findCharge))
	mux.HandleFunc("GET /checkout/{id}", s.checkoutPage)
	mux.HandleFunc("POST /checkout/{id}/{action}", s.checkoutAction)
	mux.HandleFunc("GET /admin/scenario", s.getScenario)
	mux.HandleFunc("PUT /admin/scenario", s.putScenario)
	mux.HandleFunc("POST /admin/reset", s.reset)
	mux.HandleFunc("POST /admin/charges/{id}/{action}", s.adminComplete)
	mux.HandleFunc("GET /admin/deliveries", s.getDeliveries)
	mux.HandleFunc("POST /v1/payouts", s.auth(s.createPayout))
	mux.HandleFunc("GET /v1/payouts/{key}", s.auth(s.getPayout))
	mux.HandleFunc("GET /admin/transfers", s.getTransfers)
	mux.HandleFunc("POST /merchant-sink", s.sinkReceive)
	mux.HandleFunc("GET /admin/merchant-sink", s.sinkList)
	return mux
}

// ---------------------------------------------------------------- API untuk LedgerPay

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.cfg.APISecret {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid API secret")
			return
		}
		next(w, r)
	}
}

type createChargeRequest struct {
	Amount      string            `json:"amount"`
	Currency    string            `json:"currency"`
	Reference   string            `json:"reference"`
	CallbackURL string            `json:"callback_url"`
	ReturnURL   string            `json:"return_url"`
	ExpiresAt   *time.Time        `json:"expires_at"`
	Metadata    map[string]string `json:"metadata"`
}

func (s *Server) createCharge(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	body, err := readAll(r)
	if err != nil || key == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key and a JSON body are required")
		return
	}
	scenario := r.Header.Get("X-Mock-Scenario")
	if scenario == "" {
		s.mu.Lock()
		scenario = s.scenario.Charges
		s.mu.Unlock()
	}
	switch scenario {
	case "error_5xx":
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "provider temporarily unavailable")
		return
	case "timeout_before_process": // tidak diproses, respons ditahan sampai client menyerah
		s.hold(r)
		writeErr(w, http.StatusGatewayTimeout, "timeout", "not processed")
		return
	case "decline":
		writeErr(w, http.StatusUnprocessableEntity, "declined", "charge declined")
		return
	}
	var in createChargeRequest
	if err := json.Unmarshal(body, &in); err != nil || in.Amount == "" || in.Reference == "" || in.CallbackURL == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "amount, reference and callback_url are required")
		return
	}

	hash := sha256.Sum256(body)
	s.mu.Lock()
	if e, ok := s.idem[key]; ok { // idempotency seperti PG sungguhan: key sama → respons sama
		s.mu.Unlock()
		if e.hash != hash {
			writeErr(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "same Idempotency-Key with a different body")
			return
		}
		writeRaw(w, e.status, e.body)
		return
	}
	s.seq++
	c := &Charge{
		ID: fmt.Sprintf("ch_%s_%06d", s.epoch, s.seq), Reference: in.Reference, Status: "PENDING", Amount: in.Amount, Currency: in.Currency,
		CreatedAt: s.cfg.Now(), callbackURL: in.CallbackURL, returnURL: in.ReturnURL, correlationID: in.Metadata["correlation_id"],
	}
	c.CheckoutURL = strings.TrimRight(s.cfg.PublicURL, "/") + "/checkout/" + c.ID
	s.charges[c.ID], s.byRef[c.Reference] = c, c.ID
	resp, _ := json.Marshal(c)
	s.idem[key] = idemEntry{hash: hash, status: http.StatusCreated, body: resp}
	s.mu.Unlock()

	if scenario == "timeout_after_process" { // SUDAH diproses, tapi client tidak pernah tahu → unknown outcome
		s.hold(r)
	}
	writeRaw(w, http.StatusCreated, resp)
}

func (s *Server) hold(r *http.Request) {
	select {
	case <-time.After(s.cfg.HoldTimeout):
	case <-r.Context().Done():
	}
}

func (s *Server) getCharge(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, ok := s.charges[r.PathValue("id")]
	var out []byte
	if ok {
		out, _ = json.Marshal(c)
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "charge not found")
		return
	}
	writeRaw(w, http.StatusOK, out)
}

func (s *Server) findCharge(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	id, ok := s.byRef[r.URL.Query().Get("reference")]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "charge not found")
		return
	}
	r.SetPathValue("id", id)
	s.getCharge(w, r)
}

// ---------------------------------------------------------------- bank: transfer keluar (payout)

// Magic value rekening tujuan (14-api-external-contracts §1.7): E2E/Demo C memicu skenario dari
// nomor rekening, tanpa mengubah state global dan tanpa LedgerPay tahu.
var magicAccountSuffix = map[string]string{
	"0408": "timeout_after_process", // Demo C: uang terkirim, respons hilang
	"0409": "timeout_before_process",
	"0503": "error_5xx",
	"0422": "decline",
}

type createPayoutRequest struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
	Destination struct {
		BankCode      string `json:"bank_code"`
		AccountNumber string `json:"account_number"`
		HolderName    string `json:"holder_name"`
	} `json:"destination"`
}

// createPayout: bank yang benar TIDAK PERNAH memproses key yang sama dua kali. Key yang sudah ada
// menjawab status payout yang ada, apa pun skenarionya.
func (s *Server) createPayout(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	body, err := readAll(r)
	var in createPayoutRequest
	if err != nil || key == "" || json.Unmarshal(body, &in) != nil || in.Amount == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key and a valid body are required")
		return
	}
	s.mu.Lock()
	st := s.stats[key]
	if st == nil {
		st = &payoutStats{}
		s.stats[key] = st
	}
	st.received++
	if existing, ok := s.payouts[key]; ok {
		out := *existing
		s.mu.Unlock()
		writePayout(w, out)
		return
	}
	scenario := r.Header.Get("X-Mock-Scenario")
	if scenario == "" {
		scenario = s.scenario.Payouts
	}
	acct := in.Destination.AccountNumber
	if len(acct) >= 4 {
		if m, ok := magicAccountSuffix[acct[len(acct)-4:]]; ok {
			scenario = m
		}
	}
	switch scenario {
	case "error_5xx":
		s.mu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "bank temporarily unavailable")
		return
	case "timeout_before_process": // TIDAK diproses; bank tidak mengenal key ini (status query: 404)
		s.mu.Unlock()
		s.hold(r)
		writeErr(w, http.StatusGatewayTimeout, "timeout", "not processed")
		return
	}
	s.seq++
	now := s.cfg.Now()
	p := &bankPayout{ID: fmt.Sprintf("po_%s_%06d", s.epoch, s.seq), Reference: in.Reference, Amount: in.Amount, Currency: in.Currency, ProcessedAt: &now}
	if scenario == "decline" {
		p.Status, p.Reason = "FAILED", "account_closed"
	} else {
		p.Status = "SUCCEEDED"
		st.executed++ // uang keluar dari bank
	}
	s.payouts[key] = p
	out := *p
	s.mu.Unlock()

	switch scenario {
	case "timeout_after_process": // SUDAH dikirim, respons tidak pernah sampai → LedgerPay UNKNOWN
		s.hold(r)
	case "response_mismatch": // amandemen ADR-L06: respons sukses yang isinya tidak cocok
		out.Amount = out.Amount + "0"
	}
	writePayout(w, out)
}

func writePayout(w http.ResponseWriter, p bankPayout) {
	out, _ := json.Marshal(p)
	if p.Status == "FAILED" {
		writeErr(w, http.StatusUnprocessableEntity, "declined", p.Reason)
		return
	}
	writeRaw(w, http.StatusOK, out)
}

// getPayout: status query. Kontrak safe window: payout yang diterima bank selalu terlihat di sini
// paling lambat 5 menit setelah diterima (di mock: seketika).
func (s *Server) getPayout(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	disabled := s.scenario.PayoutStatusQuery == "disabled"
	p, ok := s.payouts[r.PathValue("key")]
	var out []byte
	if ok {
		out, _ = json.Marshal(p)
	}
	s.mu.Unlock()
	switch {
	case disabled:
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "status query disabled by scenario")
	case !ok:
		writeErr(w, http.StatusNotFound, "not_found", "unknown idempotency key")
	default:
		writeRaw(w, http.StatusOK, out)
	}
}

func (s *Server) getTransfers(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("idempotency_key")
	s.mu.Lock()
	st := s.stats[key]
	var executed, received int
	if st != nil {
		executed, received = st.executed, st.received
	}
	s.mu.Unlock()
	out, _ := json.Marshal(map[string]any{"idempotency_key": key, "transfers_executed": executed, "requests_received": received})
	writeRaw(w, http.StatusOK, out)
}

// PayoutStats mengembalikan (transfer yang dieksekusi, request yang diterima) untuk satu key (test).
func (s *Server) PayoutStats(key string) (executed, received int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.stats[key]; st != nil {
		return st.executed, st.received
	}
	return 0, 0
}

// ---------------------------------------------------------------- server merchant tiruan (demo webhook keluar)

// SinkHit adalah satu webhook LedgerPay yang diterima "server merchant" tiruan.
type SinkHit struct {
	EventID string          `json:"event_id"`
	Attempt string          `json:"attempt"`
	Body    json.RawMessage `json:"body"`
	At      time.Time       `json:"at"`
}

func (s *Server) sinkReceive(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	s.mu.Lock()
	s.sink = append(s.sink, SinkHit{EventID: r.Header.Get("LedgerPay-Event-Id"), Attempt: r.Header.Get("LedgerPay-Delivery-Attempt"), Body: body, At: s.cfg.Now()})
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) sinkList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out, _ := json.Marshal(append([]SinkHit{}, s.sink...))
	s.mu.Unlock()
	writeRaw(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- halaman checkout (untuk user / demo)

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, ok := s.charges[r.PathValue("id")]
	var status, amount, id string
	if ok {
		status, amount, id = c.Status, c.Amount, c.ID
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Tanpa form-action: Chrome menerapkan form-action juga pada REDIRECT setelah submit, padahal
	// tombol Bayar me-redirect ke return_url di origin SPA LedgerPay.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="id"><head><meta charset="utf-8"><title>MockPay</title>
<style>body{font-family:sans-serif;max-width:420px;margin:48px auto;padding:0 16px}button{font-size:16px;padding:10px 18px;margin-right:8px}</style></head>
<body><h1>MockPay</h1><p>Charge <code>%s</code></p><p>Nominal: <strong>%s</strong> (minor unit IDR)</p><p>Status: <strong id="status">%s</strong></p>
<form method="post" action="/checkout/%s/pay" style="display:inline"><button id="pay">Bayar</button></form>
<form method="post" action="/checkout/%s/fail" style="display:inline"><button id="fail">Gagalkan</button></form>
<p><small>Halaman ini tiruan. LedgerPay tidak pernah mempercayai halaman ini; hanya webhook + status query.</small></p></body></html>`,
		id, amount, status, id, id)
}

func (s *Server) checkoutAction(w http.ResponseWriter, r *http.Request) {
	c, err := s.complete(r.PathValue("id"), r.PathValue("action") == "pay")
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if c.returnURL != "" {
		http.Redirect(w, r, c.returnURL, http.StatusSeeOther)
		return
	}
	_, _ = fmt.Fprintf(w, "charge %s: %s", c.ID, c.Status)
}

// ---------------------------------------------------------------- admin (hanya jaringan compose / test)

func (s *Server) getScenario(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out, _ := json.Marshal(s.scenario)
	s.mu.Unlock()
	writeRaw(w, http.StatusOK, out)
}

func (s *Server) putScenario(w http.ResponseWriter, r *http.Request) {
	var sc Scenario
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if sc.Charges == "" {
		sc.Charges = "ok"
	}
	s.mu.Lock()
	s.scenario = sc
	s.mu.Unlock()
	s.getScenario(w, r)
}

func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.resetLocked()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminComplete(w http.ResponseWriter, r *http.Request) {
	c, err := s.complete(r.PathValue("id"), r.PathValue("action") == "pay")
	if err != nil {
		writeErr(w, http.StatusConflict, "invalid_state", err.Error())
		return
	}
	out, _ := json.Marshal(c)
	writeRaw(w, http.StatusOK, out)
}

func (s *Server) getDeliveries(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out, _ := json.Marshal(append([]Delivery{}, s.deliveries...))
	s.mu.Unlock()
	writeRaw(w, http.StatusOK, out)
}

// Deliveries mengembalikan salinan log pengiriman webhook (dipakai test).
func (s *Server) Deliveries() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery{}, s.deliveries...)
}

// ChargeCount mengembalikan jumlah charge yang pernah dibuat (bukti idempotency di test).
func (s *Server) ChargeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.charges)
}

// SetScenario mengganti skenario global (dipakai test tanpa HTTP).
func (s *Server) SetScenario(sc Scenario) {
	s.mu.Lock()
	s.scenario = sc
	s.mu.Unlock()
}

// Complete membayar/menggagalkan charge seperti tombol di halaman checkout (dipakai test).
func (s *Server) Complete(chargeID string, pay bool) error {
	_, err := s.complete(chargeID, pay)
	return err
}

// ---------------------------------------------------------------- penyelesaian charge + webhook

// Magic values (14-api-external-contracts §1.7): skenario dipilih dari NOMINAL top-up, sehingga
// E2E lewat LedgerPay bisa memicu kegagalan tanpa mengubah state global dan tanpa LedgerPay tahu.
var magicAmounts = map[string]string{
	"1111100": "webhook_duplicate:3",
	"2222200": "webhook_delay:25h",
	"3333300": "webhook_out_of_order",
}

const magicFailAmount = "4444400"

func (s *Server) complete(id string, pay bool) (Charge, error) {
	s.mu.Lock()
	c, ok := s.charges[id]
	if !ok {
		s.mu.Unlock()
		return Charge{}, fmt.Errorf("charge %s not found", id)
	}
	if c.Status != "PENDING" {
		s.mu.Unlock()
		return Charge{}, fmt.Errorf("charge %s is already %s", id, c.Status)
	}
	now := s.cfg.Now()
	eventType := "charge.succeeded"
	if pay && c.Amount != magicFailAmount {
		c.Status, c.PaidAt = "SUCCEEDED", &now
	} else {
		reason := "user_cancelled"
		c.Status, c.FailureReason, eventType = "FAILED", &reason, "charge.failed"
	}
	webhooks := append([]string{}, s.scenario.Webhooks...)
	if m, ok := magicAmounts[c.Amount]; ok {
		webhooks = append(webhooks, m)
	}
	main := s.newEventLocked(eventType, c)
	stale := s.newEventLocked("charge.pending", c) // hanya dikirim pada webhook_out_of_order
	snapshot := *c
	s.mu.Unlock()

	plan := deliveryPlan{events: []event{main}, copies: 1}
	for _, w := range webhooks {
		name, arg, _ := strings.Cut(w, ":")
		switch name {
		case "webhook_duplicate":
			if n, err := strconv.Atoi(arg); err == nil && n > 0 {
				plan.copies = n
			}
		case "webhook_delay":
			if d, err := time.ParseDuration(arg); err == nil {
				plan.delay = d
			}
		case "webhook_out_of_order": // event lama datang SETELAH event final
			plan.events = append(plan.events, stale)
		case "webhook_bad_signature":
			plan.badSig = true
		}
	}
	s.wg.Add(1)
	go s.deliver(snapshot.callbackURL, plan)
	return snapshot, nil
}

type event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"created_at"`
	Data      map[string]any `json:"data"`
	chargeID  string
}

func (s *Server) newEventLocked(typ string, c *Charge) event {
	s.seq++
	status := c.Status
	if typ == "charge.pending" {
		status = "PENDING"
	}
	return event{ID: fmt.Sprintf("evt_%s_%06d", s.epoch, s.seq), Type: typ, CreatedAt: s.cfg.Now(), chargeID: c.ID, Data: map[string]any{
		"charge_id": c.ID, "reference": c.Reference, "amount": c.Amount, "currency": c.Currency, "status": status,
		"paid_at": c.PaidAt, "failure_reason": c.FailureReason, "metadata": map[string]string{"correlation_id": c.correlationID},
	}}
}

type deliveryPlan struct {
	events []event
	copies int
	delay  time.Duration
	badSig bool
}

// Jadwal retry provider untuk respons 5xx/timeout (14-api-external-contracts §2.4). 4xx tidak di-retry.
var retrySchedule = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

func (s *Server) deliver(url string, p deliveryPlan) {
	defer s.wg.Done()
	if !s.sleep(p.delay) {
		return
	}
	for _, ev := range p.events {
		body, _ := json.Marshal(ev)
		for range p.copies {
			for attempt := 0; ; attempt++ {
				status := s.send(url, ev, body, attempt+1, p.badSig)
				if (status >= 200 && status < 500) || attempt >= len(retrySchedule) {
					break // 2xx selesai; 4xx permanen (signature/format) → tidak di-retry
				}
				if !s.sleep(retrySchedule[attempt]) {
					return
				}
			}
		}
	}
}

func (s *Server) send(url string, ev event, body []byte, attempt int, badSig bool) int {
	secret := []byte(s.cfg.WebhookSecret)
	if badSig {
		secret = []byte("not-the-real-secret")
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, url, bytes.NewReader(body))
	status := 0
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Provider-Signature", webhooksig.Sign(secret, s.cfg.Now(), body))
		req.Header.Set("Provider-Event-Id", ev.ID)
		if resp, err := s.cfg.HTTPClient.Do(req); err == nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
	}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, Delivery{EventID: ev.ID, EventType: ev.Type, ChargeID: ev.chargeID, Attempt: attempt, Status: status, BadSig: badSig, At: s.cfg.Now()})
	s.mu.Unlock()
	s.cfg.Logger.Info("mock.webhook_sent", "event_id", ev.ID, "type", ev.Type, "attempt", attempt, "status", status)
	return status
}

func (s *Server) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-time.After(time.Duration(float64(d) * s.cfg.TimeScale)):
		return true
	case <-s.ctx.Done():
		return false
	}
}

// ---------------------------------------------------------------- util

// Error mock meniru PG pada umumnya ({"error":{code,message}}), bukan RFC 9457: adapter LedgerPay
// yang menerjemahkannya ke domain.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	out, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	writeRaw(w, status, out)
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func readAll(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(http.MaxBytesReader(nil, r.Body, 64<<10))
	return b.Bytes(), err
}
