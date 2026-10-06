<script setup lang="ts">
import { formatMinor } from '../money'
import AppIcon from './AppIcon.vue'

export type Tx = { id: string; type: string; direction: 'IN' | 'OUT'; amount: string; created_at: string }

defineProps<{ txs: Tx[]; emptyText?: string }>()

const labels: Record<string, string> = { TOPUP: 'Top-up', TRANSFER: 'Transfer', PAYMENT: 'Pembayaran', REFUND: 'Refund', PAYOUT: 'Tarik dana', ADJUSTMENT: 'Penyesuaian' }
const icons = { TOPUP: 'plus', TRANSFER: 'transfer', PAYMENT: 'cart', REFUND: 'undo', PAYOUT: 'bank', ADJUSTMENT: 'info' } as const
const iconOf = (t: Tx) => (t.type === 'TRANSFER' && t.direction === 'IN' ? 'arrowIn' : (icons[t.type as keyof typeof icons] ?? 'info'))
const subOf = (t: Tx) => (t.type === 'TRANSFER' ? (t.direction === 'IN' ? 'Masuk' : 'Keluar') : t.direction === 'IN' ? 'Dana masuk' : 'Dana keluar')

const dateFmt = new Intl.DateTimeFormat('id-ID', { timeZone: 'Asia/Jakarta', day: 'numeric', month: 'short', year: 'numeric' })
const timeFmt = new Intl.DateTimeFormat('id-ID', { timeZone: 'Asia/Jakarta', hour: '2-digit', minute: '2-digit' })
const shortRef = (id: string) => id.slice(0, 8).toUpperCase()
</script>

<template>
  <div class="table-scroll">
    <table class="table tx-table">
      <thead>
        <tr>
          <th scope="col">Keterangan</th>
          <th scope="col">Waktu</th>
          <th scope="col">Referensi</th>
          <th scope="col" class="right">Nominal</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="txs.length === 0"><td colspan="4" class="empty">{{ emptyText ?? 'Belum ada transaksi.' }}</td></tr>
        <tr v-for="t in txs" :key="t.id">
          <td>
            <div class="tx-cell">
              <span class="tx-icon" :class="{ in: t.direction === 'IN' }"><AppIcon :name="iconOf(t)" :size="18" /></span>
              <span><span class="tx-title">{{ labels[t.type] ?? t.type }}</span><span class="tx-sub">{{ subOf(t) }}<span class="only-mobile"> · {{ dateFmt.format(new Date(t.created_at)) }}, {{ timeFmt.format(new Date(t.created_at)) }}</span></span></span>
            </div>
          </td>
          <td>
            <span class="tx-title">{{ dateFmt.format(new Date(t.created_at)) }}</span>
            <span class="tx-sub">{{ timeFmt.format(new Date(t.created_at)) }} WIB</span>
          </td>
          <td class="mono muted">{{ shortRef(t.id) }}</td>
          <td class="right amount" :class="t.direction === 'IN' ? 'in' : 'out'">{{ t.direction === 'IN' ? '+' : '−' }} {{ formatMinor(t.amount) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
