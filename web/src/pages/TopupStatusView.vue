<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import { formatMinor } from '../money'

type Topup = { id: string; amount: string; status: 'PENDING' | 'SUCCEEDED' | 'FAILED' | 'EXPIRED' }
const topup = ref<Topup | null>(null)
const route = useRoute()
let timer: ReturnType<typeof setTimeout> | undefined

// Polling sampai worker memproses webhook. EXPIRED tetap dipolling sebentar: status lunak,
// webhook sukses yang terlambat masih bisa mengubahnya menjadi SUCCEEDED (FS-07).
async function poll(remaining = 60) {
  topup.value = await api<Topup>(`/topups/${route.params.id}`)
  if (topup.value.status === 'PENDING' && remaining > 0) timer = setTimeout(() => poll(remaining - 1), 1000)
}
onMounted(() => poll())
onUnmounted(() => clearTimeout(timer))

const text: Record<string, string> = {
  PENDING: 'Menunggu konfirmasi pembayaran…',
  SUCCEEDED: 'Top-up berhasil',
  FAILED: 'Pembayaran gagal',
  EXPIRED: 'Waktu pembayaran habis',
}
</script>

<template>
  <div class="card" v-if="topup">
    <h2>Top-up {{ formatMinor(topup.amount) }}</h2>
    <p data-test="topup-status" :class="{ ok: topup.status === 'SUCCEEDED', error: topup.status === 'FAILED' }">{{ text[topup.status] }}</p>
    <RouterLink to="/">Kembali ke wallet</RouterLink>
  </div>
</template>
