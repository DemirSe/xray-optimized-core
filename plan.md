# Three additional performance candidates

## Status and scope

Status: C1 and C3 are implemented on local sources and measured. C2 is deferred by owner decision. No candidate is deployed.
The analysis below is the original pre-implementation baseline.

Analysis baseline: `cd03620eae9e75b146782bfdc229a2abff86f8c8`.
This baseline includes the five earlier optimizations.
This plan does not repeat those implementations.

The deployed release matches this baseline.
Proposed gains in the analysis sections are hypotheses.
The local implementation section records measured results.
No production profile identifies these functions as the largest remaining costs.

The [locked constraints](docs/constraints.md) remain authoritative.
The client, wire format, REALITY handshake, and configuration schema must remain compatible.
This plan does not authorize publication or deployment.

The pending deletion of `docs/performance-plan.md` belongs to the earlier cleanup.
This new `plan.md` does not restore that document.

## Candidate selection

All source paths in this document start with `fork/xray-core/` unless a path states otherwise.
Line references describe the analysis baseline.

| ID | Candidate | Main opportunity | Risk | Proposed order |
| --- | --- | --- | --- | --- |
| C1 | Reuse the Vision output list | Remove one temporary pointer-list allocation from eligible reads | Low to medium | First |
| C2 | Add a complete-block unpadding fast path | Avoid a payload copy and an extra array acquisition | Medium to high | Third |
| C3 | Use value-based addresses for native UDP writes | Avoid per-packet address objects on an eligible socket | Medium | Second |

These candidates target different operations.
C1 does not change the unpadding parser.
C2 changes only a narrow unpadding case.
C3 changes only the native UDP write path.

The proposed order is C1, C3, then C2.
C2 is deferred by owner decision.
Only C1 and C3 were implemented.
C2 needs the most care because the parser transfers buffer ownership.

## Evidence boundaries

Source inspection confirms the current allocations and copies.
Source inspection does not establish their share of total CPU use.
The plans therefore start with a baseline measurement.

Eligible raw-copy or splice traffic can bypass the Vision work in C1 and C2.
Their benefit can concentrate on connection startup and buffered traffic.
C3 affects UDP packet writes, not bulk TCP downloads.

Earlier live checks show useful stability under the tested workloads.
Those checks do not prove ten-device capacity or every Hiddify platform.
The inherited `common/task.TestPeriodicTaskStop` counter race remains a separate test failure.

## Shared measurement procedure

1. Use clean baseline and candidate source snapshots.
2. Use the same Go version, architecture flags, and module cache.
3. Keep `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`, and `GOSUMDB=off`.
4. Use synthetic data instead of private client or server configurations.
5. Add only focused Go benchmarks to the existing packages.
6. Reset the reader, buffers, and protocol state for every benchmark operation.
7. Release every buffer after each operation.
8. Measure `ns/op`, `B/op`, and `allocs/op` with `-benchmem`.
9. Compare ten alternating baseline/candidate samples.
10. Use one pinned CPU where the host permits that setting.
11. Record the CPU, sample duration, `GOMAXPROCS`, `GOGC`, and `GOMEMLIMIT` settings.
12. Include both warm-pool and cold-pool measurements.
13. Run representative combined paths after each isolated benchmark.
14. Record raw samples and the exact fixture hash.

The existing `BenchmarkCopy` reuses a consumed reader.
That fixture cannot support a sustained-copy claim without a reset for each operation.

**CAUTION:** The owner checkout contains retained release binaries.
Run `scripts/check.sh` and `scripts/build.sh` only in a disposable source snapshot.
Those scripts replace `build/xray-trimmed` in their working directory.

If a new test file is untracked, copy that file into every snapshot explicitly.
Verify the copied file with `cmp` and SHA-256 before each gate.
A tracked-only snapshot cannot prove that the new tests ran.

No timing result alone proves a tunnel-throughput improvement.
An allocation improvement can justify a candidate without a measurable WAN speed change.

## C1: Reuse the Vision output list

### Current cost and call path

`proxy/proxy.go:204–283` implements `VisionReader.ReadMultiBuffer`.
The padding/filter branch allocates a new `MultiBuffer` at line 237.
The loop appends non-empty unpadding results to that new list.

The underlying reader already returns a list of buffer pointers.
The current implementation discards that list after it creates the replacement.
The new list stores pointers, not payload bytes.

`proxy/vless/inbound/inbound.go:645` installs the Vision reader for the server path.
`common/buf/reader.go:89–95` transfers a cached list to its consumer.
`common/buf/multi_buffer.go:57–63` defines list release behavior.

