# N1 rejection, N2 and N3 local results

## Status and scope

Status: N1 is rejected by the owner before any implementation.
N2 and N3 are implemented and measured on local sources.
At local acceptance, neither N2 nor N3 was published or deployed.

Source baseline: `1dfcee33f592d9cf055dd603a2e071a920b03ad7`.
This commit includes the completed Vision list and native UDP write optimizations.
The earlier five optimizations also remain in the source.

The earlier C2 design for zero-copy Vision unpadding remains deferred.
Neither implemented candidate transfers a Vision payload to a different buffer owner.

The [locked constraints](docs/constraints.md) remain authoritative.
No candidate changes the client, wire format, handshake, or configuration schema.
Publication and deployment require separate approval.

The existing deletion of `docs/performance-plan.md` belongs to the earlier cleanup.
This task does not change or publish that deletion.
Protected baselines, retained releases, and external evidence remain untouched.

All numbers below are local measurements on one machine.
They are not WAN speed and not live acceptance.

## Candidate status

All source paths below start with `fork/xray-core/` unless a path states otherwise.

| ID | Candidate | Status | Local measured result |
| --- | --- | --- | --- |
| N1 | Avoid clearing the whole UDP receive buffer | Rejected by owner before implementation | No source change. The clear and both `Resize` calls remain |
| N2 | Use value-based UDP source addresses | Implemented and measured | 2 allocations removed per native receive. Loopback medians decrease by 0.3–3.3 percent |
| N3 | Encode the common VLESS response directly | Implemented and measured | Header-only median 18.72 → 3.12 ns controlled and 116.70 → 83.12 ns through the buffered writer |

N2 and N3 are independent.
N1 has no implementation.
There is no combined N1/N2 path, and no combined-path procedure remains.

## N1: rejected by owner

The owner rejected N1 before implementation.
N1 proposed avoiding the preliminary 8192-byte clear of the receive buffer.

The proposal carried a dirty-tail risk.
The inactive buffer tail can retain bytes from an earlier pool use.
Safe use requires every output consumer to respect the active range.
The proposal also needed a new packet-read helper in `common/buf`.
The owner did not state a rejection reason.
No N1 implementation or benchmark followed the rejection.

No N1 code exists in the source.
`PacketReader.ReadMultiBuffer` keeps `b.Resize(0, buf.Size)` before the receive and `b.Resize(0, n)` after the receive.
The clearing behavior of `common/buf` is unchanged.
The former N1 design, its helper proposals, and the four-variant combined matrix are removed from this plan.

## N2: value-based UDP source addresses

### Implemented change

- `proxy/freedom/freedom.go`: `NewPacketReader` selects a direct concrete `*net.UDPConn` after the existing statistics unwrap.
  The selection is a direct type assertion on `c.PacketConn`.
  Packet masks, custom wrappers, and nested wrappers are never unwrapped, so they keep the generic path.
- `proxy/freedom/freedom.go`: `PacketReader` gains one unexported `nativeUDPConn` field.
  `ReadMultiBuffer` uses `ReadFromUDPAddrPort` only when that field is set.
  The generic `ReadFrom` path, its `*net.UDPAddr` assertion, and its override guard are unchanged.
- `common/net/address.go`: `IPAddressFromAddr(netip.Addr) Address` converts the value directly.
  It builds the existing built-in IPv4 and IPv6 values without an intermediate `net.IP` or string.
  A mapped IPv6 value is normalized with `Unmap`, like `IPAddress`.
  An IPv6 zone is ignored like the baseline `net.IP` conversion.
  An invalid address returns nil.

Preserved behavior, with a differential test for each item:

- Both `Resize` calls and the full preliminary clear stay in place.
- Successful reads return one owned pool buffer. Tests check payload length, capacity, and release behavior.
- Source address and port, mapped IPv6 normalization, and native IPv6 stay equal to the HEAD conversion.
- `InitChangedAddr` restores `InitUnchangedAddr`. An unrelated source keeps its real value.
- Every override combination sets `IsOverridden` and suppresses source metadata on both paths.
- Read counters add the same successful byte count. Errors add nothing.
- Empty datagrams keep one empty buffer and full source metadata.
- Deadlines and closed sockets keep the same timeout and `net.ErrClosed` classes.
- Datagram integrity holds through 8192 bytes. Oversize datagrams truncate identically to the baseline.
- Custom and masked wrappers keep the generic path and its allocation counts.
- The existing native UDP write optimization is untouched.

