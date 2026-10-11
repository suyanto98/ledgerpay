<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor, rupiahToMinor } from '../money'
import { useDemo } from '../stores/demo'

type Transfer = { id: string; amount: string; receiver: { display_name: string }; created_at?: string }

const demo = useDemo()
const route = useRoute()
// ?to= dari kartu "Coba dalam 1 menit" di Beranda (mode demo).
const form = ref({ recipient: typeof route.query.to === 'string' ? route.query.to : '', rupiah: '', note: '' })
const error = ref('')
const done = ref<Transfer | null>(null)
const busy = ref(false)
const balance = ref<string | null>(null)
// Mobile: isi → konfirmasi → bukti. Desktop memakai panel ringkasan di samping (langsung kirim).
const step = ref<'fill' | 'confirm'>('fill')
const formEl = ref<HTMLFormElement | null>(null)
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

const presets = ['10.000', '25.000', '50.000', '100.000']
const amountMinor = computed(() => rupiahToMinor(form.value.rupiah))
const amountText = computed(() => (amountMinor.value ? formatMinor(amountMinor.value) : '—'))

async function loadBalance() {
  balance.value = (await api<{ balance: string }>('/wallet')).balance
}
onMounted(loadBalance)

async function submit() {
  error.value = ''
  const amount = amountMinor.value
  if (!amount) {
    error.value = 'Nominal harus bilangan rupiah utuh'
    return
  }
  busy.value = true
  try {
    done.value = await api<Transfer>('/transfers', {
      method: 'POST', idempotencyKey: key,
      body: { recipient_email: form.value.recipient, amount, currency: 'IDR', note: form.value.note || undefined },
    })
    loadBalance()
    demo.mark('transfer') // tur demo
  } catch (e) {
    error.value = e instanceof ApiError ? (messages[e.code] ?? e.message) : 'Koneksi gagal, coba lagi (aman: tidak akan terkirim dua kali)'
  } finally {
    busy.value = false
  }
}

function toConfirm() {
  error.value = ''
  if (!formEl.value?.reportValidity()) return
  if (!amountMinor.value) {
    error.value = 'Nominal harus bilangan rupiah utuh'
    return
  }
  step.value = 'confirm'
}