The current loop also discards empty outputs without an explicit release.
An explicit release can recycle that storage.
The discarded buffers do not prove a permanent leak.

### Proposed bounded design

1. Audit every reader that supplies a list to `VisionReader`.
2. Confirm that the consumer owns the returned slice for that read.
3. Keep the existing early returns and state selection.
4. Use a write index to compact the owned list in place.
5. Call the unchanged `XtlsUnpadding` exactly one time for each original entry.
6. Store each retained output at the next write index.
7. Release each empty output that the loop owns and discards.
8. Clear every unused slice slot after the loop.
9. Return the shortened slice.
10. Keep TLS filtering and the direct-copy transition unchanged.

Do not add a list pool.
Do not add a buffer-header pool.
Do not change `XtlsUnpadding` in this candidate.

If any reader retains the same mutable slice, stop the implementation.
A smaller allocation count cannot justify a slice ownership race.

### Ownership and compatibility requirements

- Retained entries remain in their original order.
- The loop never overwrites an entry before it reads that entry.
- Every output has exactly one owner.
- Empty output release never releases retained payload storage.
- Cleared slots do not retain released headers.
- Nil-list and nil-element behavior match the baseline.
- The method returns the same underlying read error.
- Data-plus-EOF behavior matches the baseline.
- Direction-specific counters and command state match the baseline.
- TLS input drains before the raw-reader switch.
- The splice flags change at the same points.

### Allowed implementation files

- `proxy/proxy.go`: the list assembly in `VisionReader.ReadMultiBuffer`.
- `proxy/proxy_perf_test.go`: focused reader tests and benchmarks.

A new small test file in `proxy/` is also acceptable.
No other production file is necessary unless the ownership audit proves otherwise.

### Test plan

1. Keep a test-only reference of the current reader behavior.
2. Compare both directions against that reference.
3. Test zero, one, two, eight, and sixteen input buffers.
4. Test leading, middle, and trailing empty outputs.
5. Test payload-only, padding-only, and partial-header inputs.
6. Test all three Vision commands.
7. Test consecutive blocks and multiple reads of one block.
8. Test UUID mismatch and malformed input behavior.
9. Test EOF and a custom read error with payload.
10. Hold a returned list while another reader reuses pool storage.
11. Verify that no later call changes the held payload.
12. Verify release state and cleared unused slots.
13. Verify the raw-reader transition and TLS buffer drain.

Use an ownership-checking reader for deterministic tests.
Do not depend on nondeterministic `sync.Pool` pointer reuse.

### Benchmarks and acceptance

Proposed benchmark: `BenchmarkVisionReaderListCompaction`.
The fixture must rebuild the active parser state for each operation.

Measure eligible padded/filter reads with one, four, and sixteen buffers.
Include unchanged direct-read and no-filter controls.

Accept C1 only if all of the following hold:

- Differential tests and focused race tests pass.
- Eligible reads remove the replacement-list allocation.
- Representative total allocations decrease by at least one per eligible read.
- Unchanged controls show no repeatable timing regression above 3 percent.
- The implementation adds no asynchronous sharing of a mutable slice.

The allocation target concerns eligible reads only.
It does not imply zero allocations for a complete Vision read.

### Rollback

Keep C1 in a separate commit.
If ownership or compatibility fails, restore the former list assembly.
Retain the differential fixtures and measurement evidence.

## C2: Add a complete-block unpadding fast path (deferred)

Owner decision: C2 is deferred. No C2 code, helper, or test was added.

### Current cost and call path

`proxy/proxy.go:587–667` implements `XtlsUnpadding`.
After the initial UUID check, line 611 gets a new managed buffer.
The content branch copies bytes into that buffer.
The function then releases the input buffer.

A complete block can contain one contiguous content range.
For that case, the parser can potentially return a view of the existing array.
The optimization must preserve the input header's consumed state.

C2 is not the earlier TLS record cursor optimization.
The record cursor inspects TLS records without flattening them.
C2 concerns Vision payload ownership after padding removal.

### Proposed bounded design

1. Check eligibility before the general parser consumes bytes.
2. Require a complete, single block with non-empty contiguous content.
   Require an initial UUID block or a complete command boundary.
   Require a known continue, end, or direct command.
