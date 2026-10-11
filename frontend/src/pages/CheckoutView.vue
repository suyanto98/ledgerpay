<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor } from '../money'
import QRCode from 'qrcode'
import { useDemo } from '../stores/demo'

// Halaman checkout merchant (LF-14): link checkout_url dari merchant → user login → bayar dari wallet.
// Nominal TIDAK pernah dikirim dari browser: server mengambilnya dari intent (body confirm = {}).
type Intent = {
  id: string; merchant: { name: string }; merchant_order_id: string; amount: string; currency: string
  description: string | null; status: string; expires_at: string; confirmed_at?: string
}
const route = useRoute()
const intent = ref<Intent | null>(null)
const balance = ref<string | null>(null)
const error = ref('')
const busy = ref(false)
const notFound = ref(false)
// Satu halaman checkout = satu niat bayar = satu key. Klik ganda / retry setelah jaringan putus
// memakai key yang sama → server me-replay, saldo tidak terpotong dua kali.
const key = newIdempotencyKey()

const messages: Record<string, string> = {
  INSUFFICIENT_BALANCE: 'Saldo tidak cukup. Top-up dulu, lalu kembali ke halaman ini.',
  PAYMENT_EXPIRED: 'Pembayaran ini sudah kedaluwarsa. Minta link baru ke merchant.',
  INVALID_STATE_TRANSITION: 'Pembayaran ini sudah tidak bisa dibayar (mungkin sudah dibayar di tab lain).',
  RATE_LIMITED: 'Terlalu banyak transaksi, coba lagi sebentar lagi',
}

async function load() {
  try {
    intent.value = await api<Intent>(`/payment-intents/${route.params.id}`)
    if (intent.value.status === 'REQUIRES_CONFIRMATION') balance.value = (await api<{ balance: string }>('/wallet')).balance
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound.value = true
    else error.value = 'Gagal memuat pembayaran'
  }
}
onMounted(async () => {
  await load()
  if (payable.value) startWatching()
})

// QR "kasir": layar ini menampilkan link checkout; siapa pun yang login (HP lain) bisa memindai dan membayar.
// Setelah dibayar akun lain, intent ini 404 bagi kita (BOLA: hanya payer yang melihatnya), jadi 404 setelah
// sebelumnya bisa dibayar berarti "dibayar dari perangkat lain" (intent kedaluwarsa tetap 200 EXPIRED).
const qr = ref('')
const paidElsewhere = ref(false)
let watcher: ReturnType<typeof setInterval> | undefined
async function startWatching() {
  qr.value = await QRCode.toDataURL(window.location.href, { margin: 1, width: 220, color: { dark: '#0b2559', light: '#ffffff' } }).catch(() => '')
  watcher = setInterval(async () => {
    try {
      const fresh = await api<Intent>(`/payment-intents/${route.params.id}`)
      if (fresh.status !== intent.value?.status) intent.value = fresh
      if (fresh.status !== 'REQUIRES_CONFIRMATION') clearInterval(watcher)
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        paidElsewhere.value = true
        clearInterval(watcher)
      }
    }
  }, 3000)
}
onBeforeUnmount(() => clearInterval(watcher))

const payable = computed(() => intent.value?.status === 'REQUIRES_CONFIRMATION')
const enough = computed(() => balance.value !== null && intent.value !== null && BigInt(balance.value) >= BigInt(intent.value.amount))

async function pay() {
  error.value = ''
  busy.value = true
  try {
    intent.value = await api<Intent>(`/payment-intents/${route.params.id}/confirm`, { method: 'POST', idempotencyKey: key, body: {} })
    useDemo().mark('pay') // tur demo
  } catch (e) {
    error.value = e instanceof ApiError ? (messages[e.code] ?? e.message) : 'Koneksi gagal, coba lagi (aman: tidak akan terbayar dua kali)'
    if (e instanceof ApiError && e.code === 'INVALID_STATE_TRANSITION') await load()
  } finally {
    busy.value = false
  }
}

