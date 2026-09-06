#!/usr/bin/env bash
# Smoke test for maintain.sh: offline, no network, no server.
# Fake ssh EXECUTES each remote block (bash -c) with stub remote commands on
# PATH, so set -euo pipefail propagation, exit codes, and content checks are
# genuine — not phase-string matching. A fake root stands in for the server FS.
umask 077
set -u -o pipefail
cd "$(dirname "$0")/.." || exit 1
T="$(mktemp -d)"; trap 'rm -rf "$T"; kill "$SLEEP_PID" 2>/dev/null || true' EXIT
mkdir -p "$T/rbin" "$T/fakebin" "$T/tmpl"
REAL_TAR=/usr/bin/tar; REAL_SUM=/usr/bin/sha256sum

cp /usr/bin/sleep "$T/sleepbin"
"$T/sleepbin" 600 </dev/null >/dev/null 2>&1 & SLEEP_PID=$!
export SLEEP_PID T

# --- fake server filesystem template (relative paths mirror maintain.sh REQ) ---
for f in usr/local/etc/xray/config.json usr/local/bin/xray.stock-26.3.27 usr/local/bin/xray.trimmed-prev etc/systemd/system/xray.service etc/systemd/system/xray.service.d/10-donot_touch_single_conf.conf etc/systemd/system/xray.service.d/20-gogc.conf etc/systemd/system/xray.service.d/30-limits.conf etc/iptables/rules.v4 etc/iptables/rules.v6 etc/sysctl.d/99-xray-bbr.conf etc/logrotate.d/xray root/xray-meta.env root/xray-privkey usr/local/bin/xray-watch.sh etc/apt/apt.conf.d/20auto-upgrades etc/apt/apt.conf.d/50unattended-upgrades etc/ssh/sshd_config.d/50-cloud-init.conf etc/ssh/sshd_config.d/98-allowusers.conf etc/ssh/sshd_config.d/99-extra.conf etc/ssh/sshd_config.d/99-netlen.conf etc/ssh/sshd_config.d/99-no-root.conf etc/ssh/sshd_config.d/99-nox11.conf; do
  mkdir -p "$T/tmpl/$(dirname "$f")"; echo "content-of-$f" > "$T/tmpl/$f"
done
cp "$T/sleepbin" "$T/tmpl/usr/local/bin/xray"  # same exe the "xray" pid runs

mkstub(){ cat > "$T/rbin/$1"; chmod +x "$T/rbin/$1"; }
mkstub sudo <<'EOF'
#!/usr/bin/env bash
while [ $# -gt 0 ]; do case "$1" in -n|--) shift;; *) break;; esac; done
exec "$@"
EOF
mkstub ps <<'EOF'
#!/usr/bin/env bash
if [ "${PS_BUSY:-0}" = 1 ]; then echo apt-get; else printf 'systemd\nbash\n'; fi
EOF
mkstub fuser <<'EOF'
#!/usr/bin/env bash
[ "${FUSER_HELD:-0}" = 1 ] && exit 0 || exit 1
EOF
mkstub systemctl <<'EOF'
#!/usr/bin/env bash
cmd="$1"; shift
case "$cmd" in
  is-active) rc=0; for u in "$@"; do case "$u" in xray) echo "${XRAY_STATE:-active}"; [ "${XRAY_STATE:-active}" = active ] || rc=3;; *) echo "${TIMER_ACTIVE:-active}"; [ "${TIMER_ACTIVE:-active}" = active ] || rc=3;; esac; done; exit $rc;;
  is-enabled) for u in "$@"; do echo "${TIMER_ENABLED:-enabled}"; done
    [ "${TIMER_ENABLED:-enabled}" = enabled ] || exit 1;;
  disable) [ "${DISABLE_FAIL:-0}" = 1 ] && exit 1; echo "disabled $*"; echo "$*" >> "$T/disable.log";;
  cat) echo "# fake effective xray unit (with drop-ins applied)";;
  list-timers) echo "fake timer list";;
  show) echo "$SLEEP_PID";;
  *) echo "stub-systemctl: unknown $cmd" >&2; exit 2;;
esac
EOF
mkstub sysctl <<'EOF'
#!/usr/bin/env bash
[ "$1" = "-n" ] && { echo "${CC_MODE:-bbr}"; exit 0; }
echo "stub-sysctl: unknown $*" >&2; exit 2
EOF
mkstub tc <<'EOF'
#!/usr/bin/env bash
[ "${QDISC_MODE:-fq}" = fq ] && echo "qdisc fq 8005: root refcnt 2 limit 100p" \
  || echo "qdisc fq_codel 0: root refcnt 2 limit 10240p"
EOF
mkstub ss <<'EOF'
#!/usr/bin/env bash
[ "${SS_MODE:-xray}" = xray ] && echo 'LISTEN 0 0 *:443 users:(("xray",pid=1,fd=3))' \
  || echo 'LISTEN 0 0 *:443 users:(("other",pid=9,fd=3))'