3. Require normal managed storage with the standard buffer capacity.
4. Exclude unmanaged storage and ambiguous aliases.
5. Exclude incomplete headers, incomplete payloads, and incomplete padding.
6. Exclude multiple blocks in one input buffer.
7. Exclude unexpected trailing bytes after an end/direct command.
8. Parse lengths into stack variables without changing the input.
9. Check every range before a state or ownership change.
10. Transfer the array to a distinct output header.
11. Keep the full array backing store for its eventual pool release.
12. Set the output active range to the content bytes.
13. Invalidate the consumed input header without returning the array to the pool.
14. Commit the same command and remaining-length state as the reference parser.
15. Use the unchanged general parser for every excluded case.

A small helper in `common/buf` can perform the ownership transfer.
The helper must not use `unsafe` or change the `Buffer` field layout.
The helper must leave the input unchanged when eligibility fails.

Do not return the original live input header as the output.
That shortcut changes the observable release state of the consumed input.
Do not pool buffer headers.
Do not change cryptographic randomness or padding generation.

### State and buffer requirements

The reference parser defines state transitions for continue, end, and direct commands.
The fast path must produce the same transitions.
The fast path must also preserve the UUID check.

`Buffer.Release` returns the backing array to the shared pool.
A transferred array must not enter that pool while the output still owns the array.
Repeated release of the consumed input must remain safe.

The output view can have a nonzero starting offset.
That offset changes tail availability compared with a freshly copied buffer.
Audit downstream uses of `Available`, `IsFull`, `Resize`, and append operations.
If the view changes downstream behavior, exclude that case or reject C2.

The output must preserve these properties:

- Identical active bytes and length.
- Identical standard capacity.
- Identical UDP metadata behavior.
- Identical command and filter state.
- Identical debug messages when debug logging is enabled.
- Independent input and output header lifetimes.
- Safe output release after delayed consumption.

C2 can still allocate an output header.
The plan does not claim an allocation-free parser.

### Allowed implementation files

- `proxy/proxy.go`: the private eligibility check and fast path.
- `common/buf/buffer.go`: a narrow helper for the required ownership transfer.
- Corresponding buffer and proxy test files.

No generic reader, dispatcher, timer, or TLS handshake redesign belongs to C2.

### Test plan

1. Keep the original general parser as a test reference.
2. Compare output bytes and every relevant traffic-state field.
3. Cover both directions and every Vision command.
4. Split the UUID, command header, content, and padding at every boundary.
5. Cover zero content, zero padding, and maximum encoded lengths.
6. Cover multiple blocks and unexpected trailing bytes.
7. Cover UUID mismatch, short input, and invalid length combinations.
8. Cover advanced input ranges and released or unmanaged buffers.
9. Release the consumed input repeatedly while the output remains live.
10. Hold the output while other connections acquire and release managed buffers.
11. Verify that the held output never changes.
12. Release the output and verify one final array return.
13. Compare downstream readers and writers with the reference output.
14. Fuzz the parser with a bounded differential fixture.
15. Run combined C1/C2 reader tests under the race detector.

Do not normalize malformed behavior silently.
The legacy parser can reject, pass through, or panic on different malformed inputs.
The candidate must preserve the relevant existing contracts.

### Benchmarks and acceptance

Proposed benchmark: `BenchmarkXtlsUnpaddingCompleteBlock`.
Use content lengths of 64, 512, 1400, 4096, and near-capacity bytes.
Include fragmented and multi-block fallback controls.

Measure the full reader path as well as the isolated function.
Account for the output header and eventual release.
Do not measure an already-consumed input repeatedly.

Accept C2 only if all of the following hold:

- Differential tests, fuzz tests, and focused race tests pass.
- Eligible content avoids the extra payload copy.
- Eligible content avoids the extra array acquisition.
- Output ownership and input release semantics remain correct.
- Representative eligible cases improve median CPU time by at least 5 percent.
- Fallback and combined paths show no repeatable regression above 3 percent.

Reject C2 if the required ownership helper becomes a broad API redesign.
Reject C2 if its narrow eligibility makes the measured benefit negligible.

### Rollback

Keep C2 and its private helper in a separate commit.
Restore the general parser as the only parser path on rollback.
C1 must continue to work with that original parser.

## C3: Use value-based addresses for native UDP writes

### Current cost and call path

`proxy/freedom/freedom.go:422–500` implements `PacketWriter.WriteMultiBuffer`.
After address overrides and domain resolution, line 480 calls `RawNetAddr`.
The writer then calls the generic `PacketConn.WriteTo` method.

