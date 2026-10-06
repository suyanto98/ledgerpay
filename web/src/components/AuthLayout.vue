<script setup lang="ts">
import AppIcon from './AppIcon.vue'
</script>

<template>
  <div class="auth">
    <section class="auth-hero">
      <span class="brand"><span class="brand-mark"><AppIcon name="logo" :stroke="2.2" /></span>LedgerPay</span>
      <div class="auth-hero-body">
        <h1>Uangmu tetap tepat, bahkan saat jaringan tidak.</h1>
        <ul class="perks">
          <li>
            <span class="perk-icon"><AppIcon name="check" :size="18" :stroke="2" /></span>
            <div><strong>Sekali kirim, sekali terpotong</strong><span>Transfer yang terkirim ulang karena timeout tidak memotong saldo dua kali.</span></div>
          </li>
          <li>
            <span class="perk-icon"><AppIcon name="shield" :size="18" :stroke="2" /></span>
            <div><strong>Saldo tidak pernah minus</strong><span>Transaksi bersamaan dari satu wallet diproses berurutan dengan aman.</span></div>
          </li>
          <li>
            <span class="perk-icon"><AppIcon name="chart" :size="18" :stroke="2" /></span>
            <div><strong>Dicocokkan setiap hari</strong><span>Catatan kami direkonsiliasi dengan data bank setiap hari.</span></div>
          </li>
        </ul>
      </div>
    </section>
    <section class="auth-main">
      <div class="auth-form"><slot /></div>
    </section>
  </div>
</template>
