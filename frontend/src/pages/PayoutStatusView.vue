<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import AppIcon from '../components/AppIcon.vue'
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
const tone = computed(() => ({ PROCESSING: 'warn', SUCCEEDED: '', FAILED: 'bad' })[payout.value?.display_status ?? 'PROCESSING'])
const icon = computed(() => ({ PROCESSING: 'clock', SUCCEEDED: 'check', FAILED: 'x' } as const)[payout.value?.display_status ?? 'PROCESSING'])

const steps = computed(() => {
  const s = payout.value?.display_status
  return [
    { cls: 'done', title: 'Permintaan diterima', sub: 'Saldo dicadangkan untuk penarikan ini' },
    { cls: s === 'PROCESSING' ? 'now' : 'done', title: 'Diproses bank', sub: s === 'PROCESSING' ? 'Menunggu konfirmasi bank' : 'Bank sudah menjawab' },
    s === 'FAILED'
      ? { cls: 'fail', title: 'Gagal dikirim', sub: 'Dana sudah dikembalikan ke saldo' }
      : { cls: s === 'SUCCEEDED' ? 'done' : 'todo', title: 'Dana masuk ke rekening', sub: s === 'SUCCEEDED' ? 'Selesai' : 'Halaman ini diperbarui otomatis' },
  ]
})
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumb" aria-label="Breadcrumb"><RouterLink to="/">Beranda</RouterLink> / Tarik dana</nav>
      <h1>Status tarik dana</h1>
    </div>
  </header>

  <div v-if="payout" class="row-wrap">
    <section class="card grow-2 stack">
      <div class="status-hero">
        <span class="status-icon" :class="tone"><AppIcon :name="icon" :size="30" :stroke="2.6" /></span>
        <span class="status-title" data-test="payout-status">{{ text[payout.display_status] }}</span>
        <span class="big">{{ formatMinor(payout.amount) }}</span>
      </div>
      <dl class="dl">
        <div><dt>Rekening tujuan</dt><dd>{{ payout.destination.bank_code }} {{ payout.destination.account_number_masked }}</dd></div>
        <div><dt>Atas nama</dt><dd>{{ payout.destination.holder_name }}</dd></div>
        <div><dt>No. referensi</dt><dd class="mono">{{ payout.id }}</dd></div>
      </dl>
      <RouterLink to="/" class="btn ghost block">Kembali ke Beranda</RouterLink>
    </section>

    <aside class="card grow-1 stack" aria-label="Tahapan">
      <h2>Tahapan</h2>
      <ol class="steps" role="list">
        <li v-for="s in steps" :key="s.title" class="step" :class="s.cls">
          <div class="step-rail">
            <span class="step-dot"><AppIcon v-if="s.cls === 'done'" name="check" :size="14" :stroke="3" /><AppIcon v-else-if="s.cls === 'fail'" name="x" :size="14" :stroke="3" /></span>
            <span class="step-line" />
          </div>
          <div class="step-body"><strong>{{ s.title }}</strong><span>{{ s.sub }}</span></div>
        </li>
      </ol>
      <p class="note"><AppIcon name="shield" :size="18" :stroke="2" />Dana tidak akan terkirim dua kali. Jika bank lambat menjawab, kami cek statusnya sebelum mencoba lagi.</p>
    </aside>
  </div>
</template>