`common/net/destination.go:104–133` constructs a `*net.UDPAddr` for an IP destination.
`common/net/address.go:137–139` and `157–159` expose IP bytes from value-based addresses.
These conversions can create small objects on the packet path.

`transport/internet/system_dialer.go:72–83` wraps the packet socket.
`PacketConnWrapper` keeps a generic `PacketConn` at lines 151–154.
For a native UDP socket, Go offers `WriteToUDPAddrPort` with a value-based address.

### Proposed bounded design

1. Select the native socket capability in `NewPacketWriter`.
2. Require the underlying packet connection to be a concrete `*net.UDPConn`.
3. Keep the existing generic writer for wrapped and custom connections.
4. Apply the current address and port overrides first.
5. Keep the current domain resolution and cache behavior.
6. Require a resolved UDP destination with a supported IP address.
7. Convert the address to `netip.AddrPort` without string formatting.
8. Use a small conversion helper in `common/net` for the supported native addresses.
9. Use the existing fixed IPv4/IPv6 values inside that helper.
10. Avoid an intermediate `net.IP` slice on the eligible conversion path.
11. Call `WriteToUDPAddrPort` on the eligible socket.
12. Preserve byte counters and the existing release/error order.
13. Use the original `RawNetAddr` path for every unsupported case.

Do not change the `Address` interface.
Do not add a new DNS cache or change address selection.
Do not change socket creation, source binding, deadlines, or socket options.
Do not bypass packet masks or accounting wrappers.

The first implementation covers writes only.
A receive-path conversion needs a separate analysis.

### Compatibility requirements

- Domain resolution still precedes the native write.
- Destination overrides still take priority.
- Nil and unresolved destinations retain their current behavior.
- Non-UDP destinations use the original path.
- Unknown address implementations use the original path.
- IPv4-mapped IPv6 behavior matches the current address representation.
- Zone handling retains the current policy.
- Connected-socket errors retain the same error class and release behavior.
- A write failure releases the current and remaining buffers exactly one time.
- Statistics count the same successfully written bytes.
- The nil-UDP-metadata write path remains unchanged.

### Allowed implementation files

- `proxy/freedom/freedom.go`: native socket selection and eligible writes.
- `common/net/destination.go` or `address.go`: a narrow value-address helper.
- Relevant freedom and address test files.

No production change to `system_dialer.go` is necessary for the initial design.
The wrapper and socket construction remain unchanged.

### Test plan

1. Compare native and generic writes with loopback UDP listeners.
2. Cover IPv4 and IPv6.
3. Cover mapped addresses and unsupported custom address values.
4. Cover destination address and port overrides separately.
5. Cover cached and newly resolved domain destinations.
6. Verify that unresolved and forced-resolution failures retain their behavior.
7. Cover nil metadata and invalid network values.
8. Use fake packet connections to verify generic fallback selection.
9. Simulate write errors and short-write behavior in fallback fixtures.
10. Verify counters and the release state of remaining buffers.
11. Run concurrent writers on separate connections under the race detector.
12. Verify byte-exact datagrams through VLESS/XUDP after an approved deployment.

Use datagram lengths of 8, 128, 512, 1200, 1400, 2048, 4096, and 8192 bytes.
Larger datagrams need a separate baseline-supported limit check.

### Benchmarks and acceptance

Proposed benchmark: `BenchmarkFreedomPacketWriterAddress`.
Separate address conversion from loopback socket cost.
Keep the final socket write in the representative benchmark.

Measure explicit IPv4/IPv6 packet metadata and unchanged nil-metadata controls.
Measure native and wrapped connections separately.
Use the same destination distribution for both implementations.

Accept C3 only if all of the following hold:

- Functional, differential, and focused race tests pass.
- Native writes remove at least one address allocation per eligible packet.
- Representative packet cost does not regress.
- Wrapped and nil-metadata controls show no repeatable regression above 3 percent.
- DNS, override, error, counter, and release behavior remain equivalent.

Reject C3 if the conversion still allocates the same intermediate objects.
The extra branch must earn its complexity through measured savings.

### Rollback

Keep C3 in a separate commit.
Restore generic address conversion and writes on rollback.
C1 and C2 must remain independent of C3.

## Verification commands for a later implementation

The proposed new benchmarks and fuzz target do not exist yet at the analysis
baseline. `BenchmarkVisionReaderListCompaction` and
`BenchmarkFreedomPacketWriterAddress` exist now.
The C2 fuzz target does not.
Do not interpret these commands as checks that ran during this analysis.

