#!/usr/bin/env bash
# Regression for scripts/vet.sh. Offline; the probe modules use the standard
# library only. Covers: a clean module passes, the exact baseline is permitted,
# a new or altered diagnostic fails and its text is shown, tool or unexpected
# exit failures fail with the captured output, and failed report reads,
# filters, or sorts fail closed.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/vet-gate.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

make_probe() { # make_probe <name> <source-file>
	local dir="$WORK/$1"
	mkdir -p "$dir/scripts" "$dir/fork/xray-core"
	cp "$ROOT/scripts/vet.sh" "$dir/scripts/vet.sh"
	cat > "$dir/fork/xray-core/go.mod" <<'EOF'
module example.com/vetprobe

go 1.26
EOF
	cp "$2" "$dir/fork/xray-core/main.go"
	echo "$dir"
}

RUN_STATUS=0
run_gate() { # run_gate <log-name> <command...>
	local name="$1"
	shift
	set +e
	"$@" >"$WORK/$name.out" 2>&1
	RUN_STATUS=$?
	set -e
}

check_pass() { # check_pass <log-name> <expected-text>
	if [ "$RUN_STATUS" -ne 0 ]; then
		echo "FAIL: $1 must pass the vet gate" >&2
		cat "$WORK/$1.out" >&2
		exit 1
	fi
	assert_output "$1" "$2"
	echo "ok: $1"
}

check_fail() { # check_fail <log-name> <expected-text>
	if [ "$RUN_STATUS" -eq 0 ]; then
		echo "FAIL: $1 must fail the vet gate" >&2
		cat "$WORK/$1.out" >&2
		exit 1
	fi
	assert_output "$1" "$2"
	echo "ok: $1"
}

assert_output() { # assert_output <log-name> <expected-text>
	grep -qF -- "$2" "$WORK/$1.out" || {
		echo "FAIL: $1 output must show: $2" >&2
		cat "$WORK/$1.out" >&2
		exit 1
	}
}

# --- Real go: a clean module passes, a new diagnostic fails visibly. ---

cat > "$WORK/clean.go" <<'EOF'
package main

func main() {}
EOF
CLEAN="$(make_probe clean "$WORK/clean.go")"
run_gate clean bash "$CLEAN/scripts/vet.sh"
check_pass clean 'Go vet: clean'

cat > "$WORK/dirty.go" <<'EOF'
package main

func main() {
	return
	println("unreachable")
}
EOF
DIRTY="$(make_probe dirty "$WORK/dirty.go")"
run_gate dirty bash "$DIRTY/scripts/vet.sh"
check_fail dirty 'unreachable code'
assert_output dirty 'unexpected diagnostics'
assert_output dirty 'main.go:'

# --- Fake go: the permitted baseline, tool failures, unexpected exits. ---

# The baseline lines come from scripts/vet.sh itself, not duplicated here.
BASELINE="$(sed -n '/^BASELINE="/,/^proxy\/vless\/inbound\/inbound.go:601:32: possible misuse of unsafe.Pointer"$/p' "$ROOT/scripts/vet.sh")"
BASELINE="${BASELINE#BASELINE=\"}"
BASELINE="${BASELINE%\"}"
if [ "$(printf '%s\n' "$BASELINE" | grep -c .)" -ne 3 ]; then
	echo "FAIL: cannot read the three baseline diagnostics from scripts/vet.sh" >&2
	exit 1
fi

FAKEBIN="$WORK/fakebin"
mkdir -p "$FAKEBIN"

# The fake go reads its scripted output through a bash redirection, not an
# external command, so a faked cat/grep/sort on PATH cannot alter its output.
write_fake_go() { # write_fake_go <exit-status> <output-file>
	{
		printf '#!/usr/bin/env bash\n'
		printf 'OUT=%q\n' "$2"
		printf 'printf "%%s" "$(<"$OUT")"\n'
		printf 'exit %s\n' "$1"
	} > "$FAKEBIN/go"
	chmod +x "$FAKEBIN/go"
}

printf '%s\n' "$BASELINE" > "$WORK/baseline.txt"
write_fake_go 1 "$WORK/baseline.txt"
run_gate baseline-pass env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_pass baseline-pass 'only the three inherited baseline findings'

{
	printf '%s\n' "$BASELINE"
	echo 'common/utils/browser.go:99:1: bogus new finding'
} > "$WORK/baseline-extra.txt"
write_fake_go 1 "$WORK/baseline-extra.txt"
run_gate baseline-extra env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail baseline-extra 'bogus new finding'

: > "$WORK/silent.txt"
write_fake_go 3 "$WORK/silent.txt"
run_gate silent env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail silent 'unexpected diagnostics or exit status 3'
assert_output silent 'produced no output'

cat > "$FAKEBIN/go" <<'EOF'
#!/usr/bin/env bash
echo "fake go: simulated tool failure" >&2
exit 127
EOF
chmod +x "$FAKEBIN/go"
run_gate tool-failure env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail tool-failure 'simulated tool failure'
assert_output tool-failure 'unexpected diagnostics or exit status 127'

printf './main.go:1:1: fake diagnostic from a tool that exited 0\n' > "$WORK/exit0.txt"
write_fake_go 0 "$WORK/exit0.txt"
run_gate exit0-with-diagnostic env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail exit0-with-diagnostic 'fake diagnostic from a tool that exited 0'

# --- Fake report tools: read, filter, and sort failures all fail closed. ---

fake_tool() { # fake_tool <name> <script body>
	printf '#!/usr/bin/env bash\n%s\n' "$2" > "$FAKEBIN/$1"
	chmod +x "$FAKEBIN/$1"
}

# A failed report read must fail even when the report holds the pristine baseline.
fake_tool cat 'echo "fake cat: simulated report read failure" >&2
exit 5'
write_fake_go 1 "$WORK/baseline.txt"
run_gate cat-failure env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail cat-failure 'tool-processing failure: cat'
rm -f "$FAKEBIN/cat"

# A failed filter must fail closed with baseline text on stdout and with clean output.
fake_tool grep 'for last in "$@"; do :; done
printf "%s" "$(<"$last")"
exit 7'
write_fake_go 1 "$WORK/baseline.txt"
run_gate grep-error-baseline env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail grep-error-baseline 'tool-processing failure: grep'
write_fake_go 0 "$WORK/silent.txt"
run_gate grep-error-clean env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail grep-error-clean 'tool-processing failure: grep'
rm -f "$FAKEBIN/grep"

# A failed sort must fail closed: with the baseline, and with an extra diagnostic.
fake_tool sort 'echo "fake sort: simulated sort failure" >&2
exit 42'
write_fake_go 1 "$WORK/baseline.txt"
run_gate sort-error-baseline env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail sort-error-baseline 'tool-processing failure: sort'
write_fake_go 0 "$WORK/silent.txt"
run_gate sort-error-clean env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail sort-error-clean 'tool-processing failure: sort'
write_fake_go 1 "$WORK/baseline-extra.txt"
run_gate sort-error-new-diagnostic env PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail sort-error-new-diagnostic 'tool-processing failure: sort'
rm -f "$FAKEBIN/sort"

run_gate no-tmpdir env TMPDIR="$WORK/no-such-dir" PATH="$FAKEBIN:$PATH" bash "$CLEAN/scripts/vet.sh"
check_fail no-tmpdir 'cannot create a temporary report file'

echo "vet gate regression passed (clean passes, baseline permitted, diagnostics and tool failures fail)"