### Files

- Production: `proxy/freedom/freedom.go`, `common/net/address.go`.
- Tests: `proxy/freedom/packet_reader_perf_test.go`, `common/net/address_value_receive_test.go`.
- `common/buf` is unchanged because N1 is rejected.

### Tests

`packet_reader_perf_test.go` keeps the exact HEAD `PacketReader.ReadMultiBuffer` body as `prLegacyReadMultiBuffer` and the exact HEAD `NewPacketReader` body as `prLegacyNewPacketReader`.
A test-only `prLegacyPacketReader` preserves the exact HEAD field layout without the native socket field.
The four reference bodies match HEAD after documented substitutions for test types and import aliases.
The fourth reference is the N3 response encoder described below.

The file covers:

1. Native socket selection, mask rejection, stats-wrapper unwrap, and the non-wrapper fallback.
2. Native versus legacy receives on equivalent real UDP sockets for `udp4` and `udp6` through 8192 bytes.
3. A dual-stack reader that receives a mapped IPv4 source, checked against the HEAD conversion.
4. Address-only, port-only, and combined overrides.
5. Original-domain restoration and an unrelated source.
6. Explicit counter and ownership assertions, including one held output across a later receive.
7. Empty datagrams, read deadlines, closed sockets, and repeated releases.
8. Oversized datagram truncation matched against the baseline.
9. Concurrent native readers on separate sockets under the race detector.
10. The constructor baseline: reflected size, allocation count, and one real read through the HEAD reader type.

`address_value_receive_test.go` compares the value conversion with `IPAddress`.
Cases include IPv4, IPv6, mapped, zoned, loopback, and unspecified values.
The tests cover invalid input.
The tests also check allocation counts against the slice conversion.

### Measurements

Method: prepared snapshot gate with 10 paired rounds, `-benchtime=100ms`, `GOMAXPROCS=1`, pinned CPU 2, Go 1.26.0, `GOAMD64=v3`, `CGO_ENABLED=0`.
Each subcase builds its own sender and reader sockets.
The legacy and candidate subcases use the same concrete socket types and the same payload.
Every returned buffer is released.

| Subcase (median of 10 rounds) | HEAD ns/op | Current ns/op | HEAD B/op | Current B/op | HEAD allocs | Current allocs |
| --- | --- | --- | --- | --- | --- | --- |
| loopback udp4 64 B | 2630.50 | 2544.50 | 136 | 84 | 6 | 4 |
| loopback udp4 1400 B | 2712.50 | 2656.50 | 136 | 84 | 6 | 4 |
| loopback udp4 8192 B | 3322.00 | 3273.00 | 136 | 84 | 6 | 4 |
| loopback udp6 64 B | 2803.50 | 2735.50 | 160 | 96 | 6 | 4 |
| loopback udp6 1400 B | 2876.00 | 2820.00 | 160 | 96 | 6 | 4 |
| loopback udp6 8192 B | 3510.00 | 3499.50 | 160 | 96 | 6 | 4 |
| control: masked wrapper fallback | 2729.00 | 2715.00 | 136 | 136 | 6 | 6 |
| control: override | 2636.00 | 2556.50 | 108 | 56 | 4 | 2 |
| address only: IPv4 conversion | 11.97 | 13.37 | 4 | 4 | 1 | 1 |
| address only: mapped conversion | 15.33 | 13.80 | 4 | 4 | 1 | 1 |
| constructor: one reader | 56.89 | 64.87 | 68 | 84 | 2 | 2 |

Reading of the results:

- Native receives remove two allocations per packet: the temporary `*net.UDPAddr` and its IP slice.
  That removes 52 B/op on `udp4` and 64 B/op on `udp6`.
- Native loopback medians decrease by 0.3 to 3.3 percent across sizes and families in this run.
  The absolute saving is small because the loopback syscall pair dominates.
  Small timing differences remain sensitive to measurement noise.
