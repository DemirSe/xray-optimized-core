#!/usr/bin/env bash
# maintain.sh — manual Debian13+Xray maintenance, run from your local machine.
# Manual only: no timers/crons created, no auto-update, no auto-reboot.
# Never touches: dest/SNI, UUID, Xray binaries, unrelated timers/services.
# Usage: ./maintain.sh <ssh-dest>   # e.g. ./maintain.sh demir@host (dest never committed)
# ponytail: fixed remote blocks; BACKUP_BASE overridable for smoke tests only.
umask 077
set -u -o pipefail

SSH_DEST="${1:?usage: maintain.sh <ssh-dest>}"
BACKUP_BASE="${BACKUP_BASE:-$HOME/backups/xray}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
DEST="$BACKUP_BASE/$TS"
SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=yes"

die(){ echo "ABORT: $*" >&2; echo "partial backup kept at $DEST (STATUS: INCOMPLETE)" >&2; exit 1; }
confirm(){ local a; read -r -p "$1 [y/N] " a || return 1
  case "$a" in y|Y|yes|YES) return 0;; *) echo "(declined)"; return 1;; esac; }
rsp(){ $SSH "$SSH_DEST" "$1" </dev/null; }  # non-interactive remote block (never eats answers)
BUNDLE_FILES="os-kernel.txt xray-version.txt service-meta.txt sysctl-effective.txt sshd-effective.txt live-iptables-v4.rules live-ip6tables-v6.rules apt-policy.txt"

# --- 0. local backup dir safety (outside Git, 700/600) ---
[ -L "$BACKUP_BASE" ] && die "$BACKUP_BASE is a symlink"
[ -e "$DEST" ] && die "$DEST already exists"
mkdir -p "$BACKUP_BASE"; chmod 700 "$BACKUP_BASE"
mkdir "$DEST" || die "cannot create $DEST"; chmod 700 "$DEST"
echo "STATUS: INCOMPLETE ($TS)" > "$DEST/STATUS"; chmod 600 "$DEST/STATUS"

# --- 1. preflight (read-only, fail fast) ---
rsp 'sudo -n true' >/dev/null || die "passwordless sudo (sudo -n) failed on $SSH_DEST"
rsp '# MAINT-PHASE: preflight (read-only)
set -u
echo "== sudo =="; sudo -n true && echo SUDO_OK
echo "== apt-procs =="; pgrep -af "apt-get|aptitude|dpkg --|debconf" || echo PROCS_NONE
echo "== locks =="; sudo -n fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock 2>&1 || echo LOCKS_FREE
echo "== xray =="; systemctl is-active xray
echo "== bin-hashes =="; sha256sum /usr/local/bin/xray /usr/local/bin/xray.stock-26.3.27 /usr/local/bin/xray.trimmed-prev' > "$DEST/preflight.txt" || die "preflight ssh failed"
grep -q SUDO_OK "$DEST/preflight.txt" || die "sudo check failed"
grep -q PROCS_NONE "$DEST/preflight.txt" || die "apt/dpkg process active; retry later (never kill)"
grep -q LOCKS_FREE "$DEST/preflight.txt" || die "apt lock held; retry later (never kill)"
[ "$(awk '/^== xray ==/{getline; print; exit}' "$DEST/preflight.txt")" = "active" ] || die "xray not active before maintenance"
awk '/^== /{f=0} /^== bin-hashes ==/{f=1;next} f{print}' "$DEST/preflight.txt" > "$DEST/remote-binary-hashes.txt"
[ -s "$DEST/remote-binary-hashes.txt" ] || die "no binary hashes captured"

