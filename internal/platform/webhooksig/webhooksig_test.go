package webhooksig

import (
	"errors"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	secret, old := []byte("whsec_current"), []byte("whsec_old")
	body := []byte(`{"id":"evt_1","type":"charge.succeeded"}`)
	sig := Sign(secret, now, body)

	cases := []struct {
		name    string
		header  string
		body    []byte
		secrets [][]byte
		at      time.Time
		want    error
	}{
		{"valid", sig, body, [][]byte{secret}, now, nil},
		{"rotation: receiver knows old and new secret", sig, body, [][]byte{old, secret}, now, nil},
		{"rotation: sender sends two v1", Sign(old, now, body) + ",v1=" + sig[len("t=1790946000,v1="):], body, [][]byte{secret}, now, nil},
		{"body changed by one byte", sig, []byte(`{"id":"evt_1","type":"charge.succeedeD"}`), [][]byte{secret}, now, ErrBadSignature},
		{"wrong secret", sig, body, [][]byte{old}, now, ErrBadSignature},
		{"replay 6 minutes later", sig, body, [][]byte{secret}, now.Add(6 * time.Minute), ErrStale},
		{"timestamp from the future", sig, body, [][]byte{secret}, now.Add(-6 * time.Minute), ErrStale},
		{"garbage", "nope", body, [][]byte{secret}, now, ErrMalformed},
		{"missing v1", "t=1790946000", body, [][]byte{secret}, now, ErrMalformed},
	}
	for _, c := range cases {
		if _, err := Verify(c.header, c.body, c.secrets, c.at, 5*time.Minute); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
}

// Mengubah timestamp di header (untuk melewati window) merusak signature, karena t ikut di-HMAC.
func TestVerify_TamperedTimestampFails(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	secret, body := []byte("s"), []byte(`{}`)
	sig := Sign(secret, now.Add(-time.Hour), body)            // rekaman 1 jam lalu
	forged := "t=" + "1790946000" + sig[len("t=1790942400"):] // ganti t dengan "sekarang"
	if _, err := Verify(forged, body, [][]byte{secret}, now, 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered timestamp must break the signature, got %v", err)
	}
}
