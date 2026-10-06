<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor, rupiahToMinor } from '../money'

const rupiah = ref('')
const error = ref('')
const busy = ref(false)
const balance = ref<string | null>(null)
let key = newIdempotencyKey()
watch(rupiah, () => (key = newIdempotencyKey()))

const presets = ['50.000', '100.000', '200.000', '500.000', '1.000.000', '2.000.000']
const amountMinor = computed(() => rupiahToMinor(rupiah.value))
const after = computed(() => (amountMinor.value && balance.value ? formatMinor((BigInt(balance.value) + BigInt(amountMinor.value)).toString()) : '—'))

onMounted(async () => {
  balance.value = (await api<{ balance: string }>('/wallet')).balance
})

async function submit() {
  error.value = ''
  const amount = amountMinor.value
  if (!amount) {
    error.value = 'Nominal harus bilangan rupiah utuh'
    return
  }
  busy.value = true
  try {
    const t = await api<{ id: string; checkout_url: string | null }>('/topups', {
      method: 'POST', idempotencyKey: key, body: { amount, currency: 'IDR' },
    })
    if (!t.checkout_url) {
      // Provider belum menjawab (timeout): top-up tercatat, coba lagi dengan key yang SAMA.
      error.value = 'Provider pembayaran sedang lambat. Klik "Lanjut bayar" lagi (aman, tidak akan dobel).'
      return
    }
    window.location.assign(t.checkout_url) // halaman checkout provider; kembali ke /topups/:id
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Koneksi gagal, coba lagi'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Top-up</nav>
      <h1>Top-up saldo</h1>
    </div>
  </header>

  <div class="row-wrap">
    <form id="topup-form" class="card form grow-2" @submit.prevent="submit">
      <div class="field">
        <label for="amount">Nominal (Rp10.000 – Rp10.000.000)</label>
        <div class="amount-field">
          <span class="cur" aria-hidden="true">Rp</span>
          <input id="amount" v-model="rupiah" inputmode="numeric" placeholder="0" autocomplete="off" required />
        </div>
      </div>
      <div class="chips">
        <button v-for="p in presets" :key="p" type="button" class="chip-btn" :class="{ on: rupiah === p }" @click="rupiah = p">Rp{{ p }}</button>
      </div>
      <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Metode pembayaran (virtual account, QRIS, dan lainnya) dipilih di halaman penyedia pembayaran setelah ini.</p>
    </form>

    <aside class="card grow-1 stack" aria-label="Ringkasan">
      <h2>Ringkasan</h2>
      <dl class="dl">
        <div><dt>Saldo sekarang</dt><dd>{{ balance ? formatMinor(balance) : '…' }}</dd></div>
        <div><dt>Top-up</dt><dd>{{ amountMinor ? formatMinor(amountMinor) : '—' }}</dd></div>
        <div class="total"><dt>Saldo setelah top-up</dt><dd>{{ after }}</dd></div>
      </dl>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <button type="submit" form="topup-form" class="btn block" :disabled="busy">Lanjut bayar</button>
      <p class="note warn"><AppIcon name="clock" :size="18" :stroke="2" />Saldo bertambah setelah pembayaran dikonfirmasi penyedia, bukan saat tombol ditekan.</p>
    </aside>
  </div>
</template>