# --- 2. file backup: config/keys, units, firewall, sysctl, binaries, APT policy (read-only stream) ---
rsp '# MAINT-PHASE: files (read-only)
sudo -n tar -czPf - /usr/local/etc/xray/config.json /usr/local/bin/xray /usr/local/bin/xray.stock-26.3.27 /usr/local/bin/xray.trimmed-prev /etc/systemd/system/xray.service /etc/systemd/system/xray.service.d/10-donot_touch_single_conf.conf /etc/systemd/system/xray.service.d/20-gogc.conf /etc/systemd/system/xray.service.d/30-limits.conf /etc/iptables/rules.v4 /etc/iptables/rules.v6 /etc/sysctl.d/99-xray-bbr.conf /etc/logrotate.d/xray /root/xray-meta.env /root/xray-privkey /usr/local/bin/xray-watch.sh /etc/apt/apt.conf.d/20auto-upgrades /etc/apt/apt.conf.d/50unattended-upgrades /etc/ssh/sshd_config.d/50-cloud-init.conf /etc/ssh/sshd_config.d/98-allowusers.conf /etc/ssh/sshd_config.d/99-extra.conf /etc/ssh/sshd_config.d/99-netlen.conf /etc/ssh/sshd_config.d/99-no-root.conf /etc/ssh/sshd_config.d/99-nox11.conf' > "$DEST/xray-config-backup.tar.gz" || die "backup stream failed"

