<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, ApiError, newIdempotencyKey } from '../api'
import { formatMinor } from '../money'

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
onMounted(load)

const payable = computed(() => intent.value?.status === 'REQUIRES_CONFIRMATION')
const enough = computed(() => balance.value !== null && intent.value !== null && BigInt(balance.value) >= BigInt(intent.value.amount))

async function pay() {
  error.value = ''
  busy.value = true
  try {
    intent.value = await api<Intent>(`/payment-intents/${route.params.id}/confirm`, { method: 'POST', idempotencyKey: key, body: {} })
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
  <div class="card" v-if="notFound">
    <h2>Pembayaran tidak ditemukan</h2>
    <p>Link tidak valid, atau pembayaran ini sudah dibayar oleh akun lain.</p>
    <RouterLink to="/">Kembali ke wallet</RouterLink>
  </div>
  <div class="card" v-else-if="intent">
    <h2>Bayar ke {{ intent.merchant.name }}</h2>
    <p class="amount" data-test="checkout-amount">{{ formatMinor(intent.amount) }}</p>
    <p v-if="intent.description">{{ intent.description }}</p>
    <p class="muted">Order {{ intent.merchant_order_id }}</p>
    <template v-if="payable">
      <p class="muted">Berlaku sampai {{ new Date(intent.expires_at).toLocaleString('id-ID') }}</p>
      <p v-if="balance !== null">Saldo kamu: {{ formatMinor(balance) }}</p>
      <button type="button" :disabled="busy || !enough" @click="pay">Bayar {{ formatMinor(intent.amount) }}</button>
      <p v-if="balance !== null && !enough" class="error">Saldo tidak cukup. <RouterLink to="/topup">Top-up</RouterLink></p>
    </template>
    <p v-else data-test="checkout-status" :class="{ ok: intent.status === 'SUCCEEDED' }">{{ statusText[intent.status] ?? intent.status }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <RouterLink v-if="!payable" to="/">Kembali ke wallet</RouterLink>
  </div>
</template>
