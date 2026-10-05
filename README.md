# xray-optimized

This repository holds the owner's trimmed server-only build of Xray-core and
the tools that support it.

## Layout

| Path | Content |
|---|---|
| `fork/xray-core/` | Trimmed Go fork of XTLS/Xray-core, pinned at v26.3.27. |
| `.pi/extensions/server/` | Pi extension for owner-approved server jobs. |
| `maintain.sh` | Manual maintenance script with an owner approval gate. |
| `tests/` | Offline Node tests and maintenance smoke tests. |
| `scripts/` | Root build and check commands. |
| `docs/constraints.md` | Locked owner constraints. |
| `.github/workflows/check.yml` | CI for tests, build, and config checks. |

## Development commands

Prerequisites:

- Go 1.26 or later.
- Node.js 24 or later.
- npm and the Pi test dependency for the Node wiring tests (see below).

Run all checks from the repository root:

```bash
bash scripts/check.sh
```

The script runs the Node tests, the maintenance smoke tests, Go vet, the Go
test suite, the trim build, and the config checks.

Run single steps from the repository root:

```bash
node --test 'tests/*.test.mjs'
bash tests/maintain-smoke.sh
bash scripts/vet.sh
bash scripts/build.sh
(cd fork/xray-core && go test -timeout=120s ./...)
```

`scripts/build.sh` writes the binary and the SHA-256 file to `build/`.
Git ignores `build/`.

## Go vet baseline

Raw `go vet ./...` still fails on three inherited findings. They come from the
imported fork and remain open.

The executable baseline record is [`scripts/vet.sh`](scripts/vet.sh). It lists
the permitted diagnostics and runs the full analyzer set. The gate fails on
any other diagnostic, tool error, or unexpected exit status.

## Test network access

The first Go command downloads Go modules. The download is a dependency
download. It is not a test network access.

The default test suite uses no external service. One test is an exception.
`TestECHDial` dials cloudflare.com and uses DNS. Run it only with network
access:

```bash
(cd fork/xray-core && XRAY_TEST_ECH=1 go test ./transport/internet/tls/)
```

## Pi extension test dependency

`tests/server-tools.test.mjs` loads the installed Pi package to check the tool
registration. The test installs nothing. It uses the global npm directory.

Set `PI_TEST_PACKAGE_DIR` to point at another package directory:

```bash
PI_TEST_PACKAGE_DIR=/path/to/pi-coding-agent node --test 'tests/*.test.mjs'
```

If the package is absent, the test fails with a clear message. CI installs
the pinned version `@earendil-works/pi-coding-agent@1.0.3`.

## Constraints

- Locked constraints: [`docs/constraints.md`](docs/constraints.md).
