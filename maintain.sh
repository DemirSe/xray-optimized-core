#!/usr/bin/env bash
# maintain.sh — manual Debian13+Xray maintenance, run from your local machine.
# Manual only: no timers/crons created, no auto-update, no auto-reboot.
# Never touches: dest/SNI, UUID, Xray binaries, unrelated timers/services,
#   50unattended-upgrades (Automatic-Reboot stays off).
# Usage: ./maintain.sh <ssh-dest>
# ponytail: fixed remote blocks; remote PATH fixed for sbin (see SIM.md PATH note).
umask 077
set -u -o pipefail

# --- strict ssh destination (never an option, never empty/weird) ---
SSH_DEST="${1:?usage: maintain.sh <ssh-dest>}"
case "$SSH_DEST" in -*) echo "ABORT: bad ssh-dest" >&2; exit 1;; esac
[[ "$SSH_DEST" =~ ^([A-Za-z0-9._-]+@)?[A-Za-z0-9._-]+$ ]] || { echo "ABORT: bad ssh-dest" >&2; exit 1; }
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=yes)

early(){ echo "ABORT: $*" >&2; exit 1; }

# --- fixed local backup base: $HOME/backups/xray (tests point HOME at temp) ---
[ -n "${HOME:-}" ] || early "HOME unset"
case "$HOME" in /*) ;; *) early "HOME not absolute";; esac
[ -z "${BACKUP_BASE:-}" ] || early "BACKUP_BASE override removed; set HOME instead"
BACKUP_BASE="$HOME/backups/xray"
for p in "$HOME" "$HOME/backups" "$BACKUP_BASE"; do
  [ -L "$p" ] && early "$p is a symlink"
done
top="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$top" ] && case "$BACKUP_BASE/" in "$top/"*) early "backup base inside git tree";; esac
TS="$(date -u +%Y%m%dT%H%M%SZ)"
DEST="$BACKUP_BASE/$TS"
[ -e "$DEST" ] && early "$DEST already exists"
mkdir -p "$BACKUP_BASE" || early "cannot create $BACKUP_BASE"
chmod 700 "$BACKUP_BASE" || early "cannot chmod $BACKUP_BASE"
git -C "$BACKUP_BASE" rev-parse --is-inside-work-tree >/dev/null 2>&1 && early "backup base inside git repo"
mkdir "$DEST" || early "cannot create $DEST"
chmod 700 "$DEST" || early "cannot chmod $DEST"

BACKUP_DONE=0
die(){ echo "ABORT: $*" >&2
  if [ "$BACKUP_DONE" = 1 ]; then echo "backup at $DEST (STATUS: COMPLETE) preserved" >&2
  else echo "backup at $DEST (STATUS: INCOMPLETE) preserved, fix and rerun" >&2; fi
  exit 1; }
confirm(){ local a; read -r -p "$1 [y/N] " a || return 1
  case "$a" in y|Y|yes|YES) return 0;; *) echo "(declined)"; return 1;; esac; }
rsp(){ "${SSH[@]}" "$SSH_DEST" "$1" </dev/null; }  # non-interactive: stdin isolated
RHEAD='set -euo pipefail
export PATH="$PATH:/usr/local/bin:/usr/local/sbin:/usr/sbin:/sbin"'
REQ='usr/local/etc/xray/config.json usr/local/bin/xray usr/local/bin/xray.stock-26.3.27 usr/local/bin/xray.trimmed-prev etc/systemd/system/xray.service etc/systemd/system/xray.service.d/10-donot_touch_single_conf.conf etc/systemd/system/xray.service.d/20-gogc.conf etc/systemd/system/xray.service.d/30-limits.conf etc/iptables/rules.v4 etc/iptables/rules.v6 etc/sysctl.d/99-xray-bbr.conf etc/logrotate.d/xray root/xray-meta.env root/xray-privkey usr/local/bin/xray-watch.sh etc/apt/apt.conf.d/20auto-upgrades etc/apt/apt.conf.d/50unattended-upgrades etc/ssh/sshd_config.d/50-cloud-init.conf etc/ssh/sshd_config.d/98-allowusers.conf etc/ssh/sshd_config.d/99-extra.conf etc/ssh/sshd_config.d/99-netlen.conf etc/ssh/sshd_config.d/99-no-root.conf etc/ssh/sshd_config.d/99-nox11.conf'
OPT='etc/apt/apt.conf.d/99-disable-auto-upgrades'
BINS='usr/local/bin/xray usr/local/bin/xray.stock-26.3.27 usr/local/bin/xray.trimmed-prev'

echo "STATUS: INCOMPLETE ($TS)" > "$DEST/STATUS" || die "cannot write STATUS"
chmod 600 "$DEST/STATUS" || die "cannot chmod STATUS"

# --- busy check: exact process names (never matches own shell) + lock-holder test ---
busy_check(){ # busy_check <stage>: read-only, dies if apt is working
  rsp "$RHEAD
echo '== apt-procs =='
ps -e -o comm= | grep -x -e apt-get -e apt -e dpkg -e aptitude -e debconf -e dpkg-deb || echo PROCS_NONE
echo '== locks =='
if sudo -n fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock >/dev/null 2>&1; then echo LOCKS_HELD; else echo LOCKS_FREE; fi" > "$DEST/busy-$1.txt" || die "busy check ($1) ssh failed"
  [ "$(awk '/^== apt-procs ==/{getline; print; exit}' "$DEST/busy-$1.txt")" = "PROCS_NONE" ] || die "apt/dpkg process active at $1; retry later (never kill, never stop locks)"
  [ "$(awk '/^== locks ==/{getline; print; exit}' "$DEST/busy-$1.txt")" = "LOCKS_FREE" ] || die "apt lock held at $1; retry later (never kill, never stop locks)"
}

# --- 1. preflight (read-only, fail fast; prerequisite tools included) ---
rsp "$RHEAD
for c in systemctl sha256sum tar apt-config sshd iptables-save ip6tables-save ss tc sysctl; do command -v \"\$c\" >/dev/null || { echo \"MISSING-TOOL: \$c\" >&2; exit 12; }; done
sudo -n true && echo SUDO_OK
systemctl is-active xray
sha256sum /usr/local/bin/xray /usr/local/bin/xray.stock-26.3.27 /usr/local/bin/xray.trimmed-prev" > "$DEST/preflight.txt" || die "preflight ssh failed (sudo, xray-active, tools, or binary presence)"
busy_check pre
grep -qx 'SUDO_OK' "$DEST/preflight.txt" || die "sudo check failed"
[ "$(grep -c '^active$' "$DEST/preflight.txt")" = 1 ] || die "xray not active before maintenance"
awk '/ [\/]/{print}' "$DEST/preflight.txt" | grep -F /usr/local/bin/xray > "$DEST/remote-binary-hashes.txt" || die "no binary hashes captured"
[ "$(wc -l < "$DEST/remote-binary-hashes.txt")" = 3 ] || die "incomplete binary hashes"

# --- 2. file backup: required entries must exist remotely; override included on rerun ---
rsp "$RHEAD
list=''
# shellcheck disable=SC2086
for f in $REQ; do sudo -n ls -d -- \"/\$f\" >/dev/null || { echo \"MISSING: /\$f\" >&2; exit 11; }; list=\"\$list \$f\"; done
for f in $OPT; do if sudo -n ls -d -- \"/\$f\" >/dev/null 2>&1; then list=\"\$list \$f\"; fi; done
sudo -n tar -czf - -C / \$list" > "$DEST/xray-config-backup.tar.gz" || die "backup stream failed (remote tar aborted, see error above)"

# --- 3. live state, one file per call: remote failure aborts, empty result aborts ---
get(){ "${SSH[@]}" "$SSH_DEST" "$RHEAD
$1" </dev/null > "$2" || die "fetch failed: $2"; [ -s "$2" ] || die "empty result: $2"; }
get "uname -a; echo \"uptime-s: \$(uptime -s)\"" "$DEST/os-kernel.txt"
get "xray version 2>/dev/null | head -3" "$DEST/xray-version.txt"
get "systemctl cat xray" "$DEST/service-effective.txt"
get "systemctl is-active xray; systemctl list-timers apt-daily.timer apt-daily-upgrade.timer --no-pager" "$DEST/service-meta.txt"
get "sysctl -n net.ipv4.tcp_congestion_control; tc qdisc show dev eth0" "$DEST/sysctl-effective.txt"
get "sudo -n sshd -T" "$DEST/sshd-effective.txt"
get "sudo -n iptables-save" "$DEST/live-iptables-v4.rules"
get "sudo -n ip6tables-save" "$DEST/live-ip6tables-v6.rules"
get "cat /etc/apt/apt.conf.d/20auto-upgrades; if [ -f /etc/apt/apt.conf.d/99-disable-auto-upgrades ]; then cat /etc/apt/apt.conf.d/99-disable-auto-upgrades; fi; echo '== apt-config =='; apt-config dump | grep -i periodic; echo '== timers =='; systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer || true; systemctl is-active apt-daily.timer apt-daily-upgrade.timer || true; if [ -f /var/log/unattended-upgrades/unattended-upgrades.log ]; then tail -5 /var/log/unattended-upgrades/unattended-upgrades.log; else echo 'no unattended log'; fi" "$DEST/apt-policy.txt"
chmod 600 "$DEST"/* || die "cannot chmod backup files"

# --- 4. validate BEFORE any mutation: expected entries + binary content vs pre-hashes ---
tar -tzf "$DEST/xray-config-backup.tar.gz" > "$DEST/archive-list.txt" || die "archive unreadable"
# shellcheck disable=SC2086
for e in $REQ; do grep -qxF "$e" "$DEST/archive-list.txt" || die "archive missing: $e"; done
mkdir "$DEST/.xbin" || die "cannot create extract dir"
# shellcheck disable=SC2086
tar -xzf "$DEST/xray-config-backup.tar.gz" -C "$DEST/.xbin" $BINS || die "binary extract failed"
i=0; for b in $BINS; do
  i=$((i+1)); n="$(basename "$b")"
  h1="$(sed -n "${i}p" "$DEST/remote-binary-hashes.txt" | awk '{print $1}')"
  h2="$(sha256sum "$DEST/.xbin/$b" | awk '{print $1}')"
  [ -n "$h1" ] && [ "$h1" = "$h2" ] || die "binary content mismatch: $n"
done
rm -rf "$DEST/.xbin"
{ echo "maintain.sh backup $TS"; echo "entries: $(wc -l < "$DEST/archive-list.txt")"; ls -l "$DEST"; } > "$DEST/MANIFEST.txt" || die "cannot write MANIFEST"
cat > "$DEST/KURTARMA-NOTLARI.txt" <<'EOF' || die "cannot write KURTARMA-NOTLARI"
Restore sirasi: SHA256SUMS dogrula -> remote-binary-hashes.txt karsilastir ->
600 root:root yaz -> iptables-restore ASLA otomatik yapma (referans icin bak) ->
daemon-reload + restart (bakim penceresinde). Unencrypted local backup, restore denenmedi.
EOF
chmod 600 "$DEST"/MANIFEST.txt "$DEST"/KURTARMA-NOTLARI.txt || die "cannot chmod metadata"
(cd "$DEST" && sha256sum xray-config-backup.tar.gz preflight.txt busy-pre.txt archive-list.txt remote-binary-hashes.txt os-kernel.txt xray-version.txt service-effective.txt service-meta.txt sysctl-effective.txt sshd-effective.txt live-iptables-v4.rules live-ip6tables-v6.rules apt-policy.txt MANIFEST.txt KURTARMA-NOTLARI.txt > SHA256SUMS.txt && sha256sum -c SHA256SUMS.txt >/dev/null) || die "checksum validation failed"
echo "STATUS: COMPLETE ($TS, all checks passed)" > "$DEST/STATUS" || die "cannot write STATUS"
BACKUP_DONE=1
echo "backup COMPLETE: $DEST"

# --- 5. recheck busyness, then disable automatic apt (idempotent, reversible) ---
busy_check predisable
confirm "Backup COMPLETE. Disable automatic apt timers + periodic updates?" || die "stopped before any mutation (backup kept)"
rsp "$RHEAD
sudo -n systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
printf '%s\n' 'APT::Periodic::Update-Package-Lists \"0\";' 'APT::Periodic::Unattended-Upgrade \"0\";' 'APT::Periodic::Download-Upgradeable-Packages \"0\";' 'APT::Periodic::AutocleanInterval \"0\";' | sudo -n tee /etc/apt/apt.conf.d/99-disable-auto-upgrades >/dev/null" || die "disable step failed (systemctl/tee aborted)"
rsp "$RHEAD
systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer || true
systemctl is-active apt-daily.timer apt-daily-upgrade.timer || true
apt-config dump | grep -i periodic" > "$DEST/disable-verify.txt" || die "disable verify ssh failed"
[ "$(grep -cx 'disabled' "$DEST/disable-verify.txt")" = 2 ] || die "timers not disabled"
[ "$(grep -cx 'inactive' "$DEST/disable-verify.txt")" = 2 ] || die "timers not inactive"
for k in Update-Package-Lists Unattended-Upgrade Download-Upgradeable-Packages AutocleanInterval; do
  grep -q "APT::Periodic::$k \"0\";" "$DEST/disable-verify.txt" || die "periodic not effective: $k"
done
grep -i periodic "$DEST/disable-verify.txt" | grep -q '"1"' && die "periodic still enabled somewhere"
busy_check postdisable

# --- 6. manual refresh only: update and upgrade separately gated ---
if confirm "Run 'apt-get update' now? (manual only)"; then
  rsp "$RHEAD
sudo -n apt-get update -o APT::Update::Error-Mode=any" || die "apt-get update failed"
  rsp "$RHEAD
sudo -n apt-get -s upgrade" > "$DEST/upgrade-sim.txt" || die "simulate failed"
  echo "--- simulated upgrade plan ---"; cat "$DEST/upgrade-sim.txt"
  echo "WARNING: package updates can restart services."
  if confirm "Apply conservative upgrade (keeps configs, no -y/full-upgrade/autoremove)?"; then
    "${SSH[@]}" "$SSH_DEST" "$RHEAD
sudo -n apt-get upgrade -o Dpkg::Options::=--force-confold" || die "upgrade failed/aborted"
  else echo "upgrade skipped by user (no change made)"; fi
else echo "refresh skipped by user (no change made)"; fi

# --- 7. health checks (read-only); never reboot here ---
rsp "$RHEAD
echo '== xray =='; systemctl is-active xray
echo '== identity =='; pid=\"\$(systemctl show -p MainPID --value xray)\"; [ -n \"\$pid\" ] && [ \"\$pid\" != 0 ]; sudo -n sha256sum \"/proc/\$pid/exe\" /usr/local/bin/xray
echo '== port =='; sudo -n ss -ltnp | grep ':443 '
echo '== cc =='; sysctl -n net.ipv4.tcp_congestion_control
echo '== qdisc =='; tc qdisc show dev eth0
echo '== reboot =='; if ls /var/run/reboot-required* >/dev/null 2>&1; then echo REBOOT_FLAG_PRESENT; else echo NO_REBOOT_FLAG; fi; uname -r; ls -t /boot/vmlinuz-* 2>/dev/null | head -1 || true" > "$DEST/health.txt" || die "health check ssh failed"
[ "$(awk '/^== xray ==/{getline; print; exit}' "$DEST/health.txt")" = "active" ] || die "xray not active after maintenance"
awk '/^== /{f=0} /^== identity ==/{f=1;next} f{print}' "$DEST/health.txt" > "$DEST/health-hashes.txt"
[ "$(awk '{print $1}' "$DEST/health-hashes.txt" | sort -u | wc -l)" = 1 ] || die "running binary differs from disk"
h0="$(grep -F /usr/local/bin/xray "$DEST/remote-binary-hashes.txt" | head -1 | awk '{print $1}')"
[ -n "$h0" ] && [ "$(head -1 "$DEST/health-hashes.txt" | awk '{print $1}')" = "$h0" ] || die "xray binary identity CHANGED vs backup"
awk '/^== port ==/{f=1;next} /^== /{f=0} f{print}' "$DEST/health.txt" | grep -q 'xray' || die ":443 not owned by xray"
[ "$(awk '/^== cc ==/{getline; print; exit}' "$DEST/health.txt")" = "bbr" ] || die "BBR not effective (want exactly: bbr)"
awk '/^== qdisc ==/{f=1;next} /^== /{f=0} f{print}' "$DEST/health.txt" | grep -q '^qdisc fq .* root' || die "fq qdisc not on root"
echo "--- reboot signals (informational; this script never reboots) ---"
awk '/^== reboot ==/{f=1;next} f{print}' "$DEST/health.txt"
echo "If a reboot is needed: run it later as a separate, explicitly confirmed action."
echo "DONE. Backup: $DEST"
