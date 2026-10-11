<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import { useAuth } from '../stores/auth'
import { useDemo } from '../stores/demo'

// Landing publik: pengunjung yang belum login membuka "/" → halaman ini (router). Ditulis untuk orang awam dulu,
// lalu satu bagian ringkas untuk tim teknis. Tautan repo hanya tampil kalau VITE_REPO_URL diisi saat build.
const auth = useAuth()
const demo = useDemo()
const router = useRouter()
const busy = ref(false)
const error = ref('')
const repoUrl = import.meta.env.VITE_REPO_URL as string | undefined

onMounted(() => demo.detect())

async function tryDemo() {
  busy.value = true
  error.value = ''
  try {
    await auth.startDemo()
    router.push('/')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

const problems = [
  { icon: 'refresh', q: 'Tombol "Kirim" tertekan dua kali?', a: 'Saldo tetap terpotong sekali. Setiap permintaan membawa kunci unik; permintaan kedua hanya mengulang hasil yang pertama.' },
  { icon: 'chart', q: '100 transfer di detik yang sama?', a: 'Saldo tidak pernah minus. Transaksi dari satu dompet antre dengan rapi, dan saldo dicek setelah giliran didapat.' },
  { icon: 'bank', q: 'Koneksi ke bank putus di tengah jalan?', a: 'Tidak ada uang yang hilang atau keluar dua kali. Statusnya dicek ulang ke bank, lalu dicocokkan setiap hari.' },
] as const

const steps = ['Masuk sebagai Budi, saldo dan riwayat contoh sudah siap', 'Kirim uang ke teman atau bayar tagihan kafe', 'Jalankan uji 100 transfer bersamaan', 'Buka jurnal debit-kredit dari transaksi mana pun']
</script>

<template>
  <div class="welcome">
    <header class="w-nav">
      <span class="brand"><span class="brand-mark"><AppIcon name="logo" :stroke="2.2" /></span>LedgerPay</span>
      <RouterLink :to="auth.loggedIn ? '/' : '/login'" class="w-login">{{ auth.loggedIn ? 'Buka dompet' : 'Masuk' }}</RouterLink>
    </header>

    <section class="w-hero">
      <div class="w-hero-text">
        <span class="w-badge"><AppIcon name="spark" :size="14" :stroke="2.2" />Proyek portofolio · bisa dicoba</span>
        <h1>Dompet digital yang tetap tepat, bahkan saat jaringan tidak.</h1>
        <p>LedgerPay mencatat setiap rupiah di buku besar dua sisi, seperti sistem bank. Coba sendiri tanpa daftar: semua uang di demo adalah data contoh.</p>
        <div class="w-cta">
          <button v-if="demo.available" type="button" class="btn" data-test="welcome-demo" :disabled="busy" @click="tryDemo">
            <AppIcon name="play" :size="18" :stroke="2" />{{ busy ? 'Menyiapkan…' : 'Coba demo' }}
          </button>
          <RouterLink v-else to="/register" class="btn">Daftar</RouterLink>
          <RouterLink to="/login" class="btn ghost">Masuk</RouterLink>
        </div>
        <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      </div>
      <ol class="w-steps" aria-label="Yang bisa dicoba dalam 2 menit">
        <li v-for="(s, i) in steps" :key="s"><span class="w-step-no">{{ i + 1 }}</span>{{ s }}</li>
      </ol>
    </section>

    <section class="w-problems" aria-labelledby="w-problems-title">
      <h2 id="w-problems-title">Tiga hal yang sering membuat aplikasi uang salah hitung</h2>
      <div class="w-grid">
        <article v-for="p in problems" :key="p.q" class="card w-card">
          <span class="w-icon"><AppIcon :name="p.icon" :size="20" :stroke="2" /></span>
          <h3>{{ p.q }}</h3>
          <p>{{ p.a }}</p>
        </article>
      </div>
    </section>

    <section class="card w-tech" aria-labelledby="w-tech-title">
      <h2 id="w-tech-title">Untuk tim teknis</h2>
      <ul>
        <li><strong>Go + PostgreSQL:</strong> buku besar double-entry append-only, saldo dicek di dalam transaksi setelah lock baris (bukan sebelum).</li>
        <li><strong>Idempotency transaksional:</strong> kunci dan efeknya ada di transaksi yang sama, jadi retry setelah timeout aman.</li>
        <li><strong>Payout dengan status "belum diketahui":</strong> dicek ulang ke bank dan direkonsiliasi harian, bukan ditebak berhasil atau gagal.</li>
        <li><strong>Dapat diamati:</strong> metrik Prometheus, trace OpenTelemetry, alert yang tertaut ke runbook, dan invariant ledger yang diperiksa terus.</li>
        <li><strong>Diuji:</strong> test integrasi dengan PostgreSQL sungguhan, test konkurensi, dan E2E Playwright yang juga menjalankan demo ini.</li>
      </ul>
      <a v-if="repoUrl" :href="repoUrl" class="link-btn" target="_blank" rel="noopener">Lihat kode dan dokumen desainnya →</a>
    </section>

    <footer class="w-foot">LedgerPay adalah proyek portofolio, bukan layanan keuangan. Tidak ada uang sungguhan yang diproses.</footer>
  </div>
</template>

<style scoped>
.welcome { min-height: 100dvh; background: var(--bg); padding: 0 24px 40px; display: flex; flex-direction: column; gap: 32px; }
.w-nav { max-width: 1120px; width: 100%; margin: 0 auto; display: flex; justify-content: space-between; align-items: center; padding: 20px 0 0; }
.w-nav .brand { color: var(--navy); }
.w-nav .brand-mark { background: var(--navy); color: #fff; }
.w-login { font-weight: 700; text-decoration: none; }
.w-hero { max-width: 1120px; width: 100%; margin: 0 auto; background: var(--navy); color: #fff; border-radius: 28px; padding: 48px; display: grid; grid-template-columns: 1.4fr 1fr; gap: 40px; align-items: center; }
.w-hero h1 { font-size: 42px; line-height: 1.12; letter-spacing: -1px; font-weight: 800; margin: 14px 0 12px; }
.w-hero p { color: var(--on-navy-muted); font-size: 16px; line-height: 1.6; max-width: 560px; }
.w-badge { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 700; padding: 5px 12px; border-radius: 999px; background: rgba(255, 255, 255, .12); }
.w-cta { display: flex; gap: 12px; flex-wrap: wrap; margin-top: 24px; }
.w-cta .btn.ghost { background: transparent; color: #fff; border-color: rgba(255, 255, 255, .35); }
.w-steps { list-style: none; margin: 0; padding: 20px; border-radius: 20px; background: rgba(255, 255, 255, .08); display: flex; flex-direction: column; gap: 14px; font-size: 15px; }
.w-steps li { display: flex; gap: 12px; align-items: flex-start; line-height: 1.45; }
.w-step-no { width: 26px; height: 26px; border-radius: 999px; background: #fff; color: var(--navy); font-weight: 800; font-size: 13px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.w-problems, .w-tech, .w-foot { max-width: 1120px; width: 100%; margin: 0 auto; }
.w-problems h2, .w-tech h2 { font-size: 22px; font-weight: 800; letter-spacing: -.3px; margin-bottom: 16px; }
.w-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; }
.w-card { display: flex; flex-direction: column; gap: 10px; }
.w-card h3 { font-size: 16px; font-weight: 700; }
.w-card p { font-size: 14px; color: var(--text-2); line-height: 1.55; }
.w-icon { width: 40px; height: 40px; border-radius: 12px; background: var(--blue-tint); color: var(--blue); display: flex; align-items: center; justify-content: center; }
.w-tech ul { margin: 0 0 12px; padding-left: 20px; display: flex; flex-direction: column; gap: 8px; font-size: 14px; line-height: 1.55; color: var(--text-2); }
.w-foot { font-size: 12px; color: var(--muted); text-align: center; }
@media (max-width: 860px) {
  .welcome { padding: 0 16px 32px; gap: 24px; }
  .w-hero { grid-template-columns: 1fr; padding: 28px 22px; border-radius: 22px; gap: 24px; }
  .w-hero h1 { font-size: 30px; }
  .w-grid { grid-template-columns: 1fr; }
}
</style>
