<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor } from '../money'
import { useAuth } from '../stores/auth'
import { useDemo } from '../stores/demo'

// "Uji ketahanan" (mode demo): dua janji LedgerPay dibuktikan dengan tombol, memakai API sungguhan.
const auth = useAuth()
const demo = useDemo()
const siti = computed(() => demo.sandbox?.contacts[0])

// ---------------------------------------------------------------- 1. tombol tertekan dua kali
type Shot = { status: number; replayed: boolean; ref: string }
const dbl = ref<{ before: string; after: string; shots: Shot[] } | null>(null)
const dblBusy = ref(false)
const dblError = ref('')

async function balance(): Promise<string> {
  return (await api<{ balance: string }>('/wallet')).balance
}

// Dua request DENGAN KUNCI YANG SAMA dikirim bersamaan, persis seperti klik ganda atau retry setelah koneksi putus.
async function doubleSend() {
  if (!siti.value) return
  dblBusy.value = true
  dblError.value = ''
  dbl.value = null
  try {
    const before = await balance()
    const key = newIdempotencyKey()
    const send = async (): Promise<Shot> => {
      const res = await fetch('/api/v1/transfers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.token}`, 'Idempotency-Key': key },
        body: JSON.stringify({ recipient_email: siti.value!.email, amount: '1000000', currency: 'IDR', note: 'Uji klik ganda' }),
      })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) throw new ApiError(res.status, body.code ?? 'UNKNOWN', body.detail ?? res.statusText)
      return { status: res.status, replayed: res.headers.get('Idempotent-Replayed') === 'true', ref: String(body.id ?? '').slice(-8).toUpperCase() }
    }
    const shots = await Promise.all([send(), send()])
    dbl.value = { before, after: await balance(), shots }
    demo.mark('double') // tur demo
  } catch (e) {
    dblError.value = e instanceof ApiError && e.code === 'RATE_LIMITED' ? 'Terlalu banyak transaksi. Tunggu sebentar, lalu coba lagi.' : 'Uji gagal dijalankan. Coba lagi.'
  } finally {
    dblBusy.value = false
  }
}
const dblDiff = computed(() => (dbl.value ? (BigInt(dbl.value.before) - BigInt(dbl.value.after)).toString() : '0'))

// ---------------------------------------------------------------- 2. 100 transfer bersamaan
type Attempt = { n: number; order: number; ok: boolean; code?: string; duration_ms: number }
type Stress = {
  attempts: number; succeeded: number; rejected: number; start_balance: string; end_balance: string; amount: string
  recipient: string; parallel: number; elapsed_ms: number; consistent: boolean; results: Attempt[]
}
const stress = ref<Stress | null>(null)
const shown = ref(0) // jumlah kotak yang sudah "selesai" di animasi
const stressBusy = ref(false)
const stressError = ref('')
const cells = computed(() => {
  const byN = new Map<number, Attempt>()
  stress.value?.results.slice(0, shown.value).forEach((a) => byN.set(a.n, a))
  return Array.from({ length: 100 }, (_, i) => byN.get(i + 1) ?? null)
})
const okShown = computed(() => stress.value?.results.slice(0, shown.value).filter((a) => a.ok).length ?? 0)
const done = computed(() => stress.value !== null && shown.value >= stress.value.results.length)

async function runStress() {
  stressBusy.value = true
  stressError.value = ''
  stress.value = null
  shown.value = 0
  try {
    const res = await api<Stress>('/demo/stress/transfers', { method: 'POST' })
    stress.value = res
    if (res.consistent) demo.mark('stress') // tur demo
    // Putar ulang hasil sesuai URUTAN SELESAI di server (bukan urutan dikirim), ±1,5 detik.
    const step = 15
    const timer = setInterval(() => {
      shown.value = Math.min(shown.value + 2, res.results.length)
      if (shown.value >= res.results.length) clearInterval(timer)
    }, step)
  } catch (e) {
    stressError.value = e instanceof ApiError && e.code === 'RATE_LIMITED' ? 'Uji ini dibatasi 3 kali per menit. Tunggu sebentar.' : 'Uji gagal dijalankan. Coba lagi.'
  } finally {
    stressBusy.value = false
  }
}

// ---------------------------------------------------------------- 3. periksa buku besar
type Integrity = { stored_balance: string; computed_balance: string; entries: number; journals: number; unbalanced_journals: number; ok: boolean }
const integrity = ref<Integrity | null>(null)
const igBusy = ref(false)
const igError = ref('')
async function checkLedger() {
  igBusy.value = true
  igError.value = ''
  try {
    integrity.value = await api<Integrity>('/demo/wallet/integrity')
    if (integrity.value.ok) demo.mark('ledger') // tur demo
  } catch {
    igError.value = 'Pemeriksaan gagal dijalankan. Coba lagi.'
  } finally {
    igBusy.value = false
  }
}

