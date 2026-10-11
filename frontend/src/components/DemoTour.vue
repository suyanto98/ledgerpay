<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'
import AppIcon from './AppIcon.vue'
import { tour, useDemo, type TourStep } from '../stores/demo'

// Tur demo di Beranda: 6 langkah dengan "kenapa penting" dalam bahasa awam, tercentang saat benar-benar dilakukan,
// lalu rangkuman + tombol kontak. Tautan kontak/repo hanya tampil kalau env-nya diisi saat build.
const demo = useDemo()
const contactUrl = import.meta.env.VITE_CONTACT_URL as string | undefined
const repoUrl = import.meta.env.VITE_REPO_URL as string | undefined
const doneCount = computed(() => demo.done.length)

// Transaksi terbaru: langkah "buka catatan" langsung membuka jurnalnya, bukan daftar riwayat.
const latestTx = ref<string | null>(null)
onMounted(async () => {
  latestTx.value = (await api<{ data: { id: string }[] }>('/wallet/transactions?limit=1').catch(() => ({ data: [] }))).data[0]?.id ?? null
})

// Tautan yang mendarat tepat di tempat aksinya; #penanda = elemen yang disorot di sana (spotlight.ts).
function linkOf(s: TourStep) {
  switch (s.key) {
    case 'transfer':
      return demo.sandbox?.contacts[0] ? { path: '/transfer', query: { to: demo.sandbox.contacts[0].email }, hash: '#amount' } : '/transfer#amount'
    case 'pay':
      return demo.openBill ? `/checkout/${demo.openBill.id}#pay` : '/'
    case 'journal':
      return latestTx.value ? `/history/${latestTx.value}#journal` : '/history'
    default:
      return `/lab#${s.key}` // double, stress, ledger
  }
}
</script>

<template>
  <section v-if="demo.sandbox" class="card stack" aria-labelledby="try-title" data-test="demo-tour">
    <div class="card-head flush">
      <h2 id="try-title">Coba dalam 2 menit</h2>
      <button type="button" class="link-btn" data-test="show-summary" @click="demo.showSummary = true">Lihat rangkuman</button>
    </div>
    <div class="tour-progress">
      <span><strong>{{ doneCount }} dari {{ tour.length }}</strong> dicoba</span>
      <span class="tour-track" role="progressbar" :aria-valuenow="doneCount" aria-valuemin="0" :aria-valuemax="tour.length"><span :style="{ width: (doneCount / tour.length) * 100 + '%' }" /></span>
    </div>
    <ol class="try-list">
      <li v-for="(s, i) in tour" :key="s.key" :class="{ done: demo.done.includes(s.key) }" :data-test="`tour-${s.key}`">
        <RouterLink :to="linkOf(s)">
          <span class="try-step"><AppIcon v-if="demo.done.includes(s.key)" name="check" :size="14" :stroke="3" /><template v-else>{{ i + 1 }}</template></span>
          <span><strong>{{ s.title }}</strong><span class="tx-sub">{{ s.why }}</span></span>
          <AppIcon name="chevron" :size="18" />
        </RouterLink>
      </li>
    </ol>
  </section>

  <section v-if="demo.sandbox && demo.showSummary" class="card stack summary" aria-labelledby="summary-title" data-test="demo-summary">
    <h2 id="summary-title">{{ demo.finished ? 'Kamu sudah mencoba semuanya.' : 'Yang ditunjukkan demo ini' }}</h2>
    <ol class="summary-points">
      <li><strong>Uang tidak pernah terpotong dua kali</strong>, walau tombol ditekan berulang atau koneksi putus.</li>
      <li><strong>Saat ratusan transaksi datang bersamaan, saldo tetap tepat</strong> dan tidak pernah minus.</li>
      <li><strong>Setiap rupiah tercatat di dua sisi</strong> dan bisa diperiksa ulang kapan pun, seperti pembukuan bank.</li>
    </ol>
    <p class="muted summary-note">Di balik layar: setiap janji di atas diuji otomatis setiap kali kodenya berubah, termasuk demo yang baru saja kamu coba.</p>
    <div class="row-wrap">
      <a v-if="contactUrl" :href="contactUrl" class="btn sm" target="_blank" rel="noopener" data-test="summary-contact">Hubungi pembuatnya</a>
      <a v-if="repoUrl" :href="repoUrl" class="btn sm ghost" target="_blank" rel="noopener">Lihat kode</a>
      <button type="button" class="btn sm ghost" @click="demo.showSummary = false">Tutup</button>
    </div>
  </section>
</template>

<style scoped>
.card-head.flush { padding: 0; }
.tour-progress { display: flex; align-items: center; gap: 12px; font-size: 13px; color: var(--muted); }
.tour-track { flex: 1; height: 8px; border-radius: 999px; background: var(--divider); overflow: hidden; }
.tour-track span { display: block; height: 100%; background: var(--ok-icon); transition: width .4s ease; }
.try-list li.done .try-step { background: var(--ok-icon); }
.try-list li.done strong { color: var(--muted); }
.summary { border: 1.5px solid var(--ok-icon); }
.summary h2 { font-size: 18px; font-weight: 800; }
.summary-points { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 8px; font-size: 14px; line-height: 1.5; color: var(--text-2); }
.summary-points strong { color: var(--text); }
.summary-note { font-size: 13px; line-height: 1.5; }
</style>
