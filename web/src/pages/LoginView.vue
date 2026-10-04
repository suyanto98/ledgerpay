<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuth } from '../stores/auth'

const email = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)
const auth = useAuth()
const route = useRoute()
const router = useRouter()

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await auth.login(email.value, password.value)
    router.push((route.query.next as string) || '/')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="card">
    <h2>Masuk</h2>
    <form @submit.prevent="submit">
      <label for="email">Email</label>
      <input id="email" v-model="email" type="email" autocomplete="username" required />
      <label for="password">Password</label>
      <input id="password" v-model="password" type="password" autocomplete="current-password" required />
      <button type="submit" :disabled="busy">Masuk</button>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
    <p>Belum punya akun? <RouterLink to="/register">Daftar</RouterLink></p>
  </div>
</template>
