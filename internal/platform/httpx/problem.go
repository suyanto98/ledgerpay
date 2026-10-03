package httpx

import (
	"encoding/json"
	"net/http"
	"strings"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/observability"
)

// Problem adalah body RFC 9457 (docs/design/11-api-conventions.md §4).
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Code      string              `json:"code"`
	Detail    string              `json:"detail,omitempty"`
	Instance  string              `json:"instance,omitempty"`
	RequestID string              `json:"request_id"`
	Errors    []apperr.FieldError `json:"errors,omitempty"`
}

const problemTypeBase = "https://ledgerpay.dev/errors/"

// WriteError mengubah error apa pun menjadi respons problem+json.
// Error tak dikenal → 500 INTERNAL_ERROR dengan penyebab hanya di log (tidak bocor ke client).
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	if e.Status >= 500 {
		observability.Logger(r.Context()).Error("request.failed", "code", e.Code, "err", err.Error())
	}
	p := Problem{
		Type:      problemTypeBase + strings.ToLower(strings.ReplaceAll(e.Code, "_", "-")),
		Title:     e.Title,
		Status:    e.Status,
		Code:      e.Code,
		Detail:    e.Detail,
		Instance:  r.URL.Path,
		RequestID: RequestIDFrom(r.Context()),
		Errors:    e.Fields,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON menulis respons JSON sukses. Semua respons API memakai no-store (data finansial; ADR-008).
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
