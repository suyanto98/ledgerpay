import { defineStore } from 'pinia'

// Endpoint cookie (refresh/logout) wajib X-Requested-With: fetch (CSRF guard, C-08).
const cookieHeaders = { 'X-Requested-With': 'fetch' }

export const useAuth = defineStore('auth', {
  state: () => ({ token: '' as string, email: '' as string, roles: [] as string[], ready: false }),
  getters: { loggedIn: (s) => s.token !== '', isOps: (s) => s.roles.includes('OPS') },
  actions: {
    async login(email: string, password: string) {
      const res = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password }),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.code === 'RATE_LIMITED' ? 'Terlalu banyak percobaan, coba lagi nanti' : 'Email atau password salah')
      this.token = data.access_token
      this.email = email
      await this.loadMe()
    },
    /** Role dari /me hanya untuk menampilkan menu; server tetap menegakkan otorisasi. */
    async loadMe() {
      const res = await fetch('/api/v1/me', { headers: { Authorization: `Bearer ${this.token}` } })
      if (res.ok) this.roles = (await res.json()).roles ?? []
    },
    /** Memulihkan sesi dari cookie refresh HttpOnly (dipanggil saat aplikasi dibuka / token kedaluwarsa). */
    async refresh(): Promise<boolean> {
      const res = await fetch('/api/v1/auth/refresh', { method: 'POST', headers: cookieHeaders })
      if (!res.ok) {
        this.token = ''
        return false
      }
      this.token = (await res.json()).access_token
      await this.loadMe()
      return true
    },
    async logout() {
      await fetch('/api/v1/auth/logout', { method: 'POST', headers: cookieHeaders })
      this.token = ''
      this.email = ''
      this.roles = []
    },
  },
})
