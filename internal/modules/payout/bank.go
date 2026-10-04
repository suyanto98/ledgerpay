package payout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Outcome adalah hasil submit menurut LedgerPay (docs/design/14-api-external-contracts.md §1.4).
type Outcome int

const (
	// OutcomeUnknown: timeout, 5xx, koneksi putus, 400/401 (bug kita), atau respons sukses yang
	// isinya tidak cocok. Bank MUNGKIN sudah mengirim uang → tidak boleh reversal, tidak boleh
	// submit ulang buta. Diselesaikan lewat status query (ADR-L06).
	OutcomeUnknown Outcome = iota
	OutcomeSucceeded
	OutcomeFailed     // penolakan DEFINITIF (422 declined) → reversal aman
	OutcomeProcessing // 202: diterima, hasil menyusul
)

type SubmitInput struct {
	PayoutID      string // juga Idempotency-Key ke bank
	Amount        int64
	Currency      string
	BankCode      string
	AccountNumber string
	HolderName    string
}

type SubmitResult struct {
	Outcome  Outcome
	Detail   string // alasan (untuk last_error / log)
	Mismatch bool   // respons "sukses" yang isinya tidak cocok dengan catatan lokal (amandemen ADR-L06)
}

// StatusAnswer: jawaban status query.
type StatusAnswer string

const (
	StatusSucceeded  StatusAnswer = "SUCCEEDED"
	StatusFailed     StatusAnswer = "FAILED"
	StatusProcessing StatusAnswer = "PROCESSING"
	StatusNotFound   StatusAnswer = "NOT_FOUND"
)

// Bank adalah sisi bank dari sudut pandang LedgerPay (mock-provider di V1, ADR-L05).
type Bank interface {
	Submit(ctx context.Context, in SubmitInput) SubmitResult
	Status(ctx context.Context, payoutID string) (StatusAnswer, error)
}

// HTTPBank: klien kontrak bank mock. Panggilan SELALU di luar transaksi DB.
type HTTPBank struct {
	baseURL, secret string
	client          *http.Client
}

func NewHTTPBank(baseURL, secret string, timeout time.Duration) *HTTPBank {
	return &HTTPBank{baseURL: strings.TrimRight(baseURL, "/"), secret: secret, client: &http.Client{Timeout: timeout}}
}

type bankPayoutJSON struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
}

func (b *HTTPBank) Submit(ctx context.Context, in SubmitInput) SubmitResult {
	body, _ := json.Marshal(map[string]any{
		"amount": strconv.FormatInt(in.Amount, 10), "currency": in.Currency, "reference": in.PayoutID,
		"destination": map[string]string{"bank_code": in.BankCode, "account_number": in.AccountNumber, "holder_name": in.HolderName},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/payouts", bytes.NewReader(body)) //nolint:gosec // G704: host dari konfigurasi, path konstan
	if err != nil {
		return SubmitResult{Outcome: OutcomeUnknown, Detail: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", in.PayoutID) // bank tidak pernah memproses key yang sama dua kali
	req.Header.Set("Authorization", "Bearer "+b.secret)
	resp, err := b.client.Do(req) //nolint:gosec // G704: lihat di atas
	if err != nil {
		return SubmitResult{Outcome: OutcomeUnknown, Detail: "bank unreachable or timed out: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var p bankPayoutJSON
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return SubmitResult{Outcome: OutcomeUnknown, Detail: "unreadable success response"}
		}
		// Amandemen ADR-L06: sukses yang isinya tidak cocok dengan catatan lokal BUKAN sukses.
		if p.Reference != in.PayoutID || p.Amount != strconv.FormatInt(in.Amount, 10) || p.Currency != in.Currency || p.Status != "SUCCEEDED" {
			return SubmitResult{Outcome: OutcomeUnknown, Mismatch: true,
				Detail: fmt.Sprintf("mismatched bank response: ref=%s amount=%s %s status=%s", p.Reference, p.Amount, p.Currency, p.Status)}
		}
		return SubmitResult{Outcome: OutcomeSucceeded}
	case http.StatusAccepted:
		return SubmitResult{Outcome: OutcomeProcessing}
	case http.StatusUnprocessableEntity:
		return SubmitResult{Outcome: OutcomeFailed, Detail: "declined by bank"}
	default: // 400/401 = bug kita; 5xx = bank bermasalah. Keduanya: tidak bisa dipastikan → UNKNOWN
		return SubmitResult{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("bank responded %d", resp.StatusCode)}
	}
}

var errStatusUnavailable = errors.New("payout: bank status query unavailable")

func (b *HTTPBank) Status(ctx context.Context, payoutID string) (StatusAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/payouts/"+url.PathEscape(payoutID), nil) //nolint:gosec // G704: host dari konfigurasi, segmen di-escape
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.secret)
	resp, err := b.client.Do(req) //nolint:gosec // G704: lihat di atas
	if err != nil {
		return "", fmt.Errorf("%w: %w", errStatusUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return StatusNotFound, nil
	case http.StatusOK:
		var p bankPayoutJSON
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return "", err
		}
		return StatusAnswer(p.Status), nil
	}
	return "", fmt.Errorf("%w: HTTP %d", errStatusUnavailable, resp.StatusCode)
}
