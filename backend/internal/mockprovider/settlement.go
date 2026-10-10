package mockprovider

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WIB: Asia/Jakarta tanpa DST. FixedZone, bukan LoadLocation: image distroless tidak membawa tzdata.
var WIB = time.FixedZone("WIB", 7*3600)

// SettlementHeader adalah kolom file settlement (docs/design/14-api-external-contracts.md §1.6), urutan tetap.
var SettlementHeader = []string{"business_date", "type", "provider_ref", "reference", "amount", "currency", "status", "settled_at", "fee"}

// getSettlement: GET /v1/settlements/{date}.csv. Berisi charge yang SUCCEEDED (uang masuk) dan payout bank
// yang final (SUCCEEDED/FAILED) yang settled pada business_date (WIB). Skenario `settlement` membuat file
// sengaja berbeda untuk Demo D: settlement_mismatch:missing | amount | status | extra.
func (s *Server) getSettlement(w http.ResponseWriter, r *http.Request) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(r.PathValue("name"), ".csv"), WIB)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "use /v1/settlements/YYYY-MM-DD.csv")
		return
	}
	from, to := day, day.Add(24*time.Hour)
	date := day.Format("2006-01-02")
	in := func(t *time.Time) bool { return t != nil && !t.Before(from) && t.Before(to) }

	s.mu.Lock()
	var rows [][]string
	for _, c := range s.charges {
		if c.Status == "SUCCEEDED" && in(c.PaidAt) {
			rows = append(rows, []string{date, "CHARGE", c.ID, c.Reference, c.Amount, c.Currency, c.Status, c.PaidAt.UTC().Format(time.RFC3339), "0"})
		}
	}
	for key, p := range s.payouts {
		if in(p.ProcessedAt) { // key = Idempotency-Key = payout id LedgerPay (referensi matching)
			rows = append(rows, []string{date, "PAYOUT", p.ID, key, p.Amount, p.Currency, p.Status, p.ProcessedAt.UTC().Format(time.RFC3339), "0"})
		}
	}
	scenario := s.scenario.Settlement
	s.mu.Unlock()

	sort.Slice(rows, func(i, j int) bool { return rows[i][7]+rows[i][2] < rows[j][7]+rows[j][2] })
	rows = applySettlementScenario(rows, scenario, date)

	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write(SettlementHeader)
	_ = cw.WriteAll(rows)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// applySettlementScenario mengubah baris PERTAMA (urutan deterministik) sesuai skenario.
func applySettlementScenario(rows [][]string, scenario, date string) [][]string {
	kind, _ := strings.CutPrefix(scenario, "settlement_mismatch:")
	if scenario == "" || kind == scenario {
		return rows
	}
	if kind == "extra" { // baris yang tidak dikenal LedgerPay → MISSING_INTERNAL
		return append(rows, []string{date, "CHARGE", "ch_unknown_000001", "00000000-0000-7000-8000-000000000001", "1000000", "IDR", "SUCCEEDED", date + "T05:00:00Z", "0"})
	}
	if len(rows) == 0 {
		return rows
	}
	switch kind {
	case "missing": // provider tidak mencatat transaksi yang ada di LedgerPay → MISSING_EXTERNAL
		return rows[1:]
	case "amount":
		if n, err := strconv.ParseInt(rows[0][4], 10, 64); err == nil {
			rows[0][4] = strconv.FormatInt(n+100, 10)
		}
	case "status":
		if rows[0][6] == "SUCCEEDED" {
			rows[0][6] = "FAILED"
		} else {
			rows[0][6] = "SUCCEEDED"
		}
	}
	return rows
}
