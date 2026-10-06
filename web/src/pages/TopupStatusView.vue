<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import AppIcon from '../components/AppIcon.vue'
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
const tone = computed(() => ({ PENDING: 'warn', SUCCEEDED: '', FAILED: 'bad', EXPIRED: 'bad' })[topup.value?.status ?? 'PENDING'])
const icon = computed(() => ({ PENDING: 'clock', SUCCEEDED: 'check', FAILED: 'x', EXPIRED: 'clock' } as const)[topup.value?.status ?? 'PENDING'])
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Top-up</nav>
      <h1>Status top-up</h1>
    </div>
  </header>

  <section v-if="topup" class="card narrow stack">
    <div class="status-hero">
      <span class="status-icon" :class="tone"><AppIcon :name="icon" :size="30" :stroke="2.6" /></span>
      <span class="status-title" data-test="topup-status">{{ text[topup.status] }}</span>
      <span class="big">{{ formatMinor(topup.amount) }}</span>
      <p v-if="topup.status === 'PENDING'" class="muted">Halaman ini diperbarui otomatis.</p>
    </div>
    <dl class="dl">
      <div><dt>No. referensi</dt><dd class="mono">{{ topup.id }}</dd></div>
    </dl>
    <RouterLink to="/" class="btn ghost block">Kembali ke Beranda</RouterLink>
  </section>
</template>