onMounted(() => {
  if (!demo.sandbox) demo.load()
})
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Uji ketahanan</h1>
      <p class="sub">Dua hal yang sering membuat aplikasi uang salah hitung. Coba sendiri; semua memakai transaksi sungguhan.</p>
    </div>
  </header>

  <section class="card stack lab-card" aria-labelledby="dbl-title">
    <div class="lab-head">
      <span class="lab-icon"><AppIcon name="refresh" :size="20" :stroke="2" /></span>
      <div>
        <h2 id="dbl-title">Tombol kirim tertekan dua kali</h2>
        <p class="muted">Koneksi lambat, kamu menekan "Kirim" lagi. Di banyak aplikasi, uang terpotong dua kali. Di sini kedua permintaan membawa kunci yang sama, jadi server hanya memprosesnya sekali.</p>
      </div>
    </div>
    <button type="button" class="btn" data-test="lab-double" :disabled="dblBusy || !siti" @click="doubleSend">
      <AppIcon name="transfer" :size="18" :stroke="2.2" />{{ dblBusy ? 'Mengirim…' : `Kirim Rp10.000 ke ${siti?.name ?? 'Siti'}, dua kali sekaligus` }}
    </button>
    <p v-if="dblError" class="alert error" role="alert">{{ dblError }}</p>
    <div v-if="dbl" class="lab-result" data-test="lab-double-result">
      <ol class="shots">
        <li v-for="(s, i) in dbl.shots" :key="i">
          <span class="pill ok">Permintaan {{ i + 1 }} · {{ s.status }}</span>
          <span>{{ s.replayed ? 'Diulang dari hasil pertama (tidak diproses lagi)' : 'Diproses' }} · ref {{ s.ref }}</span>
        </li>
      </ol>
      <dl class="dl">
        <div><dt>Saldo sebelum</dt><dd>{{ formatMinor(dbl.before) }}</dd></div>
        <div><dt>Saldo sesudah</dt><dd>{{ formatMinor(dbl.after) }}</dd></div>
        <div class="total"><dt>Terpotong</dt><dd :class="dblDiff === '1000000' ? 'in' : 'out'">{{ formatMinor(dblDiff) }}</dd></div>
      </dl>
      <p class="verdict" :class="{ bad: dblDiff !== '1000000' }">
        <AppIcon :name="dblDiff === '1000000' ? 'check' : 'x'" :size="18" :stroke="2.4" />
        {{ dblDiff === '1000000' ? 'Dua kali ditekan, satu kali terpotong.' : 'Saldo terpotong tidak sesuai.' }}
      </p>
    </div>
  </section>

  <section class="card stack lab-card" aria-labelledby="stress-title">
    <div class="lab-head">
      <span class="lab-icon"><AppIcon name="chart" :size="20" :stroke="2" /></span>
      <div>
        <h2 id="stress-title">100 transfer di detik yang sama</h2>
        <p class="muted">Sebuah dompet uji berisi tepat <strong>Rp100.000</strong>. Kami kirim <strong>100 transfer Rp10.000</strong> bersamaan. Hasil yang benar: tepat 10 berhasil, 90 ditolak, dan saldo tidak pernah minus.</p>
      </div>
    </div>
    <button type="button" class="btn" data-test="lab-stress" :disabled="stressBusy" @click="runStress">
      <AppIcon name="play" :size="18" :stroke="2" />{{ stressBusy ? 'Menjalankan 100 transfer…' : 'Jalankan uji 100 transfer' }}
    </button>
    <p v-if="stressError" class="alert error" role="alert">{{ stressError }}</p>
    <div v-if="stressBusy || stress" class="lab-result">
      <div class="grid100" role="img" :aria-label="`${okShown} berhasil dari ${shown} yang sudah selesai`">
        <span v-for="(c, i) in cells" :key="i" class="cell" :class="c ? (c.ok ? 'ok' : 'no') : ''" :title="c ? `#${c.n}: ${c.ok ? 'berhasil' : 'ditolak, saldo tidak cukup'}` : `#${i + 1}`" />
      </div>
      <p class="legend"><span class="cell ok" />Berhasil <span class="cell no" />Ditolak: saldo tidak cukup</p>
      <dl v-if="stress && done" class="dl" data-test="lab-stress-result">
        <div><dt>Berhasil</dt><dd class="in">{{ stress.succeeded }} transfer</dd></div>
        <div><dt>Ditolak</dt><dd>{{ stress.rejected }} transfer</dd></div>
        <div><dt>Saldo dompet uji</dt><dd>{{ formatMinor(stress.start_balance) }} → {{ formatMinor(stress.end_balance) }}</dd></div>
        <div><dt>Waktu</dt><dd>{{ stress.elapsed_ms }} ms ({{ stress.parallel }} diproses paralel)</dd></div>
      </dl>
      <p v-if="stress && done" class="verdict" :class="{ bad: !stress.consistent }">
        <AppIcon :name="stress.consistent ? 'check' : 'x'" :size="18" :stroke="2.4" />
        {{ stress.consistent ? `Tepat ${stress.succeeded} lolos, saldo berakhir ${formatMinor(stress.end_balance)}, tidak pernah minus.` : 'Hasil tidak konsisten.' }}
      </p>
      <p v-if="stress && done" class="note"><AppIcon name="info" :size="18" :stroke="2" />Uang yang berhasil terkirim masuk ke {{ stress.recipient }}. Setiap transfer berebut "kunci" dompet yang sama; saldo dicek setelah kunci didapat, jadi dua transfer tidak bisa memakai saldo yang sama.</p>
    </div>
  </section>

  <section class="card stack lab-card" aria-labelledby="ig-title">
    <div class="lab-head">
      <span class="lab-icon"><AppIcon name="shield" :size="20" :stroke="2" /></span>
      <div>
        <h2 id="ig-title">Periksa buku besar wallet kamu</h2>
        <p class="muted">Saldo yang kamu lihat disimpan terpisah dari catatan transaksinya. Pemeriksaan ini menghitung ulang saldo dari semua catatan, lalu membandingkannya. Lakukan setelah uji di atas.</p>
      </div>
    </div>
    <button type="button" class="btn ghost" data-test="lab-ledger" :disabled="igBusy" @click="checkLedger">
      <AppIcon name="refresh" :size="18" :stroke="2" />{{ igBusy ? 'Memeriksa…' : 'Periksa sekarang' }}
    </button>
    <p v-if="igError" class="alert error" role="alert">{{ igError }}</p>
    <div v-if="integrity" class="lab-result" data-test="lab-ledger-result">
      <dl class="dl">
        <div><dt>Saldo tersimpan</dt><dd>{{ formatMinor(integrity.stored_balance) }}</dd></div>
        <div><dt>Dihitung ulang dari {{ integrity.entries }} catatan</dt><dd>{{ formatMinor(integrity.computed_balance) }}</dd></div>
        <div><dt>Jurnal seimbang (debit = kredit)</dt><dd>{{ integrity.journals - integrity.unbalanced_journals }} dari {{ integrity.journals }}</dd></div>
      </dl>
      <p class="verdict" :class="{ bad: !integrity.ok }">
        <AppIcon :name="integrity.ok ? 'check' : 'x'" :size="18" :stroke="2.4" />
        {{ integrity.ok ? 'Cocok sampai rupiah terakhir.' : 'Tidak cocok: ini bug, laporkan.' }}
      </p>
      <p class="note"><AppIcon name="info" :size="18" :stroke="2" />Klik transaksi mana pun di <RouterLink to="/history">Riwayat</RouterLink> untuk melihat jurnal debit-kreditnya.</p>
    </div>
  </section>
</template>

<style scoped>
.lab-head { display: flex; gap: 14px; align-items: flex-start; }
.lab-head p { margin-top: 4px; line-height: 1.5; }
.lab-icon { width: 40px; height: 40px; border-radius: 12px; background: var(--blue-tint); color: var(--blue); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.lab-card .btn { align-self: flex-start; }
.lab-result { display: flex; flex-direction: column; gap: 14px; }
.shots { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; font-size: 14px; }
.shots li { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.verdict { display: flex; gap: 8px; align-items: center; font-weight: 700; color: var(--ok); }
.verdict.bad { color: var(--bad); }
.grid100 { display: grid; grid-template-columns: repeat(10, 1fr); gap: 6px; max-width: 420px; }
.cell { display: inline-block; aspect-ratio: 1; border-radius: 6px; background: var(--divider); transition: background .15s, transform .15s; }
.cell.ok { background: var(--ok-icon); transform: scale(1.05); }
.cell.no { background: #f1b4b4; }
.legend { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--muted); flex-wrap: wrap; }
.legend .cell { width: 14px; height: 14px; }
@media (max-width: 768px) { .lab-card .btn { align-self: stretch; } }
</style>
