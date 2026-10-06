// Klien API tipis. Access token hanya di memori (store auth), tidak pernah localStorage: script
// pihak ketiga yang lolos XSS tidak bisa membacanya dari storage (ADR-009).
import { useAuth } from './stores/auth'

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public fields: { field: string; code: string }[] = []) {
    super(message)
  }
}

type Options = { method?: string; body?: unknown; idempotencyKey?: string; retryOnExpired?: boolean }

export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const auth = useAuth()
  const headers: Record<string, string> = {}
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  const res = await fetch('/api/v1' + path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  })
  // Access token 15 menit kedaluwarsa → refresh sekali lalu ulangi (aman: request ber-key idempoten).
  if (res.status === 401 && opts.retryOnExpired !== false && auth.token) {
    const p = await res.clone().json().catch(() => ({}))
    if (p.code === 'TOKEN_EXPIRED' && (await auth.refresh())) return api<T>(path, { ...opts, retryOnExpired: false })
  }
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, data.code ?? 'UNKNOWN', data.detail ?? data.title ?? res.statusText, data.errors)
  return data as T
}

/** Satu niat user = satu key. Dibuat ulang hanya saat isi form berubah (retry memakai key yang sama). */
export function newIdempotencyKey(): string {
  // randomUUID hanya ada di secure context (HTTPS / localhost). Dibuka lewat http://<IP-LAN> (tes dari
  // HP) fungsinya tidak ada → fallback UUID v4 dari getRandomValues (tetap CSPRNG, ada di semua context).
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return uuidV4(crypto.getRandomValues(new Uint8Array(16)))
}

/** UUID v4 (RFC 9562) dari 16 byte acak. */
export function uuidV4(b: Uint8Array): string {
  b[6] = (b[6] & 0x0f) | 0x40 // versi 4
  b[8] = (b[8] & 0x3f) | 0x80 // varian RFC
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}