- The wrapped fallback control keeps identical bytes and allocations, and its time is within 0.5 percent.
- The override control removes the same two temporary objects while keeping metadata suppression.
- The isolated IPv4 conversion is slower by about 1.4 ns.
  It stays at 4 B/op and 1 allocation, and the whole receive is still faster.
  The isolated mapped conversion is faster by about 1.5 ns.
- Constructor overhead is separate and one-time per response reader:
  the reflected reader size grows from 64 to 72 bytes, one pointer field.
  The constructor allocation moves from the 64-byte class to the 80-byte class.
  Both constructors allocate the same two objects: the reader and the same address box.
  The median constructor time grows by about 8 ns.

N2 acceptance:

- At least one allocation removed per native packet: yes, two.
- No repeatable slowdown above 3 percent on the receive paths and controls: yes.
  The only value above 3 percent is the isolated IPv4 conversion benchmark, not a receive path.
- Wrapped controls keep their allocation counts and behavior: yes.
- The added constructor field and the one-time constructor cost are reported above, not hidden.

## N3: direct common VLESS response header

### Implemented change

- `proxy/vless/encoding/encoding.go`: `EncodeResponseHeader` gains a fast path for `request.Version == Version`, a non-nil `responseAddons`, and `responseAddons.Flow != vless.XRV`.
  That is the same no-addon branch that the baseline encoder selects.
- The fast path writes a shared, read-only two-byte sequence: the version byte and a zero addon length.
  It performs one `writer.Write` call and keeps the exact `"failed to write response header"` wrapper and its inner error.
- Every other case keeps the original pooled encoder unchanged.

Writer-contract audit:

- The only production caller is `proxy/vless/inbound/inbound.go:649`.
  It passes a `*buf.BufferedWriter`, which copies the supplied bytes into its own managed buffer.
  It does not modify or retain the supplied slice.
- The shared sequence must stay read-only. The implementation comment records that rule.

Preserved behavior, with a differential test for each item:

- Version zero with empty or unknown addons stays exactly two zero bytes.
- Nonzero versions and Vision addons keep the pooled encoder.
- Nil request and nil addons keep their current panic behavior and panic text.
- Writer failures keep the same error cause and wrapper text.
- A short write with no error keeps the baseline nil result.
- One writer call, unchanged header and body order, and unchanged flush boundaries.
- Concurrent encodes do not modify shared bytes. Output stays two zero bytes after heavy pool use.

### Files

- Production: `proxy/vless/encoding/encoding.go`.
- Tests: `proxy/vless/encoding/response_header_perf_test.go`.
- No inbound change is necessary.

### Tests

`response_header_perf_test.go` keeps the exact HEAD `EncodeResponseHeader` body as `encLegacyEncodeResponseHeader`, verified byte-identical to HEAD after import-alias normalization only.

The file covers:

1. Versions 0, 1, and 255 with empty, unknown, and Vision addons, compared byte-for-byte with the baseline.
2. Nil request and nil addons on both paths, including panic text.
3. Writer errors with zero and one reported byte, compared for wrapper text and cause.
4. Short writes with a nil error on both paths.
5. The actual `buf.BufferedWriter` fixture: header buffered, body written, one flush, header-then-body bytes.
6. Heavy pool use and concurrent encodes on the shared bytes.

### Measurements

Method: same snapshot gate and settings as N2.

| Subcase (median of 10 rounds) | HEAD ns/op | Current ns/op | HEAD B/op | Current B/op | HEAD allocs | Current allocs |
| --- | --- | --- | --- | --- | --- | --- |
| controlled writer | 18.72 | 3.12 | 0 | 0 | 0 | 0 |
| actual `buf.BufferedWriter` | 116.70 | 83.12 | 48 | 48 | 1 | 1 |
| fallback: Vision addons | 121.35 | 122.20 | 24 | 24 | 1 | 1 |
| fallback: version 1 | 18.80 | 18.69 | 0 | 0 | 0 | 0 |

Reading of the results:

