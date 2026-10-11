import { defineStore } from 'pinia'
import { api, ApiError } from '../api'

// Sandbox pengunjung demo publik (hanya di image demo). Di build release GET /demo = 404 → semua UI demo
// tersembunyi, jadi SPA yang sama bisa dipakai di kedua build.
export type DemoContact = { name: string; email: string }
export type DemoBill = { id: string; merchant_name: string; amount: string; description: string | null; status: string }
export type Sandbox = { expires_at: string; contacts: DemoContact[]; pending_bill: DemoBill | null }

export const useDemo = defineStore('demo', {
  state: () => ({ available: null as boolean | null, ttlHours: 24, sandbox: null as Sandbox | null }),
  getters: {
    /** Tagihan merchant yang masih bisa dibayar (sudah dibayar / kedaluwarsa → tidak ditampilkan). */
    openBill: (s) => (s.sandbox?.pending_bill?.status === 'REQUIRES_CONFIRMATION' ? s.sandbox.pending_bill : null),
  },
  actions: {
    async detect(): Promise<boolean> {
      if (this.available === null) {
        try {
          const res = await fetch('/api/v1/demo')
          this.available = res.ok
          if (res.ok) this.ttlHours = Math.round(((await res.json()).ttl_seconds ?? 86400) / 3600)
        } catch {
          this.available = false
        }
      }
      return this.available
    },
    /** Info sandbox user yang login; user biasa (bukan pengunjung demo) → null. */
    async load() {
      try {
        this.sandbox = await api<Sandbox>('/demo/sandbox')
      } catch (e) {
        this.sandbox = null
        if (!(e instanceof ApiError && e.status === 404)) throw e
      }
    },
    clear() {
      this.sandbox = null
    },
  },
})
