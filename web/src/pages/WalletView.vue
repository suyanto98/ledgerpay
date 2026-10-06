<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'
import AppIcon from '../components/AppIcon.vue'
import TxTable, { type Tx } from '../components/TxTable.vue'
import { formatMinor } from '../money'
import { useAuth } from '../stores/auth'

type Wallet = { account_id: string; balance: string; currency: string; status: string }

const auth = useAuth()
const wallet = ref<Wallet | null>(null)
const txs = ref<Tx[]>([])
const hidden = ref(false)

onMounted(async () => {
  wallet.value = await api<Wallet>('/wallet')
  txs.value = (await api<{ data: Tx[] }>('/wallet/transactions?limit=8')).data
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
          <div v-else class="balance" data-test="balance">{{ wallet ? formatMinor(wallet.balance) : '…' }}</div>
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

  <section class="card card-flush">
    <div class="card-head">
      <h2>Transaksi terakhir</h2>
      <RouterLink to="/history" class="link-btn">Lihat semua</RouterLink>
    </div>
    <TxTable :txs="txs" />
  </section>
</template>
