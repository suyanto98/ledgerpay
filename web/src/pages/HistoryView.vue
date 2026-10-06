<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import AppIcon from '../components/AppIcon.vue'
import TxTable, { type Tx } from '../components/TxTable.vue'

type Page = { data: Tx[]; page: { next_cursor: string | null; has_more: boolean } }

const filters = ref({ type: '', direction: '', from: '', to: '' })
const txs = ref<Tx[]>([])
const error = ref('')
const busy = ref(false)
// Cursor buram dari server: simpan cursor tiap halaman yang sudah dibuka agar "Sebelumnya" bisa kembali.
const cursors = ref<(string | null)[]>([null])
const nextCursor = ref<string | null>(null)
const pageIndex = computed(() => cursors.value.length - 1)
const PAGE_SIZE = 20

async function load(cursor: string | null) {
  busy.value = true
  error.value = ''
  const q = new URLSearchParams({ limit: String(PAGE_SIZE) })
  const f = filters.value
  if (f.type) q.append('type', f.type)
  if (f.direction) q.set('direction', f.direction)
  if (f.from) q.set('from', f.from)
  if (f.to) q.set('to', f.to)
  if (cursor) q.set('cursor', cursor)
  try {
    const res = await api<Page>(`/wallet/transactions?${q}`)
    txs.value = res.data
    nextCursor.value = res.page.has_more ? res.page.next_cursor : null
  } catch (e) {
    txs.value = []
    nextCursor.value = null
    error.value = e instanceof ApiError && e.status === 400 ? 'Filter tidak valid (rentang tanggal maksimal 366 hari).' : 'Gagal memuat riwayat'
  } finally {
    busy.value = false
  }
}

function reset() {
  cursors.value = [null]
  load(null)
}
function next() {
  if (!nextCursor.value) return
  cursors.value.push(nextCursor.value)
  load(nextCursor.value)
}
function prev() {
  if (cursors.value.length <= 1) return
  cursors.value.pop()
  load(cursors.value[cursors.value.length - 1])
}

watch(filters, reset, { deep: true })
onMounted(() => load(null))

const firstRow = computed(() => pageIndex.value * PAGE_SIZE + 1)

// CSV dari baris yang sedang ditampilkan (dibuat di browser; tidak ada endpoint ekspor).
function downloadCsv() {
  const rows = [['id', 'waktu', 'jenis', 'arah', 'nominal_minor', 'mata_uang']]
  for (const t of txs.value) rows.push([t.id, t.created_at, t.type, t.direction, t.amount, 'IDR'])
  const blob = new Blob([rows.map((r) => r.join(',')).join('\n')], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `ledgerpay-riwayat-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Riwayat transaksi</h1>
      <p class="sub">Semua mutasi wallet, terbaru di atas.</p>
    </div>
    <button type="button" class="btn ghost sm" :disabled="txs.length === 0" @click="downloadCsv">
      <AppIcon name="download" :size="18" :stroke="2" />Unduh CSV
    </button>
  </header>

  <section class="card card-flush filters" aria-label="Filter">
    <div class="field">
      <label for="f-type">Jenis</label>
      <select id="f-type" v-model="filters.type" class="select sm">
        <option value="">Semua jenis</option>
        <option value="TRANSFER">Transfer</option>
        <option value="TOPUP">Top-up</option>
        <option value="PAYOUT">Tarik dana</option>
        <option value="PAYMENT">Pembayaran</option>
        <option value="REFUND">Refund</option>
        <option value="ADJUSTMENT">Penyesuaian</option>
      </select>
    </div>
    <div class="field">
      <label for="f-dir">Arah</label>
      <select id="f-dir" v-model="filters.direction" class="select sm">
        <option value="">Masuk &amp; keluar</option>
        <option value="IN">Dana masuk</option>
        <option value="OUT">Dana keluar</option>
      </select>
    </div>
    <div class="field">
      <label for="f-from">Dari tanggal</label>
      <input id="f-from" v-model="filters.from" type="date" class="input sm" :max="filters.to || undefined" />
    </div>
    <div class="field">
      <label for="f-to">Sampai tanggal</label>
      <input id="f-to" v-model="filters.to" type="date" class="input sm" :min="filters.from || undefined" />
    </div>
  </section>

  <p v-if="error" class="alert error" role="alert">{{ error }}</p>

  <section class="card card-flush" :aria-busy="busy">
    <TxTable :txs="txs" empty-text="Tidak ada transaksi untuk filter ini." />
    <div class="table-foot">
      <span class="muted">{{ txs.length ? `Menampilkan ${firstRow}–${firstRow + txs.length - 1}` : 'Tidak ada data' }}</span>
      <div class="btn-group">
        <button type="button" class="btn ghost sm" :disabled="busy || pageIndex === 0" @click="prev">Sebelumnya</button>
        <button type="button" class="btn ghost sm" :disabled="busy || !nextCursor" @click="next">Berikutnya</button>
      </div>
    </div>
  </section>
</template>
