#!/usr/bin/env bash
# Provisioning + hardening VM (C-30), IDEMPOTEN: aman dijalankan ulang. Ubuntu 24.04, sebagai root.
#   curl -fsSL <raw>/deploy/prod/bootstrap.sh | sudo DEPLOY_PUBKEY="ssh-ed25519 AAAA…" bash
set -euo pipefail
: "${DEPLOY_PUBKEY:?set DEPLOY_PUBKEY to the SSH public key of the deploy user}"
export DEBIAN_FRONTEND=noninteractive

apt-get update -q
apt-get install -yq ca-certificates curl ufw fail2ban unattended-upgrades git
command -v docker >/dev/null || { curl -fsSL https://get.docker.com | sh; }

# User deploy: hanya SSH key, anggota grup docker, tanpa sudo.
id deploy >/dev/null 2>&1 || useradd -m -s /bin/bash deploy
usermod -aG docker deploy
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
grep -qxF "$DEPLOY_PUBKEY" /home/deploy/.ssh/authorized_keys 2>/dev/null || echo "$DEPLOY_PUBKEY" >> /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys && chmod 600 /home/deploy/.ssh/authorized_keys

# SSH: tanpa password, tanpa login root.
cat > /etc/ssh/sshd_config.d/10-ledgerpay.conf <<C
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
C
systemctl reload ssh || systemctl reload sshd

# Firewall: hanya 22, 80, 443. Port ops (9090), Postgres, Prometheus, Jaeger tidak pernah publik.
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp && ufw allow 80/tcp && ufw allow 443/tcp
ufw --force enable

# Docker publish port melewati ufw (iptables DOCKER chain): semua port non-publik di compose.prod
# di-bind ke 127.0.0.1, jadi tetap tidak terjangkau dari luar.
dpkg-reconfigure -f noninteractive unattended-upgrades

# Swap 2 GB (build image di VM 8 GB).
[ -f /swapfile ] || { fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab; }

# Edge bersama (PayRun di VM yang sama): network docker `edge`, folder site Caddy, lock deploy lintas project.
docker network inspect edge >/dev/null 2>&1 || docker network create edge
install -d -m 755 -o deploy -g deploy /srv/edge /srv/edge/sites
touch /srv/deploy.lock && chown deploy:deploy /srv/deploy.lock
install -d -m 755 -o deploy -g deploy /srv/ledgerpay /srv/ledgerpay/releases
install -d -m 700 -o deploy -g deploy /srv/ledgerpay/env
install -d -m 755 -o deploy -g deploy /srv/ledgerpay/logs
# Backup ditulis container (user postgres, uid 70). Isinya terenkripsi age; private key tidak di VM.
install -d -m 750 -o 70 -g 70 /srv/ledgerpay/backups

# Backup harian 02:00 UTC (§5.2). Cron host, bukan container cron: satu komponen lebih sedikit.
cat > /etc/cron.d/ledgerpay-backup <<C
CRON_TZ=UTC
0 2 * * * deploy /srv/ledgerpay/current/deploy/prod/run-backup.sh >> /srv/ledgerpay/logs/backup.log 2>&1
C
chmod 644 /etc/cron.d/ledgerpay-backup
echo "bootstrap done. Next (from your laptop): deploy/prod/deploy.sh <host> <sha>"