# --- 3. live state bundle: sysctl/units/SSH/firewall-live/bin-hashes/APT (read-only, one roundtrip) ---
rsp '# MAINT-PHASE: live (read-only)
set -u
m(){ echo "###BEGIN $1"; cat; echo "###END $1"; }
{ uname -a; echo "uptime-s: $(uptime -s)"; } | m os-kernel.txt
{ /usr/local/bin/xray version 2>&1 | head -3; } | m xray-version.txt
{ systemctl is-active xray; systemctl list-timers apt-daily.timer apt-daily-upgrade.timer --no-pager 2>&1; } | m service-meta.txt
{ /sbin/sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc 2>&1; /sbin/tc qdisc show dev eth0 2>&1; } | m sysctl-effective.txt
{ sudo -n /usr/sbin/sshd -T 2>/dev/null | grep -Ei "^(permitrootlogin|passwordauthentication|allowusers|port) " || true; } | m sshd-effective.txt
sudo -n /usr/sbin/iptables-save 2>&1 | m live-iptables-v4.rules
sudo -n /usr/sbin/ip6tables-save 2>&1 | m live-ip6tables-v6.rules
{ cat /etc/apt/apt.conf.d/20auto-upgrades; systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer 2>&1; tail -5 /var/log/unattended-upgrades/unattended-upgrades.log 2>&1 || true; } | m apt-policy.txt' > "$DEST/live.bundle" || die "live-state ssh failed"
awk -v d="$DEST" '/^###BEGIN /{f=d"/"$2; next} /^###END /{f=""; next} f{print > f}' "$DEST/live.bundle"
for f in $BUNDLE_FILES; do [ -s "$DEST/$f" ] || die "live section empty/missing: $f"; done
chmod 600 "$DEST"/*

# --- 4. validate BEFORE any mutation, then mark complete ---
tar -tzf "$DEST/xray-config-backup.tar.gz" >/dev/null || die "archive corrupt"
[ -s "$DEST/xray-config-backup.tar.gz" ] || die "archive empty"
{ echo "maintain.sh backup $TS"; echo "files:"; ls -l "$DEST"; } > "$DEST/MANIFEST.txt"
cat > "$DEST/KURTARMA-NOTLARI.txt" <<'EOF'
Restore sirasi: SHA256SUMS dogrula -> remote-binary-hashes.txt karsilastir ->
600 root:root yaz -> iptables-restore ASLA otomatik yapma (referans icin bak) ->
daemon-reload + restart (bakim penceresinde). Unencrypted local backup, restore denenmedi.
EOF
chmod 600 "$DEST"/*
# shellcheck disable=SC2046
(cd "$DEST" && sha256sum xray-config-backup.tar.gz preflight.txt live.bundle remote-binary-hashes.txt MANIFEST.txt KURTARMA-NOTLARI.txt $(echo "$BUNDLE_FILES") > SHA256SUMS.txt && sha256sum -c SHA256SUMS.txt >/dev/null) || die "checksum validation failed"
echo "STATUS: COMPLETE ($TS, all checks passed)" > "$DEST/STATUS"
echo "backup COMPLETE: $DEST"

# --- 5. disable automatic apt (mutation; idempotent; only recon-found scheduling) ---
confirm "Backup COMPLETE. Disable automatic apt timers + periodic updates?" || die "stopped before any mutation (backup kept)"
rsp '# MAINT-PHASE: disable-auto (mutation)
set -u
sudo -n systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
printf "%s\n" "APT::Periodic::Update-Package-Lists \"0\";" "APT::Periodic::Unattended-Upgrade \"0\";" "APT::Periodic::AutocleanInterval \"0\";" | sudo -n tee /etc/apt/apt.conf.d/99-disable-auto-upgrades >/dev/null
echo "== verify =="; systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer; cat /etc/apt/apt.conf.d/99-disable-auto-upgrades' > "$DEST/disable.txt" || die "disable step failed"
grep -q '"0"' "$DEST/disable.txt" || die "periodic override not confirmed"

# --- 6. manual refresh only: update on request, simulate, second confirmation, conservative upgrade ---
if confirm "Run 'apt-get update' now? (manual only)"; then
  rsp '# MAINT-PHASE: update (manual refresh)
sudo -n apt-get update' || die "apt-get update failed"
  rsp '# MAINT-PHASE: simulate (read-only)
sudo -n apt-get -s upgrade' > "$DEST/upgrade-sim.txt" || die "simulate failed"
  echo "--- simulated upgrade plan ---"; cat "$DEST/upgrade-sim.txt"
  echo "WARNING: package updates can restart services."
  if confirm "Apply conservative upgrade (keeps configs, no -y/full-upgrade/autoremove)?"; then
    $SSH "$SSH_DEST" '# MAINT-PHASE: upgrade (manual, interactive)
sudo -n apt-get upgrade -o Dpkg::Options::=--force-confold' || die "upgrade failed/aborted"
  else echo "upgrade skipped by user (no change made)"; fi
else echo "refresh skipped by user (no change made)"; fi

# --- 7. health checks (read-only); never reboot here ---
rsp '# MAINT-PHASE: health (read-only)
set -u
echo "== xray =="; systemctl is-active xray
echo "== bin-hashes =="; sha256sum /usr/local/bin/xray /usr/local/bin/xray.stock-26.3.27 /usr/local/bin/xray.trimmed-prev
echo "== port =="; /sbin/ss -ltn 2>/dev/null | grep ":443 " || ss -ltn | grep ":443 " || true
echo "== bbr =="; /sbin/sysctl net.ipv4.tcp_congestion_control; /sbin/tc qdisc show dev eth0 | head -2
echo "== reboot-signals =="; ls /var/run/reboot-required* 2>&1 || echo NO_REBOOT_FLAG; uname -r; ls -t /boot/vmlinuz-* 2>/dev/null | head -1' > "$DEST/health.txt" || die "health check ssh failed"
[ "$(awk '/^== xray ==/{getline; print; exit}' "$DEST/health.txt")" = "active" ] || die "xray not active after maintenance"
awk '/^== /{f=0} /^== bin-hashes ==/{f=1;next} f{print}' "$DEST/health.txt" > "$DEST/health-hashes.txt"
diff -q "$DEST/remote-binary-hashes.txt" "$DEST/health-hashes.txt" >/dev/null || die "xray binary identity CHANGED"
grep -q ':443' "$DEST/health.txt" || die ":443 not listening"
grep -qi 'bbr' "$DEST/health.txt" || die "BBR not active"
echo "--- reboot signals (informational; this script never reboots) ---"
awk '/^== reboot-signals ==/{f=1;next} f{print}' "$DEST/health.txt"
echo "If a reboot is needed: run it later as a separate, explicitly confirmed action."
echo "DONE. Backup: $DEST"
