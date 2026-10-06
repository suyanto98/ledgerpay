<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
import AppIcon from '../components/AppIcon.vue'
import { formatMinor } from '../money'

// Ops console minimal (LF-18): antrean rekonsiliasi + payout UNKNOWN. Setiap aksi yang memindahkan
// uang wajib bukti + alasan; server menulis journal biasa (bukan edit saldo) dan audit di satu tx.
type Item = {
  id: string; object_type: string; internal_id: string | null; external_ref: string | null; category: string
  internal_amount: string | null; external_amount: string | null; internal_status: string | null; external_status: string | null
}
type Payout = { id: string; user: { email: string }; amount: string; status: string; last_error: string | null; created_at: string }
type Run = { business_date: string; status: string; matched_count: number; status_mismatch_count: number; missing_internal_count: number; missing_external_count: number; amount_mismatch_count: number }

const items = ref<Item[]>([])
const payouts = ref<Payout[]>([])
const lastRun = ref<Run | null>(null)
const msg = ref('')
const err = ref('')
const running = ref(false)
const busy = ref(false)
const today = new Date(Date.now() + 7 * 3600_000).toISOString().slice(0, 10) // Asia/Jakarta
const date = ref(today)

// Satu form resolve terbuka pada satu waktu; key idempotency dibuat saat form dibuka (retry = key sama).
const form = ref<{ kind: 'item' | 'payout'; id: string; title: string; actions: string[]; action: string; evidence: string; reason: string; key: string } | null>(null)

type Page<T> = { data: T[]; page?: { next_cursor: string | null; has_more: boolean } }

// Antrean harus lengkap (item yang tidak tampil = tidak pernah diselesaikan): ikuti cursor sampai habis.
async function all<T>(path: string, maxPages = 20): Promise<T[]> {
  const out: T[] = []
  let cursor: string | null = null
  for (let i = 0; i < maxPages; i++) {
    const res: Page<T> = await api<Page<T>>(path + (cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''))
    out.push(...res.data)
    if (!res.page?.has_more || !res.page.next_cursor) break
    cursor = res.page.next_cursor
  }
  return out
}

async function load() {
  items.value = await all<Item>('/ops/recon-items?resolution=OPEN&limit=100')
  payouts.value = await all<Payout>('/ops/payouts?status=UNKNOWN&status=SUBMITTED&limit=100')
}
onMounted(load)

async function runRecon() {
  err.value = msg.value = ''
  running.value = true
  try {
    lastRun.value = await api<Run>('/ops/recon-runs', { method: 'POST', body: { business_date: date.value, source: 'PULL' } })
    await load()
  } catch (e) {
    err.value = e instanceof ApiError ? e.message : 'Gagal menjalankan rekonsiliasi'
  } finally {
    running.value = false
  }
}

function openItem(it: Item) {
  const actions = itemActions(it)
  form.value = { kind: 'item', id: it.id, title: `${it.category} · ${it.object_type} ${shortId(it)}`, actions, action: actions[0], evidence: '', reason: '', key: newIdempotencyKey() }
  err.value = msg.value = ''
}
function openPayout(p: Payout, outcome: 'SUCCEEDED' | 'FAILED') {
  form.value = { kind: 'payout', id: p.id, title: `Payout ${p.id.slice(0, 8)} · ${formatMinor(p.amount)}`, actions: ['SUCCEEDED', 'FAILED'], action: outcome, evidence: '', reason: '', key: newIdempotencyKey() }
  err.value = msg.value = ''
}
// Aksi lain = niat lain = key baru.
function pickAction(a: string) {
  if (!form.value || form.value.action === a) return
  form.value.action = a
  form.value.key = newIdempotencyKey()
}

async function submit() {
  const f = form.value
  if (!f) return
  busy.value = true
  try {
    if (f.kind === 'item') {
      await api(`/ops/recon-items/${f.id}/resolve`, { method: 'POST', idempotencyKey: f.key, body: { action: f.action, evidence_ref: f.evidence, reason: f.reason } })
    } else {
      await api(`/ops/payouts/${f.id}/resolve`, { method: 'POST', idempotencyKey: f.key, body: { outcome: f.action, evidence_ref: f.evidence, reason: f.reason } })
    }
    msg.value = 'Diselesaikan dan tercatat di audit log'
    form.value = null
    await load()
  } catch (e) {
    err.value = e instanceof ApiError ? `${e.code}: ${e.message}` : 'Koneksi gagal, coba lagi (aman: key yang sama)'
  } finally {
    busy.value = false
  }
}

