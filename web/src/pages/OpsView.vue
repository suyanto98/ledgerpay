<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, ApiError, newIdempotencyKey } from '../api'
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
const today = new Date(Date.now() + 7 * 3600_000).toISOString().slice(0, 10) // Asia/Jakarta
const date = ref(today)

// Satu form resolve terbuka pada satu waktu; key idempotency dibuat saat form dibuka (retry = key sama).
const form = ref<{ kind: 'item' | 'payout'; id: string; action: string; evidence: string; reason: string; key: string } | null>(null)

async function load() {
  items.value = (await api<{ data: Item[] }>('/ops/recon-items?resolution=OPEN')).data
  payouts.value = (await api<{ data: Payout[] }>('/ops/payouts?status=UNKNOWN&status=SUBMITTED')).data
}
onMounted(load)

async function runRecon() {
  err.value = msg.value = ''
  try {
    lastRun.value = await api<Run>('/ops/recon-runs', { method: 'POST', body: { business_date: date.value, source: 'PULL' } })
    await load()
  } catch (e) {
    err.value = e instanceof ApiError ? e.message : 'Gagal menjalankan rekonsiliasi'
  }
}

function open(kind: 'item' | 'payout', id: string, action: string) {
  form.value = { kind, id, action, evidence: '', reason: '', key: newIdempotencyKey() }
  err.value = msg.value = ''
}

async function submit() {
  const f = form.value
  if (!f) return
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
  }
}

const itemActions = (it: Item) => {
  const a = ['NO_ACTION_TIMING', 'NO_ACTION_PROVIDER_ERROR']
  if (it.object_type === 'payout' && it.category === 'STATUS_MISMATCH' && it.external_status === 'SUCCEEDED') a.unshift('APPLY_PAYOUT_SETTLE')
  if (it.object_type === 'payout' && ((it.category === 'STATUS_MISMATCH' && it.external_status === 'FAILED') || it.category === 'MISSING_EXTERNAL')) a.unshift('APPLY_PAYOUT_REVERSAL')
  return a
}
const money = (v: string | null) => (v === null ? '—' : formatMinor(v))
</script>

<template>
  <div class="card">
    <h2>Rekonsiliasi</h2>
    <form class="row" @submit.prevent="runRecon">
      <label for="date">Tanggal (WIB)</label>
      <input id="date" v-model="date" type="date" :max="today" required />
      <button type="submit">Jalankan recon</button>
    </form>
    <p v-if="lastRun" data-test="recon-run">
      {{ lastRun.business_date }}: {{ lastRun.status }} · cocok {{ lastRun.matched_count }} · status beda {{ lastRun.status_mismatch_count }} ·
      nominal beda {{ lastRun.amount_mismatch_count }} · hanya di provider {{ lastRun.missing_internal_count }} · hanya di kita {{ lastRun.missing_external_count }}
    </p>
    <p v-if="items.length === 0" class="muted">Tidak ada item terbuka.</p>
    <table v-else>
      <thead><tr><th>Kategori</th><th>Objek</th><th>Kita</th><th>Provider</th><th></th></tr></thead>
      <tbody>
      <tr v-for="it in items" :key="it.id" data-test="recon-item">
        <td>{{ it.category }}</td>
        <td>{{ it.object_type }} <span class="muted">{{ (it.internal_id ?? it.external_ref ?? '').slice(0, 8) }}</span></td>
        <td>{{ it.internal_status ?? '—' }} {{ money(it.internal_amount) }}</td>
        <td>{{ it.external_status ?? '—' }} {{ money(it.external_amount) }}</td>
        <td><button v-for="a in itemActions(it)" :key="a" class="link" @click="open('item', it.id, a)">{{ a }}</button></td>
      </tr>
      </tbody>
    </table>
  </div>

  <div class="card">
    <h2>Payout menggantung</h2>
    <p v-if="payouts.length === 0" class="muted">Tidak ada payout UNKNOWN/SUBMITTED.</p>
    <table v-else>
      <thead><tr><th>Payout</th><th>User</th><th>Nominal</th><th>Status</th><th></th></tr></thead>
      <tbody>
      <tr v-for="p in payouts" :key="p.id">
        <td class="muted">{{ p.id.slice(0, 8) }}</td><td>{{ p.user.email }}</td><td>{{ formatMinor(p.amount) }}</td><td>{{ p.status }}</td>
        <td>
          <button class="link" @click="open('payout', p.id, 'SUCCEEDED')">Tandai berhasil</button>
          <button class="link" @click="open('payout', p.id, 'FAILED')">Tandai gagal</button>
        </td>
      </tr>
      </tbody>
    </table>
  </div>

  <div v-if="form" class="card">
    <h2>{{ form.action }}</h2>
    <form @submit.prevent="submit">
      <label for="evidence">Referensi bukti (mutasi bank / baris file settlement)</label>
      <input id="evidence" v-model="form.evidence" maxlength="200" required />
      <label for="reason">Alasan (min. 10 karakter)</label>
      <input id="reason" v-model="form.reason" required />
      <button type="submit">Selesaikan</button>
      <button type="button" class="link" @click="form = null">Batal</button>
    </form>
  </div>
  <p v-if="msg" class="ok" role="status" data-test="ops-done">{{ msg }}</p>
  <p v-if="err" class="error" role="alert">{{ err }}</p>
</template>
