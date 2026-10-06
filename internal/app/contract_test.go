package app_test

import (
	"net/http"
	"strings"
	"testing"
)

// moneyKey: nama field yang berisi uang di respons JSON.
func moneyKey(k string) bool {
	return k == "amount" || k == "balance" || strings.HasSuffix(k, "_amount")
}

// assertMoneyIsString menelusuri JSON secara rekursif: setiap field uang harus string (atau null), tidak
// pernah number. Angka JSON = float64 di banyak client (JS kehilangan presisi di atas 2^53).
func assertMoneyIsString(t *testing.T, where string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if moneyKey(k) {
				if _, ok := val.(string); !ok && val != nil {
					t.Errorf("%s: field %q is %T (%v), want a string", where, k, val, val)
				}
			}
			assertMoneyIsString(t, where+"."+k, val)
		}
	case []any:
		for _, it := range x {
			assertMoneyIsString(t, where+"[]", it)
		}
	}
}

// TestContract_MoneyIsString (LI-11, DoD V3): uang selalu string minor unit di JSON, di semua jenis respons
// yang memuat uang (wallet, riwayat, transfer, intent + refund, payout, saldo merchant, error 422).
func TestContract_MoneyIsString(t *testing.T) {
	e := newEnv(t)
	k, _, intentID, rina := paidIntent(t, e, "30000")
	login(t, e, "andi@example.com")
	responses := map[string]map[string]any{}
	get := func(name, path, token string) {
		_, b := e.do(t, http.MethodGet, path, token, nil)
		responses[name] = b
	}
	_, responses["transfer"] = e.transfer(t, rina, newKey(), transferBody("andi@example.com", "1000"))
	_, responses["refund"] = e.refund(t, k, intentID, newKey(), "1000")
	_, responses["refund_exceeds_422"] = e.refund(t, k, intentID, newKey(), "999999")
	_, responses["payout"] = e.createPayout(t, rina, "1000000", "9900001234")
	get("wallet", "/api/v1/wallet", rina)
	get("history", "/api/v1/wallet/transactions", rina)
	get("intent_payer", "/api/v1/payment-intents/"+intentID, rina)
	_, responses["intent_merchant"] = e.doH(t, http.MethodGet, "/api/v1/merchant/payment-intents/"+intentID, k, nil, nil)
	_, responses["merchant_balance"] = e.doH(t, http.MethodGet, "/api/v1/merchant/balance", k, nil, nil)
	for name, body := range responses {
		if len(body) == 0 {
			t.Fatalf("%s: empty response", name)
		}
		assertMoneyIsString(t, name, body)
	}
	if responses["refund_exceeds_422"]["refundable_amount"] == nil {
		t.Fatal("sanity: the 422 response should carry refundable_amount (otherwise the error path is not checked)")
	}
}
