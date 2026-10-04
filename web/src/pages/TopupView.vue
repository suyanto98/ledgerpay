<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
import { rupiahToMinor } from '../money'

const rupiah = ref('')
const error = ref('')
const busy = ref(false)
let key = newIdempotencyKey()
watch(rupiah, () => (key = newIdempotencyKey()))

async function submit() {
  error.value = ''
  const amount = rupiahToMinor(rupiah.value)
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
  <div class="card">
    <h2>Top-up</h2>
    <p>Saldo bertambah setelah pembayaran dikonfirmasi provider (webhook), bukan saat tombol ditekan.</p>
    <form @submit.prevent="submit">
      <label for="amount">Nominal (Rp10.000 – Rp10.000.000)</label>
      <input id="amount" v-model="rupiah" inputmode="numeric" placeholder="100000" required />
      <button type="submit" :disabled="busy">Lanjut bayar</button>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
  </div>
</template>
