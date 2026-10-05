#!/usr/bin/env bash
# Build the trimmed server binary and run the config acceptance checks.
# Output goes to build/ (ignored by Git).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Read-only module mode: the build never changes go.mod or go.sum.
export GOFLAGS="-mod=readonly"

OUT_DIR="$ROOT/build"
BIN="$OUT_DIR/xray-trimmed"
mkdir -p "$OUT_DIR"

echo "== Build"
(cd "$ROOT/fork/xray-core" && go build -trimpath -ldflags="-s -w -buildid=" -o "$BIN" ./main)

echo "== CLI smoke"
"$BIN" version
"$BIN" uuid

echo "== Config check: VLESS TCP must be accepted"
cat > "$OUT_DIR/config-valid.json" <<'JSON'
{
  "inbounds": [
    {
      "port": 1443,
      "protocol": "vless",
      "settings": {
        "clients": [
          {
            "id": "11111111-1111-4111-8111-111111111111",
            "flow": "xtls-rprx-vision"
          }
        ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp"
      }
    }
  ],
  "outbounds": [
    {
      "protocol": "freedom"
    }
  ]
}
JSON
"$BIN" run -test -c "$OUT_DIR/config-valid.json"

echo "== Config check: VMess must be rejected"
cat > "$OUT_DIR/config-invalid.json" <<'JSON'
{
  "inbounds": [
    {
      "port": 1443,
      "protocol": "vmess",
      "settings": {}
    }
  ],
  "outbounds": [
    {
      "protocol": "freedom"
    }
  ]
}
JSON
if "$BIN" run -test -c "$OUT_DIR/config-invalid.json"; then
  echo "FAIL: the trimmed binary accepted a VMess config" >&2
  exit 1
fi
echo "OK: the trimmed binary rejected the VMess config"

echo "== SHA-256"
(cd "$OUT_DIR" && sha256sum xray-trimmed | tee sha256.txt)
