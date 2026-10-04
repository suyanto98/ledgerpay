/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Dev: Vite mem-proxy /api ke API Go, sehingga SPA dan API SATU origin (http://localhost:5173).
// Cookie refresh (SameSite=Strict, Path=/api/v1/auth) ikut terkirim tanpa CORS. Produksi: Caddy
// melayani SPA dan me-reverse-proxy /api dengan cara yang sama (Sprint 7).
const api = process.env.LP_API_URL ?? `http://localhost:${process.env.LP_HTTP_PORT ?? '8080'}`

export default defineConfig({
  plugins: [vue()],
  server: { port: 5173, strictPort: true, proxy: { '/api': { target: api, changeOrigin: false } } },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
