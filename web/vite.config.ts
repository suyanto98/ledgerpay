/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { VitePWA } from 'vite-plugin-pwa'

// Dev: Vite mem-proxy /api ke API Go, sehingga SPA dan API SATU origin (http://localhost:5173).
// Cookie refresh (SameSite=Strict, Path=/api/v1/auth) ikut terkirim tanpa CORS. Produksi: Caddy
// melayani SPA dan me-reverse-proxy /api dengan cara yang sama (Sprint 7).
const api = process.env.LP_API_URL ?? `http://localhost:${process.env.LP_HTTP_PORT ?? '8080'}`

export default defineConfig({
  plugins: [
    vue(),
    // PWA: bisa di-install ke home screen. Service worker HANYA mem-precache aset statis build;
    // /api tidak pernah di-cache (saldo & status uang selalu dari server). registerSW.js dimuat
    // sebagai file eksternal, jadi tetap lolos CSP script-src 'self'.
    VitePWA({
      registerType: 'autoUpdate',
      injectRegister: 'script',
      includeAssets: ['favicon.svg', 'apple-touch-icon.png'],
      manifest: {
        name: 'LedgerPay',
        short_name: 'LedgerPay',
        description: 'Wallet digital: top-up, transfer, dan tarik dana.',
        lang: 'id',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        orientation: 'portrait',
        background_color: '#f3f5f9',
        theme_color: '#0b2559',
        icons: [
          { src: 'pwa-192.png', sizes: '192x192', type: 'image/png' },
          { src: 'pwa-512.png', sizes: '512x512', type: 'image/png' },
          { src: 'pwa-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
        ],
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,woff2,svg,png}'],
        navigateFallback: '/index.html',
        navigateFallbackDenylist: [/^\/api\//],
        runtimeCaching: [],
        cleanupOutdatedCaches: true,
      },
    }),
  ],
  server: { port: 5173, strictPort: true, proxy: { '/api': { target: api, changeOrigin: false } } },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
