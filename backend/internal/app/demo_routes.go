//go:build demo

package app

import (
	"crypto/subtle"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/httpx"
)

// demoPoints: titik fault yang boleh dipasang (sama dengan ck_demo_faults_point).
var demoPoints = map[string]bool{"payout.after_bank_call": true, "inbox.before_apply": true}

// registerDemoRoutes (build tag `demo`): /api/v1/demo/faults hanya kalau DEMO_MODE=true dan token ada
// (lapis "runtime"), dan setiap request wajib X-Demo-Token yang cocok (lapis "request"). Token salah →
// 404, sama dengan rute yang tidak ada: endpoint demo tidak bisa ditemukan pengunjung demo publik.
func registerDemoRoutes(authed chi.Router, d Deps) {
	if !d.DemoMode || d.DemoToken == "" {
		return
	}
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Demo-Token")), []byte(d.DemoToken)) != 1 {
				httpx.WriteError(w, r, apperr.ErrNotFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	demo := authed.With(guard)
	demo.Post("/demo/faults", func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		var in struct {
			Point string `json:"point"`
			Times int    `json:"times"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if in.Times == 0 {
			in.Times = 1
		}
		if !demoPoints[in.Point] || in.Times < 1 || in.Times > 10 {
			httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "point", Code: "INVALID_VALUE",
				Message: "payout.after_bank_call | inbox.before_apply; times 1–10"}))
			return
		}
		var id int64
		if err := d.Pool.QueryRow(r.Context(), `INSERT INTO demo_faults (point, remaining, created_by) VALUES ($1, $2, $3) RETURNING id`,
			in.Point, in.Times, p.UserID).Scan(&id); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d.Logger.Warn("demo.fault_armed", "point", in.Point, "times", in.Times, "user_id", p.UserID.String())
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "point": in.Point, "remaining": in.Times})
	})
	demo.Delete("/demo/faults", func(w http.ResponseWriter, r *http.Request) {
		if _, err := d.Pool.Exec(r.Context(), `DELETE FROM demo_faults`); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