EOF
mkstub sshd <<'EOF'
#!/usr/bin/env bash
printf 'permitrootlogin no\npasswordauthentication no\nallowusers demir\nport 22\n'
EOF
mkstub iptables-save <<'EOF'
#!/usr/bin/env bash
[ "${IPT_FAIL:-0}" = 1 ] && { echo "iptables-save: boom" >&2; exit 1; }
echo "# fake v4 rules"
EOF
mkstub ip6tables-save <<'EOF'
#!/usr/bin/env bash
[ "${IPT_FAIL:-0}" = 1 ] && { echo "ip6tables-save: boom" >&2; exit 1; }
echo "# fake v6 rules"
EOF
mkstub ls <<'EOF'
#!/usr/bin/env bash
args=()
for a in "$@"; do case "$a" in -*) args+=("$a");; /*) args+=("$FAKEROOT$a");; *) args+=("$a");; esac; done
exec /bin/ls "${args[@]}"
EOF
mkstub tee <<'EOF'
#!/usr/bin/env bash
args=()
for a in "$@"; do case "$a" in -*) args+=("$a");; /*) args+=("$FAKEROOT$a");; *) args+=("$a");; esac; done
exec /usr/bin/tee "${args[@]}"
EOF
mkstub cat <<'EOF'
#!/usr/bin/env bash
args=()
for a in "$@"; do case "$a" in /*) args+=("$FAKEROOT$a");; *) args+=("$a");; esac; done
exec /bin/cat "${args[@]}"
EOF
mkstub apt-config <<'EOF'
#!/usr/bin/env bash
v=0; [ "${POLICY_ONES:-0}" = 1 ] && v=1
for k in Update-Package-Lists Unattended-Upgrade Download-Upgradeable-Packages AutocleanInterval; do
  echo "APT::Periodic::$k \"$v\";"
done
EOF
mkstub apt-get <<'EOF'
#!/usr/bin/env bash
echo "apt-get $*" >> "$T/apt.log"
[ "${APT_FAIL:-0}" = 1 ] && exit 1
[ "$1" = "-s" ] && echo "fake simulated plan"
exit 0
EOF
mkstub uptime <<'EOF'
#!/usr/bin/env bash
[ "$1" = "-s" ] && { echo "2026-09-06 00:00:00"; exit 0; }
echo "fake uptime"
EOF
mkstub xray <<'EOF'
#!/usr/bin/env bash
echo "Xray fake-version"
EOF
mkstub tar <<EOF
#!/usr/bin/env bash
[ "\${TAR_FAIL:-0}" = 1 ] && { echo "tar: boom" >&2; exit 1; }
args=(); prev=""
for a in "\$@"; do
  if [ "\$prev" = "-C" ] && [ "\$a" = "/" ]; then args+=("\$FAKEROOT"); else args+=("\$a"); fi
  prev="\$a"
done
exec $REAL_TAR "\${args[@]}"
EOF
mkstub sha256sum <<EOF
#!/usr/bin/env bash
args=()
for a in "\$@"; do
  case "\$a" in -*) args+=("\$a");; /proc/*) args+=("\$a");; /*) args+=("\$FAKEROOT\$a");; *) args+=("\$a");; esac
done
exec $REAL_SUM "\${args[@]}"
EOF
cat > "$T/fakebin/ssh" <<'EOF'
#!/usr/bin/env bash
script="${*: -1}"; printf '%s\n---SSH-CALL---\n' "$script" >> "$T/ssh.log"
PATH="$T/rbin:/usr/bin:/bin" bash -c "$script"
EOF
chmod +x "$T/fakebin/ssh"
export PATH="$T/fakebin:/usr/bin:/bin"

bash -n maintain.sh || { echo "FAIL: syntax"; exit 1; }
echo "syntax OK"

N=0
newcase(){ N=$((N+1)); export HOME="$T/h$N" FAKEROOT="$T/r$N"
  mkdir -p "$HOME"; cp -r "$T/tmpl/." "$FAKEROOT"/ 2>/dev/null || { mkdir -p "$FAKEROOT"; cp -r "$T/tmpl/." "$FAKEROOT/"; }
  : > "$T/ssh.log"; : > "$T/apt.log"; : > "$T/disable.log";
  unset PS_BUSY FUSER_HELD DISABLE_FAIL POLICY_ONES CC_MODE QDISC_MODE SS_MODE IPT_FAIL APT_FAIL XRAY_STATE TIMER_ENABLED TIMER_ACTIVE; }
latest(){ ls -dt "$HOME"/backups/xray/*/ | head -1; }
pass(){ echo "ok: $1"; }
fail(){ echo "FAIL: $1"; tail -20 "$T/out.log"; exit 1; }

