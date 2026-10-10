<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import { useAuth } from '../stores/auth'

const auth = useAuth()
const router = useRouter()
const name = computed(() => auth.displayName || auth.email)
const initials = computed(() => name.value.split(/[\s@.]+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join(''))

async function logout() {
  await auth.logout()
  router.push('/login')
}
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Akun</h1>
      <p class="sub">Profil dan pengaturan sesi.</p>
    </div>
  </header>

  <section class="card narrow stack">
    <div class="profile">
      <span class="avatar lg">{{ initials }}</span>
      <div>
        <strong class="profile-name">{{ auth.displayName || '—' }}</strong>
        <span class="muted">{{ auth.email }}</span>
      </div>
    </div>
  </section>

  <nav class="card card-flush narrow menu-list" aria-label="Menu akun">
    <RouterLink to="/topup" class="menu-item"><span class="tx-icon"><AppIcon name="plus" :size="18" /></span>Top-up saldo<AppIcon name="chevron" :size="18" class="menu-chevron" /></RouterLink>
    <RouterLink to="/payout" class="menu-item"><span class="tx-icon"><AppIcon name="bank" :size="18" /></span>Tarik dana ke bank<AppIcon name="chevron" :size="18" class="menu-chevron" /></RouterLink>
    <RouterLink to="/history" class="menu-item"><span class="tx-icon"><AppIcon name="list" :size="18" /></span>Riwayat transaksi<AppIcon name="chevron" :size="18" class="menu-chevron" /></RouterLink>
    <RouterLink v-if="auth.isOps" to="/ops" class="menu-item"><span class="tx-icon"><AppIcon name="shield" :size="18" /></span>Ops console<AppIcon name="chevron" :size="18" class="menu-chevron" /></RouterLink>
  </nav>

  <div class="narrow stack">
    <p class="note"><AppIcon name="shield" :size="18" :stroke="2" />LedgerPay tidak pernah meminta kata sandi lewat chat atau telepon.</p>
    <button type="button" class="btn ghost block danger" data-test="account-logout" @click="logout"><AppIcon name="logout" :size="18" />Keluar</button>
  </div>
</template>
