#!/usr/bin/env bash
# Smoke test for maintain.sh: offline, fake ssh, no network, no server.
# Proves: (1) backup failure blocks all mutation; (2) declining upgrade blocks upgrade.
# ponytail: single file, PATH-stubbed ssh, temp BACKUP_BASE.
umask 077
set -u -o pipefail
cd "$(dirname "$0")/.." || exit 1
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
export BACKUP_BASE="$T/backups" FAKE_MODE=ok
mkdir -p "$T/bin" "$T/fix"
echo dummy-config > "$T/fix/config.json"

H='deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef'
cat > "$T/bin/ssh" <<EOF
#!/usr/bin/env bash
script="\${*: -1}"; echo "\$script" >> "$T/ssh.log"
case "\$script" in
  *"MAINT-PHASE: files"*) [ "\$FAKE_MODE" = ok ] && tar -czf - -C "$T/fix" config.json || exit 1;;
  *"MAINT-PHASE: live"*)
    for s in os-kernel.txt xray-version.txt service-meta.txt sysctl-effective.txt sshd-effective.txt live-iptables-v4.rules live-ip6tables-v6.rules apt-policy.txt; do
      echo "###BEGIN \$s"; echo "fake-content \$s"; echo "###END \$s"; done;;
  *"MAINT-PHASE: preflight"*|*"MAINT-PHASE: health"*)
    printf '%s\n' "== sudo ==" SUDO_OK "== apt-procs ==" PROCS_NONE "== locks ==" LOCKS_FREE "== xray ==" active "== bin-hashes ==" \
      "$H  /usr/local/bin/xray" "$H  /usr/local/bin/xray.stock-26.3.27" "$H  /usr/local/bin/xray.trimmed-prev" \
      "== port ==" "LISTEN 0 0 *:443" "== bbr ==" "net.ipv4.tcp_congestion_control = bbr" "fq" "== reboot-signals ==" NO_REBOOT_FLAG;;
  *"MAINT-PHASE: disable-auto"*) printf '%s\n' "== verify ==" "disabled" "disabled" 'APT::Periodic::Unattended-Upgrade "0";';;
  *"sudo -n true"*) ;;
  *) echo "fake-ok \$script";;
esac
exit 0
EOF
chmod +x "$T/bin/ssh"
export PATH="$T/bin:$PATH"

bash -n maintain.sh || { echo "FAIL: syntax"; exit 1; }
echo "syntax OK"

# Test 1: backup stream fails -> nonzero exit, no mutation, INCOMPLETE kept.
export FAKE_MODE=backup-fail
: > "$T/ssh.log"
printf '\n' | ./maintain.sh fake-dest >/dev/null 2>&1; rc=$?
[ $rc -ne 0 ] || { echo "FAIL: backup-fail exited 0"; exit 1; }
grep -q 'disable-auto\|MAINT-PHASE: upgrade' "$T/ssh.log" && { echo "FAIL: mutation after failed backup"; exit 1; }
grep -rq 'STATUS: INCOMPLETE' "$BACKUP_BASE" || { echo "FAIL: INCOMPLETE not kept"; exit 1; }
echo "test1 OK: backup failure blocks mutation, INCOMPLETE kept"

# Test 2: backup OK but user declines everything -> no disable, no update/upgrade.
export FAKE_MODE=ok
sleep 2  # new timestamp dir
: > "$T/ssh.log"
printf 'n\nn\n' | ./maintain.sh fake-dest >/dev/null 2>&1
grep -q 'disable-auto' "$T/ssh.log" && { echo "FAIL: disable ran after decline"; exit 1; }
grep -q 'apt-get upgrade' "$T/ssh.log" && { echo "FAIL: upgrade ran after decline"; exit 1; }
grep -q 'apt-get update' "$T/ssh.log" && { echo "FAIL: update ran after decline"; exit 1; }
grep -rq 'STATUS: COMPLETE' "$BACKUP_BASE" || { echo "FAIL: backup not COMPLETE"; exit 1; }
echo "test2 OK: declines block disable/update/upgrade, backup COMPLETE"
echo "ALL SMOKE TESTS PASSED"