function again() {
  done.value = null
  step.value = 'fill'
  form.value = { recipient: '', rupiah: '', note: '' }
}
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Transfer</nav>
      <h1>Transfer ke sesama LedgerPay</h1>
    </div>
  </header>

  <!-- Bukti transfer -->
  <section v-if="done" class="card narrow stack">
    <div class="status-hero">
      <span class="status-icon"><AppIcon name="check" :size="32" :stroke="2.6" /></span>
      <span class="status-title">Transfer berhasil</span>
      <span class="big">{{ formatMinor(done.amount) }}</span>
      <p class="muted" role="status" data-test="transfer-done">Terkirim {{ formatMinor(done.amount) }} ke {{ done.receiver.display_name }}</p>
    </div>
    <dl class="dl">
      <div><dt>Penerima</dt><dd>{{ done.receiver.display_name }}</dd></div>
      <div><dt>Email</dt><dd>{{ form.recipient }}</dd></div>
      <div v-if="form.note"><dt>Catatan</dt><dd>{{ form.note }}</dd></div>
      <div><dt>No. referensi</dt><dd class="mono">{{ done.id.slice(-8).toUpperCase() }}</dd></div>
      <div><dt>Saldo sekarang</dt><dd>{{ balance ? formatMinor(balance) : '…' }}</dd></div>
    </dl>
    <div class="row-wrap">
      <button type="button" class="btn ghost grow-1" @click="again">Transfer lagi</button>
      <RouterLink to="/" class="btn grow-1">Ke Beranda</RouterLink>
    </div>
  </section>

  <!-- Mobile: konfirmasi sebelum kirim -->
  <section v-else-if="step === 'confirm'" class="stack narrow">
    <div class="card status-hero plain">
      <span class="muted">Kamu akan mengirim</span>
      <span class="big">{{ amountText }}</span>
      <span class="muted">ke <strong>{{ form.recipient }}</strong></span>
    </div>
    <dl class="card dl">
      <div><dt>Ke</dt><dd>{{ form.recipient }}</dd></div>
      <div v-if="form.note"><dt>Catatan</dt><dd>{{ form.note }}</dd></div>
      <div><dt>Biaya</dt><dd class="in">Gratis</dd></div>
      <div class="total"><dt>Total</dt><dd>{{ amountText }}</dd></div>
    </dl>
    <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Aman meski tombol tertekan dua kali atau koneksi putus: transfer hanya diproses sekali dan saldo terpotong sekali.</p>
    <p v-if="error" class="alert error" role="alert">{{ error }}</p>
    <div class="sticky-cta stack">
      <button type="button" class="btn block" :disabled="busy" @click="submit"><AppIcon name="lock" :size="18" :stroke="2.2" />Kirim sekarang</button>
      <button type="button" class="btn ghost block" :disabled="busy" @click="step = 'fill'">Ubah detail</button>
    </div>
  </section>

  <div v-else class="row-wrap">
    <form id="transfer-form" ref="formEl" class="card form grow-2" @submit.prevent="submit">
      <div class="field">
        <label for="recipient">Email penerima</label>
        <input id="recipient" v-model="form.recipient" class="input" type="email" placeholder="nama@contoh.id" required />
        <div v-if="demo.sandbox?.contacts.length" class="chips" aria-label="Kontak demo">
          <button v-for="c in demo.sandbox.contacts" :key="c.email" type="button" class="chip-btn" :class="{ on: form.recipient === c.email }" @click="form.recipient = c.email">
            <AppIcon name="user" :size="14" :stroke="2.2" />{{ c.name }}
          </button>
        </div>
      </div>
      <div class="field">
        <label for="amount">Nominal (Rp)</label>
        <div class="amount-field">
          <span class="cur" aria-hidden="true">Rp</span>
          <input id="amount" v-model="form.rupiah" data-spot="amount" inputmode="numeric" placeholder="0" autocomplete="off" required />
        </div>
        <div class="amount-meta">
          <span class="hint">Saldo tersedia {{ balance ? formatMinor(balance) : '…' }}</span>
          <div class="chips">
            <button v-for="p in presets" :key="p" type="button" class="chip-btn" :class="{ on: form.rupiah === p }" @click="form.rupiah = p">Rp{{ p }}</button>
          </div>
        </div>
      </div>
      <div class="field">
        <label for="note">Catatan (opsional)</label>
        <input id="note" v-model="form.note" class="input" maxlength="140" />
      </div>
      <p v-if="error" class="alert error only-mobile" role="alert">{{ error }}</p>
      <div class="sticky-cta only-mobile">
        <button type="button" class="btn block" @click="toConfirm">Lanjutkan</button>
      </div>
    </form>

    <aside class="card grow-1 stack hide-mobile" aria-label="Ringkasan">
      <h2>Ringkasan</h2>
      <dl class="dl">
        <div><dt>Ke</dt><dd>{{ form.recipient || '—' }}</dd></div>
        <div><dt>Nominal</dt><dd>{{ amountText }}</dd></div>
        <div><dt>Biaya</dt><dd class="in">Gratis</dd></div>
        <div class="total"><dt>Total</dt><dd>{{ amountText }}</dd></div>
      </dl>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <button type="submit" form="transfer-form" class="btn block" :disabled="busy">
        <AppIcon name="lock" :size="18" :stroke="2.2" />Kirim{{ amountMinor ? ' ' + amountText : '' }}
      </button>
      <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Aman meski tombol tertekan dua kali atau koneksi putus: transfer hanya diproses sekali.</p>
    </aside>
  </div>
</template>
