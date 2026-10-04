<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError } from '../api'
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
  <div class="card">
    <h2>Daftar</h2>
    <form @submit.prevent="submit">
      <label for="name">Nama</label>
      <input id="name" v-model="form.display_name" required maxlength="80" />
      <label for="email">Email</label>
      <input id="email" v-model="form.email" type="email" autocomplete="username" required />
      <label for="password">Password (min. 12 karakter)</label>
      <input id="password" v-model="form.password" type="password" autocomplete="new-password" required minlength="12" />
      <button type="submit" :disabled="busy">Daftar</button>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
  </div>
</template>
