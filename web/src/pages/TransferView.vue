<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
import { formatMinor, rupiahToMinor } from '../money'

const form = ref({ recipient: '', rupiah: '', note: '' })
const error = ref('')
const done = ref('')
const busy = ref(false)
// Satu niat = satu key: klik ganda atau retry setelah jaringan putus memakai key yang SAMA
// (server me-replay, uang tidak pindah dua kali). Key baru hanya kalau isi form berubah.
let key = newIdempotencyKey()
watch(form, () => (key = newIdempotencyKey()), { deep: true })

const messages: Record<string, string> = {
  INSUFFICIENT_BALANCE: 'Saldo tidak cukup',
  INVALID_RECIPIENT: 'Penerima tidak dapat menerima transfer',
  SELF_TRANSFER: 'Tidak bisa transfer ke diri sendiri',
  RATE_LIMITED: 'Terlalu banyak transaksi, coba lagi sebentar lagi',
}

async function submit() {
  error.value = ''
  done.value = ''
  const amount = rupiahToMinor(form.value.rupiah)
  if (!amount) {
    error.value = 'Nominal harus bilangan rupiah utuh'
    return
  }
  busy.value = true
  try {
    const t = await api<{ amount: string; receiver: { display_name: string } }>('/transfers', {
      method: 'POST', idempotencyKey: key,
      body: { recipient_email: form.value.recipient, amount, currency: 'IDR', note: form.value.note || undefined },
    })
    done.value = `Terkirim ${formatMinor(t.amount)} ke ${t.receiver.display_name}`
  } catch (e) {
    error.value = e instanceof ApiError ? (messages[e.code] ?? e.message) : 'Koneksi gagal, coba lagi (aman: tidak akan terkirim dua kali)'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="card">
    <h2>Transfer</h2>
    <form @submit.prevent="submit">
      <label for="recipient">Email penerima</label>
      <input id="recipient" v-model="form.recipient" type="email" required />
      <label for="amount">Nominal (Rp)</label>
      <input id="amount" v-model="form.rupiah" inputmode="numeric" placeholder="50000" required />
      <label for="note">Catatan (opsional)</label>
      <input id="note" v-model="form.note" maxlength="140" />
      <button type="submit" :disabled="busy">Kirim</button>
      <p v-if="done" class="ok" role="status" data-test="transfer-done">{{ done }}</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
  </div>
</template>
