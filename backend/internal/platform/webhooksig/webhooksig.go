// Package webhooksig: tanda tangan webhook "t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + raw_body)>"
// (docs/design/14-api-external-contracts.md §2.1). Dipakai untuk memverifikasi webhook provider
// (Sprint 5), untuk menandatangani di mock-provider, dan nanti webhook keluar ke merchant (Sprint 6).
//
// Kenapa timestamp ikut ditandatangani: tanpa itu, request yang pernah direkam penyerang bisa
// dikirim ulang kapan saja. Dengan t di dalam HMAC dan batas |now − t| ≤ 5 menit, rekaman lama
// ditolak; replay di dalam window ditangani dedupe provider_event_id.
package webhooksig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMalformed    = errors.New("webhooksig: malformed signature header")
	ErrBadSignature = errors.New("webhooksig: signature mismatch")
	ErrStale        = errors.New("webhooksig: timestamp outside tolerance")
)

// Sign menghasilkan nilai header Provider-Signature.
func Sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

// Verify memeriksa header terhadap raw body. Boleh ada beberapa v1= (rotasi di sisi pengirim) dan
// beberapa secret (rotasi di sisi penerima): valid jika ada pasangan yang cocok. Perbandingan
// constant-time (hmac.Equal). Signature diperiksa SEBELUM timestamp, supaya penyerang tanpa
// secret tidak bisa memakai pesan error untuk mempelajari apa pun.
func Verify(header string, body []byte, secrets [][]byte, now time.Time, tolerance time.Duration) (time.Time, error) {
	var ts string
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return time.Time{}, ErrMalformed
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil {
				return time.Time{}, ErrMalformed
			}
			sigs = append(sigs, b)
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return time.Time{}, ErrMalformed
	}
	valid := false
	for _, secret := range secrets {
		want, _ := hex.DecodeString(mac(secret, ts, body))
		for _, got := range sigs {
			if hmac.Equal(got, want) {
				valid = true
			}
		}
	}
	if !valid {
		return time.Time{}, ErrBadSignature
	}
	t := time.Unix(unix, 0).UTC()
	if d := now.Sub(t); d > tolerance || d < -tolerance {
		return t, ErrStale
	}
	return t, nil
}

func mac(secret []byte, ts string, body []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
