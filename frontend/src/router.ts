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
import CheckoutView from './pages/CheckoutView.vue'
import OpsView from './pages/OpsView.vue'
import HistoryView from './pages/HistoryView.vue'
import AccountView from './pages/AccountView.vue'
import LabView from './pages/LabView.vue'

declare module 'vue-router' {
  interface RouteMeta {
    guest?: boolean
    ops?: boolean
    /** Judul di app bar mobile. */
    title?: string
    /** Halaman tab utama (mobile): tampilkan bottom tab bar, tanpa tombol kembali. */
    tab?: boolean
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: LoginView, meta: { guest: true } },
    { path: '/register', component: RegisterView, meta: { guest: true } },
    { path: '/', component: WalletView, meta: { tab: true, title: 'Beranda' } },
    { path: '/transfer', component: TransferView, meta: { title: 'Transfer' } },
    { path: '/history', component: HistoryView, meta: { tab: true, title: 'Riwayat' } },
    { path: '/account', component: AccountView, meta: { tab: true, title: 'Akun' } },
    { path: '/topup', component: TopupView, meta: { title: 'Top-up saldo' } },
    { path: '/topups/:id', component: TopupStatusView, meta: { title: 'Status top-up' } }, // return_url dari halaman checkout provider
    { path: '/payout', component: PayoutView, meta: { title: 'Tarik dana' } },
    { path: '/payouts/:id', component: PayoutStatusView, meta: { title: 'Status tarik dana' } },
    { path: '/checkout/:id', component: CheckoutView, meta: { title: 'Pembayaran' } }, // checkout_url dari merchant (login dulu, lalu kembali ke sini)
    { path: '/lab', component: LabView, meta: { title: 'Uji ketahanan' } }, // mode demo (tombol di Beranda + sidebar)
    { path: '/ops', component: OpsView, meta: { ops: true, title: 'Ops console' } }, // server tetap memeriksa role OPS (403)
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
  if (to.meta.ops && !auth.isOps) return '/'
})
