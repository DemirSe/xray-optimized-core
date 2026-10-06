#!/usr/bin/env bash
# Bounded go vet gate for the fork module.
# Runs the full default analyzer set. No analyzer is disabled.
# The gate permits only two outcomes:
#   - go vet exits 0 and prints no diagnostic;
#   - go vet exits 1 and prints exactly the baseline diagnostics below.
# The baseline is the diagnostic set at pristine HEAD 22566562. It is
# inherited technical debt, not resolved. Any other diagnostic, any setup,
# tool, or execution failure, and any unexpected exit status fails the gate.
# A failed report read, filter, or sort is a tool-processing failure and
# fails the gate as well.
set -uo pipefail

fail_setup() { # fail_setup <reason>
	echo "== Go vet: FAIL (gate setup error: $1)" >&2
	exit 2
}

fail_tool() { # fail_tool <message>
	echo "== Go vet: FAIL (tool-processing failure: $1)" >&2
	exit 1
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)" || fail_setup "cannot resolve the repository root"
cd "$ROOT" || fail_setup "cannot enter the repository root"

export GOFLAGS="-mod=readonly"

command -v go >/dev/null 2>&1 || fail_setup "the go tool is not in PATH"

# Proven with: git archive HEAD 22566562, then go vet ./... in fork/xray-core.
# The findings keep their original text at the current source locations.
BASELINE="common/utils/browser.go:52:2: unreachable code
proxy/vless/inbound/inbound.go:615:29: possible misuse of unsafe.Pointer
proxy/vless/inbound/inbound.go:616:32: possible misuse of unsafe.Pointer"

REPORT="$(mktemp "${TMPDIR:-/tmp}/vet-report.XXXXXX")" || fail_setup "cannot create a temporary report file"
trap 'rm -f "$REPORT"' EXIT

( cd "$ROOT/fork/xray-core" && go vet ./... ) >"$REPORT" 2>&1
STATUS=$?

# Report processing is fail-closed: a failed read, filter, or sort is a
# tool-processing failure, never a clean result.
CAPTURE="$(cat "$REPORT")"
CAT_STATUS=$?
if [ "$CAT_STATUS" -ne 0 ]; then
	fail_tool "cat failed to read the report (exit status $CAT_STATUS)"
fi

# Diagnostics only: package header lines ("# pkg") are not diagnostics.
# grep exit status 1 means "no diagnostic lines"; any other nonzero status is
# a tool-processing failure.
DIAGNOSTICS="$(grep -v '^#' "$REPORT")"
GREP_STATUS=$?
if [ "$GREP_STATUS" -gt 1 ]; then
	fail_tool "grep failed to filter the report (exit status $GREP_STATUS)"
fi

# Both sorted values are computed and checked before the comparison. pipefail
# makes each assignment fail when printf, sort, or the pipe fails.
sort_lines() { printf '%s\n' "$1" | LC_ALL=C sort; }
SORTED_DIAGNOSTICS="$(sort_lines "$DIAGNOSTICS")" || fail_tool "sort failed to order the diagnostics"
SORTED_BASELINE="$(sort_lines "$BASELINE")" || fail_tool "sort failed to order the baseline"

if [ "$STATUS" -eq 0 ] && [ -z "$DIAGNOSTICS" ]; then
	echo "== Go vet: clean"
	exit 0
fi

if [ "$STATUS" -eq 1 ] && [ "$SORTED_DIAGNOSTICS" = "$SORTED_BASELINE" ]; then
	echo "== Go vet: only the three inherited baseline findings (not resolved)"
	printf '%s\n' "$DIAGNOSTICS"
	exit 0
fi

{
	echo "== Go vet: FAIL (unexpected diagnostics or exit status $STATUS)"
	if [ -n "$DIAGNOSTICS" ]; then
		printf '%s\n' "$DIAGNOSTICS"
	elif [ -n "$CAPTURE" ]; then
		printf '%s\n' "$CAPTURE"
	else
		echo "(the go vet command produced no output)"
	fi
} >&2
exit 1