Run these checks inside the prepared source snapshot:

```bash
cd fork/xray-core

go test -count=20 -timeout=120s \
  ./common/buf ./common/net ./proxy ./proxy/vless/... \
  ./proxy/freedom ./transport/internet ./app/dispatcher ./app/proxyman/outbound

CGO_ENABLED=1 CC=/usr/bin/gcc go test -race -count=10 -timeout=120s \
  ./common/buf ./common/net ./proxy ./proxy/vless/... \
  ./proxy/freedom ./transport/internet ./app/dispatcher ./app/proxyman/outbound

go test ./proxy -run='^$' \
  -fuzz='^FuzzXtlsUnpaddingDifferential$' \
  -fuzztime=30s -fuzzminimizetime=100x -parallel=2

go test ./proxy -run='^$' -benchmem -count=10 \
  -bench='BenchmarkVisionReaderListCompaction|BenchmarkXtlsUnpaddingCompleteBlock'

go test ./proxy/freedom -run='^$' -benchmem -count=10 \
  -bench='BenchmarkFreedomPacketWriterAddress'
```

The count limits apply to the changed-path packages.
Do not multiply unrelated sleeping tests into this focused gate.
Run the normal offline suite from the snapshot root without repeated test counts.

```bash
bash scripts/check.sh
```

The full race suite still requires an honest baseline comparison.
Do not suppress the inherited periodic-test race or label the full suite green.

## Release acceptance after separate approval

1. Complete the measurement gate for one candidate.
2. Complete independent code review and the relevant tests.
3. Inspect the actual diff and every new test file.
4. Publish only after owner approval.
5. Build a clean, identifiable candidate release.
6. Preserve the current binary as the verified rollback release.
7. Validate the existing private configuration.
   Do not print credentials.
8. Deploy only after owner approval.
9. Test fresh authenticated TCP and UDP connections.
10. Verify payload integrity and both outbound address families.
11. Exercise delayed readers, abrupt closes, and connection churn.
12. Compare CPU, memory, latency, and throughput under the same workload.
13. When the devices are available, verify the supported client platforms.
14. Monitor a finite soak before acceptance.
15. If a regression appears, restore the previous verified binary.

A passing local benchmark is not production acceptance.
A successful laptop test is not a complete client matrix.
No candidate changes the existing private-IP bypass rules on the laptop.
Those rules require a separate client-policy decision.

## Local implementation status of C1 and C3

This section records the local result of this task.
It separates local checks from production compatibility.
All commands below ran offline on the pinned Go 1.26.0 toolchain.
No private client configuration supplied test input.
Retained releases and private baselines remain unchanged.

### C1: implemented and measured

Changed files:

- `proxy/proxy.go`: `VisionReader.ReadMultiBuffer` compacts the handed-off list
  in place with a write index, releases owned discarded empty outputs, and
  clears every unused slot. `XtlsUnpadding`, parsing, and TLS classification
  are unchanged.
- `proxy/vision_reader_perf_test.go`: differential fixtures, ownership checks,
  and the required benchmark. The former reader body is kept as a reference.

Supplying-reader audit:

- The Vision reader wraps the inbound `*buf.BufferedReader`.
  `DecodeBodyAddons` returns `buf.NewReader(reader)`, which returns that
  `BufferedReader` because it already implements `buf.Reader`.
- `BufferedReader.ReadMultiBuffer` nils its cached list before the handoff.
- `ReadVReader` allocates a fresh slice per read and keeps no slice reference
  (the readv arrays copy buffer pointers).
- `SingleReader`, `PacketReader`, `cnc.Connection`, and `pipe` transfer the
  list and drop their own reference before returning.
- No supplier re-exposes the same mutable slice after the handoff, so in-place
  compaction adds no asynchronous sharing.

The differential matrix uses 28 fixtures in both directions, for 56 subtests.
The fixtures cover empty, partial, unmatched, nil, and multiple blocks.
They also cover all three commands and payload with EOF or a custom error.
Additional tests check `BufferedReader` handoff, held outputs, and cleared slots.
They also check release of empty inputs and the direct transition.

Benchmark `BenchmarkVisionReaderListCompaction` uses identical rebuilt fixtures
for both subcases and ran with `-benchmem -benchtime=100ms -count=10`:

- eligible 1-buffer reads: 15 to 14 allocs/op, 777 to 769 B/op,
  median 506.6 to 487.9 ns/op.
- eligible 4-buffer reads: 30 to 27 allocs/op, 17843 to 1410 B/op,
  median 3437.5 to 1061.0 ns/op.
