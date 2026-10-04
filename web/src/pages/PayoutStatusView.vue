<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import { formatMinor } from '../money'

type Payout = {
  id: string; amount: string; display_status: 'PROCESSING' | 'SUCCEEDED' | 'FAILED'
  destination: { bank_code: string; account_number_masked: string; holder_name: string }
}
const payout = ref<Payout | null>(null)
const route = useRoute()
let timer: ReturnType<typeof setTimeout> | undefined

// User hanya melihat PROCESSING / SUCCEEDED / FAILED. Beda SUBMITTED vs UNKNOWN (bank belum
// menjawab) adalah urusan sistem: dana tetap aman dan sedang dipastikan lewat status query.
async function poll(remaining = 120) {
  payout.value = await api<Payout>(`/payouts/${route.params.id}`)
  if (payout.value.display_status === 'PROCESSING' && remaining > 0) timer = setTimeout(() => poll(remaining - 1), 1000)
}
onMounted(() => poll())
onUnmounted(() => clearTimeout(timer))

const text = { PROCESSING: 'Sedang diproses…', SUCCEEDED: 'Berhasil dikirim', FAILED: 'Gagal — dana dikembalikan ke saldo' }
</script>

<template>
  <div class="card" v-if="payout">
    <h2>Tarik saldo {{ formatMinor(payout.amount) }}</h2>
    <p>{{ payout.destination.bank_code }} {{ payout.destination.account_number_masked }} a.n. {{ payout.destination.holder_name }}</p>
    <p data-test="payout-status" :class="{ ok: payout.display_status === 'SUCCEEDED', error: payout.display_status === 'FAILED' }">
      {{ text[payout.display_status] }}
    </p>
    <RouterLink to="/">Kembali ke wallet</RouterLink>
  </div>
</template>