const itemActions = (it: Item) => {
  const a = ['NO_ACTION_TIMING', 'NO_ACTION_PROVIDER_ERROR']
  if (it.object_type === 'payout' && it.category === 'STATUS_MISMATCH' && it.external_status === 'SUCCEEDED') a.unshift('APPLY_PAYOUT_SETTLE')
  if (it.object_type === 'payout' && ((it.category === 'STATUS_MISMATCH' && it.external_status === 'FAILED') || it.category === 'MISSING_EXTERNAL')) a.unshift('APPLY_PAYOUT_REVERSAL')
  return a
}
const actionLabels: Record<string, string> = {
  APPLY_PAYOUT_SETTLE: 'Settle payout — bank sudah mengirim dana',
  APPLY_PAYOUT_REVERSAL: 'Reversal — dana dikembalikan ke user',
  NO_ACTION_TIMING: 'Tanpa aksi — beda waktu pencatatan',
  NO_ACTION_PROVIDER_ERROR: 'Tanpa aksi — kesalahan di sisi provider',
  SUCCEEDED: 'Tandai berhasil — bank sudah mengirim dana',
  FAILED: 'Tandai gagal — dana dikembalikan ke user',
}
const categoryTone = (c: string) => (c.startsWith('MISSING') ? 'warn' : 'bad')
const shortId = (it: Item) => (it.internal_id ?? it.external_ref ?? '').slice(0, 8)
const money = (v: string | null) => (v === null ? '' : formatMinor(v))
const side = (status: string | null, amount: string | null) => (status === null && amount === null ? '— tidak ada' : [status, money(amount)].filter(Boolean).join(' · '))

