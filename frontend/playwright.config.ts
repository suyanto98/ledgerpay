import { defineConfig, devices } from '@playwright/test'

// E2E butuh stack backend berjalan (`make up`: api + worker + mock-provider + postgres).
// Playwright menyalakan Vite dev server sendiri; /api di-proxy ke API (LP_HTTP_PORT).
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: { baseURL: 'http://localhost:5173', testIdAttribute: 'data-test', trace: 'retain-on-failure', ...devices['Desktop Chrome'] },
  webServer: { command: 'yarn dev', url: 'http://localhost:5173', reuseExistingServer: !process.env.CI, timeout: 60_000 },
})
