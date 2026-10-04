<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, newIdempotencyKey } from '../api'
import { rupiahToMinor } from '../money'

const form = ref({ rupiah: '', bank: 'MOCKBANK', account: '', holder: '' })
const error = ref('')
const busy = ref(false)
const router = useRouter()
let key = newIdempotencyKey()
watch(form, () => (key = newIdempotencyKey()), { deep: true })

const messages: Record<string, string> = {
  INSUFFICIENT_BALANCE: 'Saldo tidak cukup',
  ACCOUNT_FROZEN: 'Wallet dibekukan',
  RATE_LIMITED: 'Terlalu banyak transaksi, coba lagi sebentar lagi',
}

async function submit() {
  error.value = ''
  const amount = rupiahToMinor(form.value.rupiah)
  if (!amount) {
    error.value = 'Nominal harus bilangan rupiah utuh'
    return
  }
  busy.value = true
  try {
    const p = await api<{ id: string }>('/payouts', {
      method: 'POST', idempotencyKey: key,
      body: { amount, currency: 'IDR', destination: { bank_code: form.value.bank, account_number: form.value.account, holder_name: form.value.holder } },
    })
    router.push(`/payouts/${p.id}`)
  } catch (e) {
    error.value = e instanceof ApiError ? (messages[e.code] ?? e.message) : 'Koneksi gagal, coba lagi (aman: tidak akan dobel)'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="card">
    <h2>Tarik saldo</h2>
    <p>Dana langsung ditahan dari saldo; pengiriman ke bank diproses di latar belakang.</p>
    <form @submit.prevent="submit">
      <label for="amount">Nominal (Rp10.000 – Rp25.000.000)</label>
      <input id="amount" v-model="form.rupiah" inputmode="numeric" placeholder="200000" required />
      <label for="bank">Bank</label>
      <select id="bank" v-model="form.bank">
        <option v-for="b in ['MOCKBANK', 'BCA', 'BNI', 'BRI', 'MANDIRI']" :key="b">{{ b }}</option>
      </select>
      <label for="account">Nomor rekening</label>
      <input id="account" v-model="form.account" inputmode="numeric" pattern="[0-9]{6,20}" required />
      <label for="holder">Nama pemilik rekening</label>
      <input id="holder" v-model="form.holder" required maxlength="100" />
      <button type="submit" :disabled="busy">Tarik</button>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
  </div>
</template>
