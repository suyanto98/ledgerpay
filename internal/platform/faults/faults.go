//go:build !demo

// Package faults: titik injeksi kegagalan untuk DEMO (docs/design/24-docker-compose.md §9, O15/O18).
// Di titik bernama (contoh "payout.after_bank_call") worker demo bisa diminta mati seketika
// (os.Exit(137), seperti kill -9) supaya Demo C deterministik, bukan bergantung kecepatan tangan.
//
// Build ini (tanpa tag `demo`) adalah build RELEASE: Check kosong dan di-inline, tidak ada kode yang
// membaca tabel demo_faults, dan tidak ada cara mengaktifkannya lewat env (lapis guard "build").
package faults

import (
	"context"

	"ledgerpay/internal/platform/db"
)

// Check adalah titik injeksi bernama. Di build release tidak melakukan apa pun.
func Check(context.Context, db.DBTX, string) {}

// Enabled melaporkan apakah binary ini dibangun dengan tag `demo`.
func Enabled() bool { return false }
