package topup

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

// Provider adalah payment gateway dari sudut pandang LedgerPay. Kode produksi tidak tahu apakah
// di baliknya mock-provider atau PG sungguhan; test service memakai fake in-process (ADR-L05).
type Provider interface {
	// CreateCharge idempoten dengan key = topup id: dipanggil ulang setelah timeout aman.
	CreateCharge(ctx context.Context, in CreateChargeInput) (Charge, error)
	GetCharge(ctx context.Context, chargeID string) (Charge, error)
}

type CreateChargeInput struct {
	TopupID       string
	Amount        int64
	Currency      string
	CallbackURL   string
	ReturnURL     string
	ExpiresAt     time.Time
	CorrelationID string
}

type Charge struct {
	ID          string
	Reference   string
	Status      string // PENDING, SUCCEEDED, FAILED, EXPIRED
	Amount      int64
	Currency    string
	CheckoutURL string
}

// ErrChargeNotFound: provider menjawab 404 (charge belum pernah dibuat / ID tidak dikenal).
var ErrChargeNotFound = errors.New("topup: charge not found at provider")

// HTTPProvider adalah klien HTTP kontrak mock-provider (docs/design/14-api-external-contracts.md §1).
type HTTPProvider struct {
	baseURL string
	secret  string
	client  *http.Client
}

// NewHTTPProvider: timeout total 10 dtk (docs/design/19-tx-concurrency.md §6). Panggilan ini
// SELALU di luar transaksi DB, jadi provider yang lambat tidak pernah menahan lock.
func NewHTTPProvider(baseURL, secret string) *HTTPProvider {
	return NewHTTPProviderTimeout(baseURL, secret, 10*time.Second)
}

// NewHTTPProviderTimeout: timeout kustom (test memakai nilai kecil untuk mensimulasikan provider timeout).
func NewHTTPProviderTimeout(baseURL, secret string, timeout time.Duration) *HTTPProvider {
	return &HTTPProvider{baseURL: strings.TrimRight(baseURL, "/"), secret: secret, client: &http.Client{Timeout: timeout}}
}

type chargeJSON struct {
	ID          string `json:"id"`
	Reference   string `json:"reference"`
	Status      string `json:"status"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	CheckoutURL string `json:"checkout_url"`
}

func (p *HTTPProvider) CreateCharge(ctx context.Context, in CreateChargeInput) (Charge, error) {
	body, _ := json.Marshal(map[string]any{
		"amount": strconv.FormatInt(in.Amount, 10), "currency": in.Currency, "reference": in.TopupID,
		"callback_url": in.CallbackURL, "return_url": in.ReturnURL, "expires_at": in.ExpiresAt.UTC(),
		"metadata": map[string]string{"correlation_id": in.CorrelationID},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/charges", bytes.NewReader(body)) //nolint:gosec // G704: base URL dari konfigurasi server, path konstan
	if err != nil {
		return Charge{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", in.TopupID) // key ke provider = topup id (ADR-L02 pola b)
	return p.do(req)
}

func (p *HTTPProvider) GetCharge(ctx context.Context, chargeID string) (Charge, error) {
	// chargeID berasal dari payload webhook (input luar): di-escape supaya nilai seperti
	// "../admin/reset" tidak bisa membelokkan request verify-by-fetch ke endpoint lain.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/charges/"+url.PathEscape(chargeID), nil) //nolint:gosec // G704: host dari konfigurasi; segmen path di-escape
	if err != nil {
		return Charge{}, err
	}
	return p.do(req)
}

func (p *HTTPProvider) do(req *http.Request) (Charge, error) {
	req.Header.Set("Authorization", "Bearer "+p.secret)
	resp, err := p.client.Do(req) //nolint:gosec // G704: URL dari konfigurasi server (PROVIDER_BASE_URL), bukan input user
	if err != nil {
		return Charge{}, fmt.Errorf("topup: provider unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Charge{}, ErrChargeNotFound
	}
	if resp.StatusCode/100 != 2 {
		return Charge{}, fmt.Errorf("topup: provider responded %d", resp.StatusCode)
	}
	var c chargeJSON
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return Charge{}, fmt.Errorf("topup: provider response: %w", err)
	}
	amount, err := strconv.ParseInt(c.Amount, 10, 64)
	if err != nil {
		return Charge{}, fmt.Errorf("topup: provider amount %q: %w", c.Amount, err)
	}
	return Charge{ID: c.ID, Reference: c.Reference, Status: c.Status, Amount: amount, Currency: c.Currency, CheckoutURL: c.CheckoutURL}, nil
}
