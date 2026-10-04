import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from './stores/auth'
import LoginView from './pages/LoginView.vue'
import RegisterView from './pages/RegisterView.vue'
import WalletView from './pages/WalletView.vue'
import TransferView from './pages/TransferView.vue'
import TopupView from './pages/TopupView.vue'
import TopupStatusView from './pages/TopupStatusView.vue'
import PayoutView from './pages/PayoutView.vue'
import PayoutStatusView from './pages/PayoutStatusView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: LoginView, meta: { guest: true } },
    { path: '/register', component: RegisterView, meta: { guest: true } },
    { path: '/', component: WalletView },
    { path: '/transfer', component: TransferView },
    { path: '/topup', component: TopupView },
    { path: '/topups/:id', component: TopupStatusView }, // return_url dari halaman checkout provider
    { path: '/payout', component: PayoutView },
    { path: '/payouts/:id', component: PayoutStatusView },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuth()
  if (!auth.ready) {
    await auth.refresh() // sesi dipulihkan dari cookie; access token tidak pernah disimpan
    auth.ready = true
  }
  if (!to.meta.guest && !auth.loggedIn) return { path: '/login', query: { next: to.fullPath } }
  if (to.meta.guest && auth.loggedIn) return '/'
})
