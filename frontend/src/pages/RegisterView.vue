<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError } from '../api'
import AuthLayout from '../components/AuthLayout.vue'
import { useAuth } from '../stores/auth'

const form = ref({ email: '', password: '', display_name: '' })
const error = ref('')
const busy = ref(false)
const router = useRouter()

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await api('/auth/register', { method: 'POST', body: form.value })
    await useAuth().login(form.value.email, form.value.password)
    router.push('/')
  } catch (e) {
    error.value = e instanceof ApiError && e.code === 'EMAIL_ALREADY_REGISTERED' ? 'Email sudah terdaftar' : (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthLayout>
    <form class="form" @submit.prevent="submit">
      <div>
        <h2>Buat akun</h2>
        <p class="lead">Gratis, langsung dapat wallet LedgerPay.</p>
      </div>
      <div class="field">
        <label for="name">Nama</label>
        <input id="name" v-model="form.display_name" class="input" required maxlength="80" autocomplete="name" />
      </div>
      <div class="field">
        <label for="email">Email</label>
        <input id="email" v-model="form.email" class="input" type="email" autocomplete="username" required />
      </div>
      <div class="field">
        <label for="password">Kata sandi (min. 12 karakter)</label>
        <input id="password" v-model="form.password" class="input" type="password" autocomplete="new-password" required minlength="12" />
      </div>
      <p v-if="error" class="alert error" role="alert">{{ error }}</p>
      <button type="submit" class="btn block" :disabled="busy">Daftar</button>
      <p class="auth-switch">Sudah punya akun? <RouterLink to="/login">Masuk</RouterLink></p>
    </form>
  </AuthLayout>
</template>