const stats = computed(() => {
  const r = lastRun.value
  if (!r) return []
  return [
    { k: 'Cocok', v: r.matched_count, cls: 'ok' },
    { k: 'Status beda', v: r.status_mismatch_count, cls: r.status_mismatch_count ? 'bad' : '' },
    { k: 'Nominal beda', v: r.amount_mismatch_count, cls: r.amount_mismatch_count ? 'bad' : '' },
    { k: 'Hanya di provider', v: r.missing_internal_count, cls: r.missing_internal_count ? 'warn' : '' },
    { k: 'Hanya di kita', v: r.missing_external_count, cls: r.missing_external_count ? 'warn' : '' },
  ]
})
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Ops console</h1>
      <p class="sub">Setiap penyelesaian wajib bukti + alasan dan tercatat di audit log.</p>
    </div>
    <form class="row-wrap end" @submit.prevent="runRecon">
      <div class="field">
        <label for="date" class="label">Tanggal bisnis (WIB)</label>
        <input id="date" v-model="date" class="input sm" type="date" :max="today" required />
      </div>
      <button type="submit" class="btn sm" :disabled="running"><AppIcon name="refresh" :size="18" :stroke="2" />Jalankan rekonsiliasi</button>
    </form>
  </header>

  <section v-if="lastRun" class="stack" aria-label="Hasil rekonsiliasi terakhir">
    <p class="muted" data-test="recon-run">Hasil {{ lastRun.business_date }}: <span class="pill" :class="lastRun.status === 'COMPLETED' ? 'ok' : 'warn'">{{ lastRun.status }}</span></p>
    <div class="stats">
      <div v-for="s in stats" :key="s.k" class="stat" :class="s.cls"><span class="k">{{ s.k }}</span><span class="v">{{ s.v }}</span></div>
    </div>
  </section>

  <p v-if="msg" class="alert ok" role="status" data-test="ops-done">{{ msg }}</p>
  <p v-if="err && !form" class="alert error" role="alert">{{ err }}</p>

  <div class="row-wrap">
    <div class="grow-2 stack">
      <section class="card card-flush">
        <div class="card-head">
          <h2>Antrean rekonsiliasi</h2>
          <span class="pill" :class="items.length ? 'warn' : 'ok'">{{ items.length }} terbuka</span>
        </div>
        <div class="table-scroll">
          <table class="table">
            <thead><tr><th scope="col">Kategori</th><th scope="col">Objek</th><th scope="col">Catatan kita</th><th scope="col">Bank / provider</th><th scope="col"><span class="sr-only">Aksi</span></th></tr></thead>
            <tbody>
              <tr v-if="items.length === 0"><td colspan="5" class="empty">Tidak ada item terbuka.</td></tr>
              <tr v-for="it in items" :key="it.id" data-test="recon-item" :data-object-id="it.internal_id ?? it.external_ref" :class="{ selected: form?.kind === 'item' && form.id === it.id }">
                <td><span class="tag" :class="categoryTone(it.category)">{{ it.category }}</span></td>
                <td>{{ it.object_type }} <span class="mono muted">{{ shortId(it) }}</span></td>
                <td>{{ side(it.internal_status, it.internal_amount) }}</td>
                <td><strong>{{ side(it.external_status, it.external_amount) }}</strong></td>
                <td class="right"><button type="button" class="btn-row" :class="{ on: form?.kind === 'item' && form.id === it.id }" @click="openItem(it)">Tinjau</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card card-flush">
        <div class="card-head"><h2>Payout menggantung</h2></div>
        <div class="table-scroll">
          <table class="table">
            <thead><tr><th scope="col">Payout</th><th scope="col">User</th><th scope="col">Nominal</th><th scope="col">Status</th><th scope="col"><span class="sr-only">Aksi</span></th></tr></thead>
            <tbody>
              <tr v-if="payouts.length === 0"><td colspan="5" class="empty">Tidak ada payout UNKNOWN/SUBMITTED.</td></tr>
              <tr v-for="p in payouts" :key="p.id" :class="{ selected: form?.kind === 'payout' && form.id === p.id }">
                <td class="mono muted">{{ p.id.slice(0, 8) }}</td>
                <td>{{ p.user.email }}</td>
                <td class="amount">{{ formatMinor(p.amount) }}</td>
                <td><span class="tag" :class="p.status === 'UNKNOWN' ? 'bad' : 'info'">{{ p.status }}</span></td>
                <td class="right">
                  <span class="btn-group">
                    <button type="button" class="btn-row ok" @click="openPayout(p, 'SUCCEEDED')">Tandai berhasil</button>
                    <button type="button" class="btn-row bad" @click="openPayout(p, 'FAILED')">Tandai gagal</button>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <aside v-if="form" class="card panel grow-1 stack" aria-label="Panel penyelesaian">
      <div class="panel-head">
        <div>
          <span class="muted">{{ form.kind === 'item' ? 'Selesaikan item rekonsiliasi' : 'Selesaikan payout' }}</span>
          <h2>{{ form.title }}</h2>
        </div>
        <button type="button" class="icon-btn bare" aria-label="Tutup panel" @click="form = null"><AppIcon name="x" :size="18" :stroke="2.2" /></button>
      </div>
      <form class="form" @submit.prevent="submit">
        <fieldset class="fieldset">
          <legend>Tindakan</legend>
          <label v-for="a in form.actions" :key="a" class="radio-card">
            <input type="radio" name="ops-action" :value="a" :checked="form.action === a" @change="pickAction(a)" />
            <span>{{ actionLabels[a] ?? a }}<small class="mono">{{ a }}</small></span>
          </label>
        </fieldset>
        <div class="field">
          <label for="evidence">Referensi bukti (mutasi bank / baris file settlement)</label>
          <input id="evidence" v-model="form.evidence" class="input sm" maxlength="200" required />
        </div>
        <div class="field">
          <label for="reason">Alasan (min. 10 karakter)</label>
          <textarea id="reason" v-model="form.reason" class="textarea" rows="3" minlength="10" required />
        </div>
        <p v-if="err" class="alert error" role="alert">{{ err }}</p>
        <button type="submit" class="btn sm block" :disabled="busy">Selesaikan &amp; catat audit</button>
      </form>
    </aside>
  </div>
</template>