const statusText: Record<string, string> = {
  SUCCEEDED: 'Pembayaran berhasil', PARTIALLY_REFUNDED: 'Pembayaran berhasil (sebagian dikembalikan)',
  REFUNDED: 'Pembayaran dikembalikan penuh', EXPIRED: 'Kedaluwarsa', CANCELLED: 'Dibatalkan',
}
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Pembayaran</nav>
      <h1>Pembayaran merchant</h1>
    </div>
  </header>

  <section v-if="paidElsewhere" class="card narrow stack" data-test="paid-elsewhere">
    <div class="status-hero">
      <span class="status-icon"><AppIcon name="check" :size="30" :stroke="2.6" /></span>
      <h2 class="status-title">Dibayar dari perangkat lain</h2>
      <p class="muted">Tagihan ini sudah dibayar lewat kode QR. Detailnya hanya terlihat oleh akun yang membayar.</p>
    </div>
    <RouterLink to="/" class="btn ghost block">Kembali ke Beranda</RouterLink>
  </section>

  <section v-else-if="notFound" class="card narrow stack">
    <div class="status-hero">
      <span class="status-icon bad"><AppIcon name="x" :size="30" :stroke="2.6" /></span>
      <h2 class="status-title">Pembayaran tidak ditemukan</h2>
      <p class="muted">Link tidak valid, atau pembayaran ini sudah dibayar oleh akun lain.</p>
    </div>
    <RouterLink to="/" class="btn ghost block">Kembali ke Beranda</RouterLink>
  </section>

  <section v-else-if="intent" class="card narrow stack">
    <div class="status-hero">
      <span class="status-icon" :class="payable ? 'info' : intent.status === 'SUCCEEDED' || intent.status === 'PARTIALLY_REFUNDED' ? '' : 'warn'">
        <AppIcon :name="payable ? 'cart' : intent.status === 'SUCCEEDED' || intent.status === 'PARTIALLY_REFUNDED' ? 'check' : 'info'" :size="30" :stroke="2.4" />
      </span>
      <h2 class="status-title">Bayar ke {{ intent.merchant.name }}</h2>
      <span class="big" data-test="checkout-amount">{{ formatMinor(intent.amount) }}</span>
      <p v-if="!payable" class="pill" :class="intent.status === 'SUCCEEDED' || intent.status === 'PARTIALLY_REFUNDED' ? 'ok' : 'warn'" data-test="checkout-status">{{ statusText[intent.status] ?? intent.status }}</p>
    </div>
    <dl class="dl">
      <div v-if="intent.description"><dt>Deskripsi</dt><dd>{{ intent.description }}</dd></div>
      <div><dt>No. order</dt><dd class="mono">{{ intent.merchant_order_id }}</dd></div>
      <div v-if="payable"><dt>Berlaku sampai</dt><dd>{{ new Date(intent.expires_at).toLocaleString('id-ID', { timeZone: 'Asia/Jakarta' }) }} WIB</dd></div>
      <div v-if="payable && balance !== null"><dt>Saldo kamu</dt><dd>{{ formatMinor(balance) }}</dd></div>
    </dl>
    <template v-if="payable">
      <p v-if="balance !== null && !enough" class="alert error">Saldo tidak cukup. <RouterLink to="/topup">Top-up dulu</RouterLink>, lalu kembali ke halaman ini.</p>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <button type="button" class="btn block" :disabled="busy || !enough" data-spot="pay" @click="pay"><AppIcon name="lock" :size="18" :stroke="2.2" />Bayar {{ formatMinor(intent.amount) }}</button>
      <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Aman meski tombol tertekan dua kali atau halaman dibuka di dua tab: saldo hanya terpotong sekali.</p>
      <div v-if="qr" class="qr-box hide-mobile" data-test="checkout-qr">
        <img :src="qr" width="148" height="148" alt="Kode QR link pembayaran ini" />
        <div>
          <strong>Bayar dari HP</strong>
          <p class="muted">Pindai kode ini dengan kamera HP. Siapa pun yang sudah login bisa membayar, seperti QR di kasir. Halaman ini berubah otomatis setelah dibayar.</p>
        </div>
      </div>
    </template>
    <template v-else>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <RouterLink to="/" class="btn ghost block">Kembali ke Beranda</RouterLink>
    </template>
  </section>
</template>
