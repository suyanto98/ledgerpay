<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useAuth } from './stores/auth'

const auth = useAuth()
const router = useRouter()
async function logout() {
  await auth.logout()
  router.push('/login')
}
</script>

<template>
  <header>
    <strong>LedgerPay</strong>
    <template v-if="auth.loggedIn">
      <RouterLink to="/">Wallet</RouterLink>
      <RouterLink to="/transfer">Transfer</RouterLink>
      <RouterLink to="/topup">Top-up</RouterLink>
      <span class="spacer" />
      <button class="link" data-test="logout" @click="logout">Keluar</button>
    </template>
  </header>
  <main><RouterView /></main>
</template>
