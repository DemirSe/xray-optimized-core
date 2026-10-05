#!/usr/bin/env bash
# Repository check: Node tests, maintenance smoke tests, Go vet, Go tests,
# trim build, and config checks. Run from any directory.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Read-only module mode: the checks never change go.mod or go.sum.
export GOFLAGS="-mod=readonly"

echo "== Node tests"
node --test 'tests/*.test.mjs'

echo "== Maintenance smoke tests (offline, fake ssh)"
bash tests/maintain-smoke.sh

echo "== Go vet (bounded baseline gate, see scripts/vet.sh)"
bash "$ROOT/scripts/vet.sh"

echo "== Go vet gate regression"
bash "$ROOT/tests/vet-gate.test.sh"

echo "== Go tests (network tests are opt-in, see README.md)"
(cd "$ROOT/fork/xray-core" && go test -timeout=120s ./...)

echo "== Build and config checks"
bash "$ROOT/scripts/build.sh"

echo "== All checks passed"
