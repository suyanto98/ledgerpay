<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from './components/AppIcon.vue'
import { useAuth } from './stores/auth'
import { useDemo } from './stores/demo'

const auth = useAuth()
const demo = useDemo()
const route = useRoute()
const router = useRouter()

// Halaman tamu (masuk/daftar) punya layout sendiri; halaman lain memakai sidebar (desktop)
// atau app bar + bottom tab bar (mobile, ≤768px; diatur CSS).
const guest = computed(() => route.meta.guest === true || route.meta.public === true || !auth.loggedIn)

const menu = [
  { to: '/', label: 'Beranda', icon: 'home', match: (p: string) => p === '/' },
  { to: '/transfer', label: 'Transfer', icon: 'transfer', match: (p: string) => p.startsWith('/transfer') },
  { to: '/topup', label: 'Top-up', icon: 'plus', match: (p: string) => p.startsWith('/topup') },
  { to: '/payout', label: 'Tarik dana', icon: 'bank', match: (p: string) => p.startsWith('/payout') },
  { to: '/history', label: 'Riwayat', icon: 'list', match: (p: string) => p.startsWith('/history') },
] as const

const tabs = [
  { to: '/', label: 'Beranda', icon: 'home', match: (p: string) => p === '/' },
  { to: '/transfer', label: 'Transfer', icon: 'transfer', match: (p: string) => p.startsWith('/transfer') },
  { to: '/history', label: 'Riwayat', icon: 'list', match: (p: string) => p.startsWith('/history') },
  { to: '/account', label: 'Akun', icon: 'user', match: (p: string) => p.startsWith('/account') },
] as const

function back() {
  if (window.history.state?.back) router.back()
  else router.push('/')
}

async function logout() {
  await auth.logout()
  router.push('/login')
}

// Mode demo: info sandbox dimuat setiap kali sesi berganti (login, demo baru, logout).
watch(
  () => auth.token,
  async (token) => {
    if (token && (await demo.detect())) await demo.load().catch(() => demo.clear())
    else demo.clear()
  },
  { immediate: true },
)

const rtf = new Intl.RelativeTimeFormat('id-ID', { numeric: 'auto' })
const expiresIn = computed(() => {
  if (!demo.sandbox) return ''
  const mins = Math.max(0, Math.round((new Date(demo.sandbox.expires_at).getTime() - Date.now()) / 60000))
  return mins >= 60 ? rtf.format(Math.round(mins / 60), 'hour') : rtf.format(mins, 'minute')
})

// Mulai ulang = sandbox baru (data segar). Muat ulang halaman penuh: semua view membaca data baru.
const restarting = ref(false)
async function restartDemo() {
  restarting.value = true
  try {
    await auth.startDemo()
    window.location.assign('/')
  } catch (e) {
    restarting.value = false
    window.alert((e as Error).message)
  }
}
</script>

<template>
  <RouterView v-if="guest" />
  <div v-else class="shell" :class="{ 'is-tab': route.meta.tab, 'is-home': route.path === '/' }">
    <aside class="sidebar">
      <RouterLink to="/" class="brand" aria-label="LedgerPay, ke Beranda">
        <span class="brand-mark"><AppIcon name="logo" :stroke="2.2" /></span>
        <span>LedgerPay</span>
      </RouterLink>
      <div class="sidebar-body">
        <nav class="nav" aria-label="Menu utama">
          <RouterLink v-for="m in menu" :key="m.to" :to="m.to" class="nav-link" :class="{ active: m.match(route.path) }">
            <AppIcon :name="m.icon" />{{ m.label }}
          </RouterLink>
        </nav>
        <div v-if="demo.sandbox" class="nav-group">
          <span class="nav-title">MODE DEMO</span>
          <RouterLink to="/lab" class="nav-link" :class="{ active: route.path === '/lab' }"><AppIcon name="spark" />Uji ketahanan</RouterLink>
        </div>
        <div v-if="auth.isOps" class="nav-group">
          <span class="nav-title">OPERASIONAL</span>
          <RouterLink to="/ops" class="nav-link" :class="{ active: route.path.startsWith('/ops') }"><AppIcon name="shield" />Ops console</RouterLink>
        </div>
        <div class="sidebar-spacer" />
        <button type="button" class="nav-link" data-test="logout" @click="logout"><AppIcon name="logout" />Keluar</button>
      </div>
    </aside>

    <!-- Mobile: app bar untuk halaman non-tab (tombol kembali + judul). -->
    <header v-if="!route.meta.tab" class="appbar">
      <button type="button" class="icon-btn bare appbar-back" aria-label="Kembali" @click="back"><AppIcon name="back" :size="22" :stroke="2" /></button>
      <span class="appbar-title">{{ route.meta.title }}</span>
    </header>

    <main class="content">
      <div v-if="demo.sandbox" class="demo-banner" role="status" data-test="demo-banner">
        <AppIcon name="spark" :size="18" :stroke="2" />
        <span><strong>Mode demo</strong> · semua data di sini contoh dan terhapus {{ expiresIn }}.</span>
        <button type="button" class="link-btn" :disabled="restarting" @click="restartDemo">{{ restarting ? 'Menyiapkan…' : 'Mulai ulang' }}</button>
      </div>
      <RouterView />
    </main>

    <!-- Mobile: bottom tab bar di halaman tab utama. -->
    <nav v-if="route.meta.tab" class="tabbar" aria-label="Navigasi bawah">
      <RouterLink v-for="t in tabs" :key="t.to" :to="t.to" class="tab" :class="{ active: t.match(route.path) }" :aria-current="t.match(route.path) ? 'page' : undefined">
        <AppIcon :name="t.icon" :size="22" />{{ t.label }}
      </RouterLink>
    </nav>
  </div>
</template>
