<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, ApiError } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor } from '../money'

// Detail transaksi + "di balik layar": journal double-entry dari GET /wallet/transactions/{id}. Akun pihak lain
// hanya berlabel generik (server tidak pernah mengirim ID-nya, BOLA).
type Entry = { side: 'D' | 'C'; amount: string; account_label: string }
type Detail = {
  id: string; type: string; direction: 'IN' | 'OUT'; amount: string; created_at: string; counterparty: string | null; note: string | null
  journal: { id: string; purpose: string; created_at: string; entries: Entry[] }
}
const route = useRoute()
const tx = ref<Detail | null>(null)
const notFound = ref(false)
const error = ref('')

onMounted(async () => {
  try {
    tx.value = await api<Detail>(`/wallet/transactions/${route.params.id}`)
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound.value = true
    else error.value = 'Gagal memuat transaksi'
  }
})

const labels: Record<string, string> = { TOPUP: 'Top-up', TRANSFER: 'Transfer', PAYMENT: 'Pembayaran', REFUND: 'Refund', PAYOUT: 'Tarik dana', ADJUSTMENT: 'Penyesuaian saldo' }
const title = computed(() => {
  const t = tx.value
  if (!t) return ''
  if (!t.counterparty) return labels[t.type] ?? t.type
  return t.type === 'TRANSFER' ? (t.direction === 'IN' ? 'Dari ' : 'Ke ') + t.counterparty : t.counterparty
})
const sum = (side: 'D' | 'C') => (tx.value?.journal.entries ?? []).filter((e) => e.side === side).reduce((a, e) => a + BigInt(e.amount), 0n).toString()
const debit = computed(() => sum('D'))
const credit = computed(() => sum('C'))
const balanced = computed(() => debit.value === credit.value)
const when = computed(() => (tx.value ? new Date(tx.value.created_at).toLocaleString('id-ID', { timeZone: 'Asia/Jakarta', dateStyle: 'long', timeStyle: 'short' }) + ' WIB' : ''))
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/history">Riwayat</RouterLink> / Detail</nav>
      <h1>Detail transaksi</h1>
    </div>
  </header>

  <p v-if="error" class="alert error" role="alert">{{ error }}</p>
  <section v-if="notFound" class="card narrow stack">
    <p class="muted">Transaksi tidak ditemukan.</p>
    <RouterLink to="/history" class="btn ghost block">Kembali ke riwayat</RouterLink>
  </section>

  <template v-else-if="tx">
    <section class="card narrow stack">
      <div class="status-hero plain">
        <span class="muted">{{ title }}</span>
        <span class="big amount" :class="tx.direction === 'IN' ? 'in' : 'out'" data-test="tx-amount">{{ tx.direction === 'IN' ? '+' : '−' }} {{ formatMinor(tx.amount) }}</span>
        <span class="pill ok">Berhasil</span>
      </div>
      <dl class="dl">
        <div><dt>Jenis</dt><dd>{{ labels[tx.type] ?? tx.type }}</dd></div>
        <div v-if="tx.note"><dt>Catatan</dt><dd>{{ tx.note }}</dd></div>
        <div><dt>Waktu</dt><dd>{{ when }}</dd></div>
        <div><dt>No. referensi</dt><dd class="mono">{{ tx.id.slice(-8).toUpperCase() }}</dd></div>
      </dl>
    </section>

    <section class="card narrow stack" aria-labelledby="journal-title" data-test="journal">
      <div>
        <h2 id="journal-title">Di balik layar: buku besar</h2>
        <p class="muted small-lead">Setiap transaksi dicatat di dua sisi. Jumlah yang keluar dari satu akun (debit) selalu sama dengan yang masuk ke akun lain (kredit). Kalau tidak seimbang, database menolak menyimpannya.</p>
      </div>
      <div class="table-scroll">
        <table class="table journal-table">
          <thead><tr><th scope="col">Akun</th><th scope="col" class="right">Debit</th><th scope="col" class="right">Kredit</th></tr></thead>
          <tbody>
            <tr v-for="(e, i) in tx.journal.entries" :key="i" :class="{ mine: e.account_label === 'Wallet Anda' }">
              <td>{{ e.account_label }}</td>
              <td class="right mono">{{ e.side === 'D' ? formatMinor(e.amount) : '' }}</td>
              <td class="right mono">{{ e.side === 'C' ? formatMinor(e.amount) : '' }}</td>
            </tr>
          </tbody>
          <tfoot><tr><th scope="row">Total</th><td class="right mono">{{ formatMinor(debit) }}</td><td class="right mono">{{ formatMinor(credit) }}</td></tr></tfoot>
        </table>
      </div>
      <p class="verdict" :class="{ bad: !balanced }" data-test="journal-verdict">
        <AppIcon :name="balanced ? 'check' : 'x'" :size="18" :stroke="2.4" />{{ balanced ? 'Seimbang: debit = kredit.' : 'Tidak seimbang.' }}
      </p>
      <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Saldo wallet adalah uang kamu yang dititipkan, jadi bagi LedgerPay ia "utang": kredit menambah saldo, debit mengurangi.</p>
    </section>
  </template>
</template>

<style scoped>
.small-lead { font-size: 13px; line-height: 1.5; margin-top: 4px; }
.journal-table { min-width: 0; } /* 3 kolom pendek: muat di kartu sempit; .table global min-width 640px menyembunyikan kolom Kredit */
.journal-table th:first-child, .journal-table td:first-child { padding-left: 0; }
.journal-table th:last-child, .journal-table td:last-child { padding-right: 0; }
.journal-table tfoot th, .journal-table tfoot td { font-weight: 800; border-top: 1.5px solid var(--border-strong); }
.journal-table tr.mine td:first-child { font-weight: 700; }
.verdict { display: flex; gap: 8px; align-items: center; font-weight: 700; color: var(--ok); }
.verdict.bad { color: var(--bad); }
</style>