- eligible 16-buffer reads: 90 to 81 allocs/op, 69702 to 3981 B/op,
  median 12794.5 to 3498.5 ns/op.
- direct-read and no-padding/no-filter controls keep identical allocation
  counts, B/op, and medians within noise.

The extra savings in the 4-buffer and 16-buffer cases are the reclaimed empty
outputs that the former loop dropped.
The 1-buffer case isolates the removed list allocation: exactly one allocation.
Fixture digests are logged by `TestVisionReaderListCompactionFixtureDigest`.

Local checks: `go test -count=20` and `go test -race -count=10` pass for the
five focused packages. Local checks are not production compatibility.
No live, ten-device, or client-platform validation ran.

The host ran `scripts/check.sh` in a disposable source clone.
The clone included the tracked diff and every new test file.
Copy checks used `cmp` and SHA-256.
Node tests, maintenance smoke, and the strict vet gate passed.
The full normal Go suite, trim build, and configuration checks also passed.

### C3: implemented and measured

Changed files:

- `common/net/destination.go`: `Destination.RawNetAddrPort` converts built-in
  IPv4/IPv6 value addresses to `netip.AddrPort` without an intermediate
  `net.IP` or string formatting. Unsupported implementations report false.
- `proxy/freedom/freedom.go`: `NewPacketWriter` selects the concrete
  `*net.UDPConn` once after the stats unwrap. `PacketWriter.writePacket` uses
  `WriteToUDPAddrPort` for supported value destinations and keeps
  `RawNetAddr`/`WriteTo` for everything else. Null metadata, overrides,
  resolution, counters, and release order are unchanged.
- `common/net/destination_value_test.go` and
  `proxy/freedom/packet_writer_perf_test.go`: conversion, socket, fallback,
  override, cache, error, counter, and benchmark checks.

Capability audit:

- The concrete `*internet.PacketConnWrapper` remains required.
- `c.PacketConn.(*net.UDPConn)` runs once and never unwraps a packet mask,
  a custom connection, or an accounting wrapper.
- UDP mask dialers replace `PacketConn` with a mask wrapper, so those keep the
  generic path.

Benchmark `BenchmarkFreedomPacketWriterAddress` keeps the pre-change `PacketWriter.WriteMultiBuffer` body from `cd03620e` as a test-only reference.
Both write loops use one parameterized runner with identical reset and release fixtures.
Both sides use the same concrete socket types.
Eligible writes and nil-metadata controls use real native sockets.
Custom fallback controls use the same `pwMaskPacketConn` wrapper over a real socket. The runner adds symmetric escape
allocations to both implementations.
Those fixture costs cancel in each baseline/candidate allocation difference.

The benchmark ran with `-benchmem -benchtime=100ms -count=10` on local
loopback sockets:

- address-only: 35.5 to 2.4 ns/op, 52 to 0 B/op, 2 to 0 allocs/op.
- loopback IPv4 write: 7 to 5 allocs/op, 184 to 132 B/op,
  median 3663.5 to 3600.0 ns/op.
- loopback IPv6 write: 7 to 5 allocs/op, 208 to 144 B/op,
  median 3873.0 to 3793.5 ns/op.
- nil-metadata control: identical 4 allocs/op and 108 B/op,
  median 3532.0 to 3575.5 ns/op with overlapping ranges.
- custom-connection fallback control: identical 7 allocs/op and 184 B/op,
  median 3746.0 to 3706.0 ns/op with overlapping ranges.

Each eligible native packet removes two allocations and 52 B on IPv4, or 64 B
on IPv6.
The nil-metadata median increases 1.2 percent in the primary run.
The second run also increases: 3596.0 to 3681.5 ns/op, or 2.4 percent.
Both increases remain below the 3 percent control threshold.
The sample ranges overlap, and allocation counts remain identical.
These samples do not establish a significant timing change.
No target tunnel-throughput claim follows from these numbers.

Local checks: `go test -count=20` and `go test -race -count=10` pass for the
five focused packages, including byte-exact loopback datagrams of 8 to 8192
bytes in both families.
The offline snapshot check also passes for this state.
Local checks are not production compatibility.
No VLESS/XUDP deployment check ran, because deployment needs separate owner
approval.

## Completion criteria

A candidate is complete only after its implementation, evidence, and review satisfy its acceptance gate.
A candidate can also finish as rejected when measurements do not justify the change.
The plan does not require speculative optimizations to enter the release.
