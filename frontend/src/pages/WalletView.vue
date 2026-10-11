<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'
import AppIcon from '../components/AppIcon.vue'
import DemoTour from '../components/DemoTour.vue'
import TxTable, { type Tx } from '../components/TxTable.vue'
import { formatMinor } from '../money'
import { useAuth } from '../stores/auth'
import { useDemo } from '../stores/demo'

type Wallet = { account_id: string; balance: string; currency: string; status: string }

const auth = useAuth()
const demo = useDemo()
const wallet = ref<Wallet | null>(null)
const txs = ref<Tx[]>([])
const hidden = ref(false)
const loading = ref(true)
type Summary = { period: string; in_amount: string; out_amount: string; transactions: number }
const summary = ref<Summary | null>(null)

onMounted(async () => {
  // Tiga permintaan paralel; Beranda tampil lengkap sekaligus, kerangka (skeleton) selama menunggu.
  const [w, t, s] = await Promise.all([
    api<Wallet>('/wallet'),
    api<{ data: Tx[] }>('/wallet/transactions?limit=8'),
    api<Summary>('/wallet/summary').catch(() => null),
  ])
  wallet.value = w
  txs.value = t.data
  summary.value = s
  loading.value = false
  if (demo.sandbox) await demo.load() // status tagihan terbaru (mungkin baru saja dibayar)
})

const monthName = computed(() => {
  if (!summary.value) return ''
  const [y, m] = summary.value.period.split('-').map(Number)
  return new Intl.DateTimeFormat('id-ID', { month: 'long', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 1)))
})

const now = new Date()
const hour = Number(new Intl.DateTimeFormat('en-GB', { timeZone: 'Asia/Jakarta', hour: 'numeric', hourCycle: 'h23' }).format(now))
const greeting = hour < 11 ? 'Selamat pagi' : hour < 15 ? 'Selamat siang' : hour < 18 ? 'Selamat sore' : 'Selamat malam'
const today = new Intl.DateTimeFormat('id-ID', { timeZone: 'Asia/Jakarta', weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }).format(now)
const name = computed(() => auth.displayName || auth.email)
const initials = computed(() => name.value.split(/[\s@.]+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join(''))
</script>

<template>
  <header class="page-head">
    <div>
      <h1>{{ greeting }}, {{ name.split(' ')[0] }}</h1>
      <p class="sub">{{ today }}</p>
    </div>
    <RouterLink to="/account" class="user-chip" aria-label="Akun"><span class="avatar">{{ initials }}</span><span class="user-chip-name">{{ name }}</span></RouterLink>
  </header>

  <div class="row-wrap stretch">
    <section class="balance-card grow-2" aria-label="Saldo">
      <div class="balance-top">
        <div>
          <div class="balance-label">Saldo aktif</div>
          <div v-if="hidden" class="balance">Rp •••••</div>
          <div v-else-if="wallet" class="balance" data-test="balance">{{ formatMinor(wallet.balance) }}</div>
          <div v-else class="balance"><span class="skeleton on-navy" aria-label="Memuat saldo" /></div>
          <div class="balance-meta">Wallet LedgerPay · {{ wallet?.currency ?? 'IDR' }}</div>
        </div>
        <button type="button" class="icon-btn-navy" :aria-label="hidden ? 'Tampilkan saldo' : 'Sembunyikan saldo'" @click="hidden = !hidden">
          <AppIcon :name="hidden ? 'eyeOff' : 'eye'" />
        </button>
      </div>
      <div class="balance-actions">
        <RouterLink to="/topup" class="btn-on-navy primary"><span class="action-icon"><AppIcon name="plus" :size="20" :stroke="2.2" /></span>Top-up</RouterLink>
        <RouterLink to="/transfer" class="btn-on-navy"><span class="action-icon"><AppIcon name="transfer" :size="20" :stroke="2.2" /></span>Transfer</RouterLink>
        <RouterLink to="/payout" class="btn-on-navy"><span class="action-icon"><AppIcon name="bank" :size="20" /></span>Tarik dana</RouterLink>
        <RouterLink to="/history" class="btn-on-navy only-mobile"><span class="action-icon"><AppIcon name="clock" :size="20" /></span>Riwayat</RouterLink>
      </div>
    </section>

    <section class="card grow-1 stack hide-mobile" aria-label="Akun">
      <h2>Akun</h2>
      <dl class="dl">
        <div><dt>Nama</dt><dd>{{ auth.displayName || '—' }}</dd></div>
        <div><dt>Email</dt><dd>{{ auth.email }}</dd></div>
        <div>
          <dt>Status wallet</dt>
          <dd>
            <span v-if="wallet" class="pill" :class="wallet.status === 'ACTIVE' ? 'ok' : 'bad'">{{ wallet.status === 'ACTIVE' ? 'Aktif' : 'Dibekukan' }}</span>
          </dd>
        </div>
      </dl>
      <p class="note"><AppIcon name="shield" :size="18" :stroke="2" />Transfer yang terkirim ulang karena koneksi putus tidak akan memotong saldo dua kali.</p>
    </section>
  </div>

  <section v-if="loading || summary" class="kpi-row" aria-label="Ringkasan bulan ini"><!-- ringkasan gagal dimuat: sembunyikan, jangan kerangka abadi -->
    <div class="kpi">
      <span class="kpi-label"><span class="kpi-dot in" aria-hidden="true" />Uang masuk{{ monthName ? ' ' + monthName : '' }}</span>
      <span v-if="summary" class="kpi-value" data-test="kpi-in">{{ formatMinor(summary.in_amount) }}</span>
      <span v-else class="skeleton line w-60" />
    </div>
    <div class="kpi">
      <span class="kpi-label"><span class="kpi-dot out" aria-hidden="true" />Uang keluar{{ monthName ? ' ' + monthName : '' }}</span>
      <span v-if="summary" class="kpi-value" data-test="kpi-out">{{ formatMinor(summary.out_amount) }}</span>
      <span v-else class="skeleton line w-60" />
    </div>
    <div class="kpi">
      <span class="kpi-label">Transaksi bulan ini</span>
      <span v-if="summary" class="kpi-value">{{ summary.transactions }}</span>
      <span v-else class="skeleton line w-30" />
    </div>
  </section>

  <section v-if="demo.openBill" class="card bill-card" aria-label="Tagihan menunggu" data-test="demo-bill">
    <span class="tx-icon"><AppIcon name="cart" :size="18" /></span>
    <div class="grow-1">
      <span class="tx-title">Tagihan dari {{ demo.openBill.merchant_name }}</span>
      <span class="tx-sub">{{ demo.openBill.description }} · {{ formatMinor(demo.openBill.amount) }}</span>
    </div>
    <RouterLink :to="`/checkout/${demo.openBill.id}`" class="btn sm">Bayar</RouterLink>
  </section>

  <DemoTour />

  <section class="card card-flush">
    <div class="card-head">
      <h2>Transaksi terakhir</h2>
      <RouterLink to="/history" class="link-btn">Lihat semua</RouterLink>
    </div>
    <TxTable :txs="txs" :loading="loading" />
  </section>
</template>
