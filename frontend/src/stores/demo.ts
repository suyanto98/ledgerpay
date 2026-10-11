import { defineStore } from 'pinia'
import { api, ApiError } from '../api'

// Sandbox pengunjung demo publik (hanya di image demo). Di build release GET /demo = 404 → semua UI demo
// tersembunyi, jadi SPA yang sama bisa dipakai di kedua build.
export type DemoContact = { name: string; email: string }
export type DemoBill = { id: string; merchant_name: string; amount: string; description: string | null; status: string }
export type Sandbox = { expires_at: string; contacts: DemoContact[]; pending_bill: DemoBill | null }

/** Langkah tur demo. Tercentang otomatis saat benar-benar dilakukan (bukan saat diklik). */
export type TourKey = 'transfer' | 'pay' | 'double' | 'stress' | 'journal' | 'ledger'
export type TourStep = { key: TourKey; to: string; title: string; why: string }
export const tour: TourStep[] = [
  { key: 'transfer', to: '/transfer', title: 'Kirim uang ke Siti', why: 'Tombol tertekan dua kali atau sinyal putus tidak membuat uangmu terkirim dua kali.' },
  { key: 'pay', to: '/', title: 'Bayar tagihan Kopi Senja', why: 'Seperti bayar di toko online. Tagihan yang dibuka di dua tab pun tidak bisa terbayar dua kali.' },
  { key: 'double', to: '/lab', title: 'Uji klik ganda', why: 'Kamu lihat sendiri: dua permintaan yang sama, saldo terpotong sekali.' },
  { key: 'stress', to: '/lab', title: 'Uji 100 transfer di detik yang sama', why: 'Saat ramai (gajian, promo), saldo tetap tepat dan tidak pernah minus.' },
  { key: 'journal', to: '/history', title: 'Buka catatan sebuah transaksi', why: 'Setiap rupiah dicatat di dua sisi seperti pembukuan bank, jadi selisih sekecil apa pun langsung ketahuan.' },
  { key: 'ledger', to: '/lab', title: 'Periksa buku besar dompetmu', why: 'Saldo yang kamu lihat dihitung ulang dari semua catatan dan harus cocok sampai rupiah terakhir.' },
]

export const useDemo = defineStore('demo', {
  state: () => ({ available: null as boolean | null, ttlHours: 24, sandbox: null as Sandbox | null, done: [] as TourKey[], showSummary: false }),
  getters: {
    /** Tagihan merchant yang masih bisa dibayar (sudah dibayar / kedaluwarsa → tidak ditampilkan). */
    openBill: (s) => (s.sandbox?.pending_bill?.status === 'REQUIRES_CONFIRMATION' ? s.sandbox.pending_bill : null),
    finished: (s) => tour.every((t) => s.done.includes(t.key)),
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
        this.done = readTour(this.sandbox.expires_at)
      } catch (e) {
        this.sandbox = null
        if (!(e instanceof ApiError && e.status === 404)) throw e
      }
    },
    clear() {
      this.sandbox = null
      this.done = []
      this.showSummary = false
    },
    /** Tandai langkah tur selesai (hanya di sandbox). Selesai semua → rangkuman tampil. */
    mark(key: TourKey) {
      if (!this.sandbox || this.done.includes(key)) return
      this.done = [...this.done, key]
      writeTour(this.sandbox.expires_at, this.done)
      if (this.finished) this.showSummary = true
    },
  },
})

// Progres tur per sandbox di localStorage: kenyamanan per penonton. Storage diblokir → tur tetap jalan, tidak diingat.
function tourKey(sandboxExpiry: string) {
  return 'lp_demo_tour:' + sandboxExpiry
}
function readTour(sandboxExpiry: string): TourKey[] {
  try {
    const v = JSON.parse(localStorage.getItem(tourKey(sandboxExpiry)) ?? '[]')
    return Array.isArray(v) ? v.filter((k) => tour.some((t) => t.key === k)) : []
  } catch {
    return []
  }
}
function writeTour(sandboxExpiry: string, done: TourKey[]) {
  try {
    localStorage.setItem(tourKey(sandboxExpiry), JSON.stringify(done))
  } catch {
    /* mode privat */
  }
}
