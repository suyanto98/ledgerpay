<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api'
import { formatMinor } from '../money'

type Wallet = { balance: string; currency: string; status: string }
type Tx = { id: string; type: string; direction: 'IN' | 'OUT'; amount: string; created_at: string }

const wallet = ref<Wallet | null>(null)
const txs = ref<Tx[]>([])
const labels: Record<string, string> = { TOPUP: 'Top-up', TRANSFER: 'Transfer', PAYMENT: 'Pembayaran', ADJUSTMENT: 'Penyesuaian' }

onMounted(async () => {
  wallet.value = await api<Wallet>('/wallet')
  txs.value = (await api<{ data: Tx[] }>('/wallet/transactions?limit=20')).data
})
</script>

<template>
  <div class="card">
    <div>Saldo</div>
    <div class="balance" data-test="balance">{{ wallet ? formatMinor(wallet.balance) : '…' }}</div>
  </div>
  <div class="card">
    <h3>Riwayat</h3>
    <p v-if="txs.length === 0">Belum ada transaksi.</p>
    <table v-else>
      <tr v-for="t in txs" :key="t.id">
        <td>{{ new Date(t.created_at).toLocaleString('id-ID') }}</td>
        <td>{{ labels[t.type] ?? t.type }}</td>
        <td :class="t.direction === 'IN' ? 'in' : 'out'">{{ t.direction === 'IN' ? '+' : '−' }}{{ formatMinor(t.amount) }}</td>
      </tr>
    </table>
  </div>
</template>