- The common header path drops the pooled-array acquisition: 18.72 to 3.12 ns controlled, an 83 percent improvement.
- Through the real buffered writer the header-only median improves 116.70 to 83.12 ns, about 29 percent.
  The remaining time is the writer's own buffer acquisition and flush path, which N3 does not remove.
- Warm allocation counts do not increase: zero for the controlled writer, one for the buffered writer on both sides.
- Both fallback subcases stay within one percent and keep their bytes and allocations.

N3 acceptance:

- No temporary managed-array acquisition on the common path: yes.
- Warm allocation counts do not increase: yes.
- Median header-only CPU improvement of at least 5 percent: yes, both fixtures.
- Fallback within 3 percent: yes.
- Shared-byte safety follows the audited writer contract: yes.

## Measurement method and evidence

Environment: Go 1.26.0, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOSUMDB=off`, `GOAMD64=v3`.
Functional tests run with `CGO_ENABLED=0`.
Race tests run with `CGO_ENABLED=1` and `CC=/usr/bin/gcc`.
Benchmarks run with `GOMAXPROCS=1` and `taskset -c 2`.

The prepared snapshot gate clones HEAD into disposable scratch.
The gate copies the seven changed files and checks their hashes.
The gate also checks that no file is staged.
SHA-bound logs remain outside the repository.
The measurement tables use saved benchmark run `1791410040657231851`.
Its modes are `normal`, `race`, `offline`, `bench`, and `scope`.
The `bench` mode runs ten paired rounds with `-benchmem -benchtime=100ms`.

Never run `scripts/check.sh` or `scripts/build.sh` in the owner checkout.
Those scripts overwrite `build/xray-trimmed`.
The `offline` mode runs them only inside the disposable snapshot.

The earlier local loop also ran `go test` and `go test -race` for `./common/buf`, `./common/net`, `./proxy`, `./proxy/freedom`, `./proxy/vless/encoding`, and `./proxy/vless/inbound`.

The inherited `common/task.TestPeriodicTaskStop` race remains unresolved.
The focused race suites pass.
Do not claim full-repository race success.

## Known limits and verification boundaries

- The measurements are local loopback numbers on one machine.
  They are not WAN speed and not live acceptance.
- The saving per packet is small in absolute terms because the socket syscall dominates.
  The same syscall leads the benchmark on both sides.
- N2 does not make packet reads allocation-free.
  The output `Destination` and the `Address` value box still allocate on both paths.
  N2 removes only the temporary UDP source objects.
- The isolated IPv4 conversion is slower than the baseline by about 1.4 ns.
  The complete native receive is still faster on every measured size and family.
- N2 adds one pointer field to `PacketReader` and about 8 ns plus 16 B/op to the one-time constructor per response reader.
  This is reported separately and is not part of the per-packet result.
- The native read path applies only when the dialer returns a `PacketConnWrapper` whose packet connection is a direct `*net.UDPConn`.
  Packet masks and custom wrappers keep the generic path.
- IPv6 zone identifiers from a real socket are not exercised on loopback.
  The conversion ignores the zone like the baseline `net.IP` conversion, and the synthetic zone cases are covered.
- Oversized datagrams truncate at 8192 bytes identically to the baseline on this platform.
- N3 removes only the temporary array acquisition of the response header.
  The buffered writer still owns its own buffer and flush path.
- No client, handshake, schema, protobuf, or randomness behavior changes.

## Final acceptance and release boundary

A candidate can finish as rejected if measurements do not justify its complexity.
The owner rejected N1 without a benchmark.
N2 and N3 passed their local gates.

Require independent review and current-state tests before accepting an implementation.
Inspect the complete diff and all new test files.
Keep candidate changes separate enough for targeted rollback.

N2 and N3 are independent for rollback.
N2 rollback restores the generic source read and conversion.
N3 rollback restores the pooled encoder.
N3 does not depend on any UDP change.

Publish or deploy only after separate owner approval.
For an approved deployment, preserve the current verified release and private configuration.
Test fresh authenticated TCP and UDP connections, byte integrity, and both outbound address families.
Check source metadata and packet boundaries after approved UDP deployment tests.

Local tests do not establish ten-device capacity or every supported client platform.
None of these candidates changes the existing client bypass policy.
Do not present reduced local work as proven Internet throughput improvement.
