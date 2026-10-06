<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor, rupiahToMinor } from '../money'

const form = ref({ rupiah: '', bank: 'MOCKBANK', account: '', holder: '' })
const error = ref('')
const busy = ref(false)
const balance = ref<string | null>(null)
const router = useRouter()
let key = newIdempotencyKey()
watch(form, () => (key = newIdempotencyKey()), { deep: true })

const messages: Record<string, string> = {
  INSUFFICIENT_BALANCE: 'Saldo tidak cukup',
  ACCOUNT_FROZEN: 'Wallet dibekukan',
  RATE_LIMITED: 'Terlalu banyak transaksi, coba lagi sebentar lagi',
}

const amountMinor = computed(() => rupiahToMinor(form.value.rupiah))
const amountText = computed(() => (amountMinor.value ? formatMinor(amountMinor.value) : '—'))
const masked = computed(() => (form.value.account.length > 4 ? '•••• ' + form.value.account.slice(-4) : form.value.account || '—'))

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
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Tarik dana</nav>
      <h1>Tarik dana ke rekening bank</h1>
    </div>
  </header>

  <div class="row-wrap">
    <form id="payout-form" class="card form grow-2" @submit.prevent="submit">
      <div class="field">
        <label for="amount">Nominal (Rp10.000 – Rp25.000.000)</label>
        <div class="amount-field">
          <span class="cur" aria-hidden="true">Rp</span>
          <input id="amount" v-model="form.rupiah" inputmode="numeric" placeholder="0" autocomplete="off" required />
        </div>
        <span class="hint">Saldo tersedia {{ balance ? formatMinor(balance) : '…' }}</span>
      </div>
      <div class="field">
        <label for="bank">Bank</label>
        <select id="bank" v-model="form.bank" class="select">
          <option v-for="b in ['MOCKBANK', 'BCA', 'BNI', 'BRI', 'MANDIRI']" :key="b">{{ b }}</option>
        </select>
      </div>
      <div class="field">
        <label for="account">Nomor rekening</label>
        <input id="account" v-model="form.account" class="input" inputmode="numeric" pattern="[0-9]{6,20}" required />
      </div>
      <div class="field">
        <label for="holder">Nama pemilik rekening</label>
        <input id="holder" v-model="form.holder" class="input" required maxlength="100" />
      </div>
    </form>

    <aside class="card grow-1 stack" aria-label="Ringkasan">
      <h2>Ringkasan</h2>
      <dl class="dl">
        <div><dt>Ke</dt><dd>{{ form.bank }} {{ masked }}</dd></div>
        <div><dt>Atas nama</dt><dd>{{ form.holder || '—' }}</dd></div>
        <div class="total"><dt>Total</dt><dd>{{ amountText }}</dd></div>
      </dl>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <button type="submit" form="payout-form" class="btn block" :disabled="busy"><AppIcon name="bank" :size="18" />Tarik dana</button>
      <p class="note"><AppIcon name="shield" :size="18" :stroke="2" />Dana langsung ditahan dari saldo dan tidak akan terkirim dua kali. Pengiriman ke bank diproses di latar belakang.</p>
    </aside>
  </div>
</template>