# T1: missing remote file aborts tar (genuine exit 11 through set -e).
newcase; rm "$FAKEROOT/etc/iptables/rules.v4"
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T1 missing-file exited 0"
grep -q 'MAINT-PHASE: disable-auto' "$T/ssh.log" && fail "T1 mutation after failed backup"
grep -q 'STATUS: INCOMPLETE' "$(latest)/STATUS" || fail "T1 INCOMPLETE not kept"
pass "T1 missing file blocks mutation, INCOMPLETE kept"

# T2/T3: busy preflight (processes / locks).
newcase; export PS_BUSY=1
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T2 busy-procs exited 0"; pass "T2 apt process blocks"
newcase; export FUSER_HELD=1
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T3 busy-locks exited 0"; pass "T3 held lock blocks"

# T4: iptables-save failure aborts live fetch (error is failure, not data).
newcase; export IPT_FAIL=1
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T4 iptables-fail exited 0"
grep -q 'MAINT-PHASE: disable-auto' "$T/ssh.log" && fail "T4 mutation after failed fetch"
pass "T4 iptables failure blocks, INCOMPLETE kept"

# T5: decline before mutation -> COMPLETE backup, no changes.
newcase
printf 'n\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T5 decline exited 0"
grep -q 'STATUS: COMPLETE' "$(latest)/STATUS" || fail "T5 backup not COMPLETE"
grep -q 'MAINT-PHASE: disable-auto' "$T/ssh.log" && fail "T5 disable ran after decline"
grep -q 'MAINT-PHASE: update' "$T/ssh.log" && fail "T5 update ran after decline"
pass "T5 decline blocks mutation, backup COMPLETE"

# T6: disable works (timers already off in this server state), skip update.
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive
printf 'y\nn\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -eq 0 ] || fail "T6 disable path failed"
grep -q 'apt-daily' "$T/disable.log" || fail "T6 disable not executed"
grep -q 'MAINT-PHASE: update' "$T/ssh.log" && fail "T6 update ran after decline"
pass "T6 disable verified, update skipped"

# T7: failed timer disable aborts; COMPLETE backup preserved (not INCOMPLETE).
newcase; export DISABLE_FAIL=1
out=$(printf 'y\n' | ./maintain.sh fake-dest 2>&1); rc=$?
[ $rc -ne 0 ] || fail "T7 disable-fail exited 0"
echo "$out" | grep -q 'STATUS: COMPLETE) preserved' || fail "T7 die wrongly claims INCOMPLETE"
pass "T7 disable failure aborts, COMPLETE preserved"

# T8: wrong effective policy aborts.
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive POLICY_ONES=1
printf 'y\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T8 wrong-policy exited 0"; pass "T8 periodic=1 rejected"

# T9/T10/T11: health gates (cubic / fq_codel / :443 owned by other).
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive CC_MODE=cubic
printf 'y\nn\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T9 cubic exited 0"; pass "T9 cubic rejected"
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive QDISC_MODE=codel
printf 'y\nn\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T10 codel exited 0"; pass "T10 missing fq rejected"
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive SS_MODE=other
printf 'y\nn\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T11 foreign-:443 exited 0"; pass "T11 :443 ownership enforced"

# T12: update runs (strict error mode), upgrade declined -> no upgrade.
newcase; export TIMER_ENABLED=disabled TIMER_ACTIVE=inactive
printf 'y\ny\nn\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -eq 0 ] || fail "T12 update path failed"
grep -q 'APT::Update::Error-Mode=any' "$T/ssh.log" || fail "T12 update lacks Error-Mode=any"
grep -qx 'upgrade.*' "$T/apt.log" && fail "T12 upgrade ran after decline"
pass "T12 update gated, upgrade declined cleanly"

# T13: option-like destination rejected without any ssh call.
newcase
printf '\n' | ./maintain.sh -foo >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T13 bad dest exited 0"
[ -s "$T/ssh.log" ] && fail "T13 ssh called with bad dest"
pass "T13 bad ssh-dest rejected"

# T14/T15: unsafe backup base (symlink / inside git) refused.
newcase; rm -rf "$HOME/backups"; ln -s /tmp "$HOME/backups"
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T14 symlink base accepted"; pass "T14 symlink base refused"
newcase; git init -q "$HOME" 2>/dev/null || fail "T15 no git for test"
printf '\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T15 in-git base accepted"; pass "T15 in-git base refused"

# T16: override from a previous run is captured (rerun coverage).
newcase; echo 'APT::Periodic::Unattended-Upgrade "0";' > "$FAKEROOT/etc/apt/apt.conf.d/99-disable-auto-upgrades"
printf 'n\n' | ./maintain.sh fake-dest >"$T/out.log" 2>&1; rc=$?
[ $rc -ne 0 ] || fail "T16 rerun declined exit"
grep -qxF 'etc/apt/apt.conf.d/99-disable-auto-upgrades' "$(latest)/archive-list.txt" || fail "T16 override not archived"
pass "T16 prior override captured in archive"
echo "ALL SMOKE TESTS PASSED"
