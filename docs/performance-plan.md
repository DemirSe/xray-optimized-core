# Server performance plan: five targeted changes

## Status and scope

**Status: items 1–5 implemented. Items 1–5 are not deployed.**

This plan covers five changes in the existing Go server.
The reference revision is `c5747fe5`.
All source paths below are relative to the repository root.

The [locked constraints](constraints.md) remain the source of truth for supported protocols, clients, configuration, and operations.
Follow those constraints during implementation and verification.
This plan does not authorize a production deployment, client migration, commit, or push.

The following work is outside this plan:

- A rewrite in another language.
- Uplink splice, new transports, or different Vision commands.
- PGO, dependency updates, or a different Go release.
- Server, client, DNS, firewall, or kernel configuration changes.
- A new benchmark service, management framework, or public profiling endpoint.

## Evidence and expected effects

Source inspection found avoidable allocations and copies.
Those findings do not establish the main production bottleneck.
A lower allocation count does not prove higher tunnel throughput.

Existing microbenchmarks ran on a local Intel i5-1135G7 with Go 1.26.0 and `GOAMD64=v3`.
The benchmark used `CGO_ENABLED=0`, a 100 ms duration, and two repetitions.

| Existing benchmark | Bytes per operation | Allocations per operation | Observed time |
| --- | ---: | ---: | ---: |
| `BenchmarkNewBuffer` | 72 | 2 | 56–57 ns |
| `BenchmarkNewBufferStack` | 24 | 1 | 36–37 ns |

Compiler escape analysis identified `pool.Put(p)` in `Buffer.Release` as a heap allocation site.
These numbers describe local buffer operations, not VPS throughput.
The other four changes have no measured performance result yet.

| Item | Intended effect | Main risk |
| --- | --- | --- |
| 1. Typed buffer pool | Remove slice boxing during buffer recycling | Incorrect buffer eligibility or ownership |
| 2. TLS record inspection | Remove a payload allocation and copy | Different results for fragmented records |
| 3. Debug formatting | Avoid string construction when debug logging is off | Accidental changes to buffer reshaping |
| 4. Initial inbound buffer | Reuse the existing pool across connections | Premature recycling during asynchronous use |
| 5. Freedom retry waits | Respond promptly to cancellation and final failure | Different retry timing or error causes |

## Delivery sequence

1. Capture comparable local baseline measurements before editing the source.
2. Implement items 1, 2, and 3 as separate, bounded changes.
3. Implement item 4 only after the buffer ownership trace is complete.
4. Implement item 5 without changing the retry count or destination selection.
5. Run the detailed regression and performance checks after implementation.
6. Complete the approved stock-client tests before final acceptance.

Use one writer for the source tree.
Keep each change separately reversible.
Do not add repeated approval stages within an approved implementation scope.
Stop only for a real ownership, compatibility, access, or scope blocker.

During implementation, use only the focused checks needed to catch immediate mistakes.
Run the complete suite at the end.
Keep baseline binaries and measurements in temporary comparison storage.
Remove that storage after acceptance or rollback.
Do not add a historical archive to the repository.

## 1. Replace slice values in the buffer pool

### Baseline behavior

File: `fork/xray-core/common/buf/buffer.go`.

The pool stores `[]byte` values behind `any`.
`New` and `StackNew` retrieve those values.
`Release` returns eligible slices through `pool.Put(p)`.
The interface conversion causes the measured slice-header allocation.

The pool stores backing arrays, not `Buffer` objects.
That distinction prevents reuse of a released `Buffer` header through a stale pointer.
Preserve that distinction.

### Implementation

1. Keep the existing `Size` constant.
2. Change the pool item type to `*[Size]byte`.
3. If the pool needs an item, create a new array pointer.
4. Convert the array pointer to a slice in `New` and `StackNew`.
5. Keep the public `Buffer` methods and fields unchanged.
6. Preserve `FromBytes` as an unmanaged allocation.
7. Preserve `NewWithSize` allocation and capacity behavior.
8. In `Release`, save the backing slice before clearing the buffer header.
9. Return only managed backing storage with exactly `Size` capacity.
10. Reslice eligible storage to `Size` before converting it to an array pointer.
11. Preserve clearing of the active range and UDP metadata.
12. Preserve the current behavior of nil, unmanaged, and repeated releases.

Use the Go slice-to-array-pointer conversion.
Do not introduce `unsafe` or pool complete `Buffer` objects.
Audit every insertion into the pool before changing the assertion in its readers.

### Ownership and safety conditions

- A buffer view must not remain in use after `Release`.
- Unmanaged slices must never enter the pool.
- Different-sized storage must never enter the regular pool.
- `NewWithSize(Size)` can enter the pool if the existing eligibility rule permits it.
- A repeated release must not insert the same backing storage twice.
- A reused buffer must not expose bytes outside its active range.
- A reused buffer must not retain a previous UDP destination.
- Tests must not assume that `sync.Pool` returns a particular item.

Do not add whole-array clearing without a separate need and measurement.
Preserve the existing rules for initializing bytes before transmission.

### Planned tests and measurements

Extend `fork/xray-core/common/buf/buffer_test.go`.

- Test acquire, write, release, and subsequent acquire.
- Test nil release and repeated release.
- Test that unmanaged storage retains its existing release behavior.
- Test sizes below, equal to, and above `Size`.
- Test cleared ranges and absent UDP metadata.
- Test concurrent independent users of the pool.
- Test operation after garbage collection empties the pool.
- Repeat `BenchmarkNewBuffer` and `BenchmarkNewBufferStack`.

Compare warm-pool measurements without the race detector.
The race build can change pool reuse and allocation counts.

### Acceptance

- All existing buffer tests pass.
- Managed storage keeps the same lifetime rules.
- The warm-pool benchmarks remove the slice-boxing allocation.
- Target one fewer allocation per operation under the same compiler.
- Do not require a fixed nanosecond result across machines.
- Reject a reproducible throughput or memory regression in the combined tests.

## 2. Inspect TLS records without flattening the payload

### Baseline behavior

File: `fork/xray-core/proxy/proxy.go`, function `IsCompleteRecord`.

The function allocates a slice equal to the entire `MultiBuffer` length.
It copies the payload before inspecting TLS record headers.
`VisionWriter.WriteMultiBuffer` calls the function during the padding phase.
The copy occurs during padding, not on every byte of an established splice flow.

### Implementation

1. Replace the flattened slice with a cursor over existing buffer views.
2. Read each five-byte record header across buffer boundaries.
3. Check the existing application-data marker and record version bytes.
4. Decode the two-byte record length with the same byte order.
5. Skip the payload by advancing offsets, without copying its bytes.
6. Return false for incomplete headers, incomplete payloads, and invalid markers.
7. Return true only when the same complete-record condition holds.
8. Keep the empty-input behavior identical to the current function.
9. Preserve the current rejection of zero-length record cases.
10. Leave buffer contents, ranges, ownership, and order unchanged.

Use scalar cursor state or a small fixed stack array.
Do not allocate a payload slice or build a temporary list of record bytes.
Do not call `Release`, `Resize`, or `Advance` on the input buffers.
Do not change the caller's padding decisions.

Distinguish a nil `MultiBuffer` from a nil element inside a nonempty list.
Preserve the existing valid-input contract.
Do not silently turn malformed internal inputs into accepted TLS records.

### Planned tests and measurements

Add focused tests in `fork/xray-core/proxy/proxy_perf_test.go`.
The file can contain normal regression tests as well as benchmarks.

Keep the old parser as a test-only reference for differential checks.
Do not ship two production parsers.

Test the following cases:

- Empty and nil lists.
- A single complete record.
- Several complete records in one buffer.
- A header split at every possible byte boundary.
- A payload split across several buffers.
- Empty buffers between record fragments.
- A final incomplete header or payload.
- Invalid marker and version bytes.
- Zero-length records and large encoded record lengths.
- Buffers with nonzero active-range offsets.
- Byte-for-byte unchanged input after inspection.

Generate valid buffer partitions for differential fuzz tests.
Compare the new result with the reference result.
Bound generated input sizes so fuzz tests cannot exhaust memory.

Add `BenchmarkIsCompleteRecord` cases for contiguous and fragmented input.
Use small records and larger multi-record batches.
Prepare the input outside the timed section.

### Acceptance

- Differential tests show identical results for supported input layouts.
- The parser does not mutate or recycle input buffers.
- Warm benchmarks allocate no payload storage during inspection.
- Vision makes the same padding and direct-copy decisions for the same input.
- Compare decoded commands and payload, not fresh random padding bytes.
- Preserve production randomness without adding test hooks to the handshake.
- Fragmentation and truncation do not cause out-of-range reads or an infinite loop.

## 3. Defer debug string construction

### Baseline behavior

File: `fork/xray-core/proxy/proxy.go`, function `ReshapeMultiBuffer`.

The function builds `toPrint` while reshaping buffers.
`errors.LogDebug` checks the log level only after the caller prepares the string.
The existing level filter therefore does not remove that preparation cost.

The filter already exists in `fork/xray-core/common/log/level.go`.
Do not create another global level variable or logging framework.

### Implementation

1. Set the formatting decision with one call to `log.Passes(log.Severity_Debug)`.
2. Keep all buffer selection and reshaping outside the formatting condition.
3. If formatting is enabled, build the diagnostic text.
4. Preserve the current text for enabled debug logging.
5. If the function prepared diagnostic text, emit the debug event.
6. Keep the existing final severity check in `errors.LogDebug`.
7. Preserve the early return when no buffer needs reshaping.

A `strings.Builder` is optional for the enabled-debug branch.
Use it only if the resulting change stays small and preserves the text.
Do not change the split index, padding headroom, or TLS marker search.

The level check selects diagnostic work for that call.
A concurrent level change must not cause a data race.
A diagnostic event can disappear if the level changes during the call.
Data-path output must remain independent of the level.

### Planned tests and measurements

Extend `fork/xray-core/proxy/proxy_perf_test.go`.

- Compare returned bytes and buffer order under debug, info, warning, and disabled logging.
- Test inputs that need no reshape.
- Test inputs that need one or several additional buffers.
- Check the enabled-debug text against the previous text.
- Check that disabled-debug runs emit no debug event.
- Restore test log settings and handlers after each test.
- Do not run tests that alter global logging state in parallel.

Add `BenchmarkReshapeMultiBuffer` cases with debug off and on.
Count necessary buffer work separately from diagnostic formatting.
Do not assert zero total allocations when reshaping needs new buffers.

### Acceptance

- All log levels produce identical payload bytes and buffer ordering.
- Disabled-debug formatting performs no string construction for `toPrint`.
- Enabled-debug output keeps the existing message format.
- Logging changes do not alter Vision padding or direct-copy state.

## 4. Pool the initial VLESS inbound buffer

### Current behavior

File: `fork/xray-core/proxy/vless/inbound/inbound.go`, function `Handler.Process`.

The initial read uses `buf.FromBytes(make([]byte, buf.Size))`.
That backing array does not return to the regular managed pool.
The handler passes the buffer to `buf.BufferedReader` and request decoding.

`BufferedReader.Read` can recycle consumed buffers through `SplitBytes`.
`BufferedReader.ReadMultiBuffer` can transfer the remaining buffer to a caller.
`BufferedReader.Close` closes its reader without releasing its cached buffers.

The dispatcher can expose the reader to other routines.
`common/task.Run` can return on an error before every routine exits.
A blanket `defer first.Release()` is therefore not an adequate ownership proof.

### Implementation

1. Use the managed buffer API from item 1 for the initial read.
2. Preserve the existing read deadline and read operation.
3. Preserve the current handling of read errors and partial initial reads.
4. Release the initial buffer on failures before any ownership transfer.
5. Trace ownership through request decoding, fallback inspection, and dispatch.
6. Define the exact point at which the buffered reader receives ownership.
7. Before dispatch starts other routines, release only pending buffers still owned by the handler on failure.
8. After handoff, let the actual consumer release or transfer the buffer.
9. Do not recycle storage while a reader, writer, or asynchronous routine can still access it.
10. Preserve support for request headers followed by payload in the initial read.

Audit these files while tracing ownership:

- `fork/xray-core/common/buf/reader.go`.
- `fork/xray-core/common/buf/multi_buffer.go`.
- `fork/xray-core/proxy/vless/encoding/encoding.go`.
- `fork/xray-core/app/dispatcher/default.go`.
- `fork/xray-core/app/proxyman/outbound/handler.go`.
- `fork/xray-core/common/task/task.go`.

Do not redesign those components as part of this optimization.
Do not add a generic global buffer reaper.
If a post-handoff lifetime remains unclear, keep that path conservative and report the limitation.
If safe reuse requires a broader lifecycle change, defer item 4 rather than recycle memory prematurely.

Existing fallback code must not corrupt or lose the first bytes.
Testing that dormant branch does not authorize a production fallback configuration.

### Planned tests and measurements

Add `fork/xray-core/proxy/vless/inbound/inbound_buffer_test.go`.
Use synthetic connections and a minimal dispatcher stub.

- Test initial-read failure before handoff.
- Test a header split across several reads.
- Test invalid version, UUID, addons, and flow paths with synthetic credentials.
- Test a header and application payload in the same read.
- Test a successful consumer that reads the remaining initial payload later.
- Test cancellation while another routine still holds the reader.
- Test a downstream failure before the consumer reads the initial payload.
- Test retained fallback parsing without enabling fallback on production.
- Check that a released buffer cannot affect another connection's payload.

Do not assume that the pool returns the same backing array in every test.
Check explicit ownership, byte integrity, and released-buffer state instead.

Add a focused initial-read allocation benchmark.
Keep connection setup and synthetic input preparation outside the timed section where possible.

### Acceptance

- Every ownership transfer has a clear release responsibility.
- No initial bytes disappear, repeat, or change during dispatch.
- No post-handoff reader observes recycled storage.
- Successful repeated connections reuse the initial backing storage.
- The change removes the recurring initial-array allocation without adding another per-connection array.
- Cancellation and error tests pass under the race detector.

### Status

Item 4 is implemented in `fork/xray-core/proxy/vless/inbound/inbound.go`.
The handler takes the initial 8 KiB buffer from the managed pool.

Ownership rules:

- Before the handoff, the handler owns the reader cache. Each failure releases it.
- The handoff sites are `task.Run` in the fallback path, `r.NewMux`, and `dispatch.DispatchLink`. The handler marks the handoff before each call.
- After the handoff, the consumer owns the cache. It releases or transfers the cache.

Ownership caveat: a failed or canceled connection can abandon an unread cache.
The handler does not force that cache back into the pool.
The storage stays alive until all references drop, then the garbage collector reclaims it.

Tests and benchmarks: `fork/xray-core/proxy/vless/inbound/inbound_buffer_test.go`.

## 5. Make Freedom retry waits cancelable

### Current behavior

File: `fork/xray-core/proxy/freedom/freedom.go`, function `Handler.Process`.

The dial loop makes at most five attempts.
A failed attempt sleeps for `attempt * 100 ms`.
The loop also sleeps for 400 ms after the final failed attempt.
The sleeps do not observe context cancellation.

The current waits before subsequent attempts are 0, 100, 200, and 300 ms.
Keep those inter-attempt delays.
This change affects failure and cancellation latency, not successful streaming throughput.

### Implementation

1. Check `ctx.Err()` before each dial attempt.
2. Keep the maximum attempt count at five.
3. Stop immediately after a successful dial.
4. After the final failed attempt, return without creating another wait.
5. Preserve the zero-delay first retry.
6. For positive delays, wait on both a timer and `ctx.Done()`.
7. If cancellation wins, stop the timer.
8. Return an error whose cause exposes cancellation or deadline expiry.
9. Preserve the existing wrapper for an exhausted dial failure.
10. Preserve destination selection, DNS strategy, and proxy-protocol handling.

Use a small private helper only if tests need a deterministic seam.
Do not restore a general retry framework or add a dependency.
Do not change the separate inbound fallback retry loop in this item.

If the context reports cancellation, stop before another retry.
Handle the race between timer expiry and cancellation with a context check before dialing.
Do not run dial attempts in new detached goroutines.

### Planned tests and measurements

Add `fork/xray-core/proxy/freedom/retry_test.go`.
Use fake dial results and controllable synchronization.

- Test success on the first attempt.
- Test success on each later attempt.
- Test failure on all five attempts.
- Test an already canceled context and zero dial calls.
- Test cancellation during every positive wait.
- Test deadline expiry during a wait.
- Test cancellation immediately before the next attempt.
- Check preserved dial-error and context-error causes.
- Check that canceled waits leave no detached timer or retry routine.

Check the intended delay sequence without long sleep-based tests.
Use channels and generous timeout bounds for the real cancellation test.
Do not require exact millisecond timing on a loaded machine.

### Acceptance

- Success uses no extra retries or wait.
- Five failures use only the four inter-attempt waits.
- Final failure removes the existing unnecessary 400 ms sleep.
- Cancellation stops pending retries promptly.
- Error causes remain useful to the existing outbound error handling.
- No destination, transport, or DNS behavior changes.

### Status

Item 5 adds the cancelable retry waits in `fork/xray-core/proxy/freedom/freedom.go`.
The dial loop keeps five attempts. It uses the same waits before attempts 2–5: 0, 100, 200, and 300 ms.
The loop checks the context before each attempt. A canceled or expired context stops a positive wait and the loop.

The loop starts no wait after the fifth failure. An exhausted loop keeps the exact last dial error as the cause.
A context error takes priority when the context stops the loop.

Known limitation: a DNS lookup (`LookupForIP`) or a dialer that ignores the context does not stop early.
The retry wait cannot interrupt that work.

Tests and measurements: `fork/xray-core/proxy/freedom/retry_test.go`.
Both timing pairs below use the same fake-dialer fixture on the same host.

| Measurement | Before | After |
| --- | ---: | ---: |
| Five failed dials through `Process` | 1001 ms | 601 ms |
| Cancel to `Process` return, during a retry wait | 951.7 ms | 0.03 ms |

The two numbers describe local failure and cancellation latency.
They do not show a throughput or CPU improvement.

## Final verification

### Local regression checks

Run these checks after the five changes, or after the accepted subset if item 4 remains blocked.
Run commands from the repository root with a compatible Go compiler on `PATH`.
Use the same compiler and flags for before-and-after performance comparisons.

```bash
PI_TEST_PACKAGE_DIR=/path/to/installed/pi-package \
  CGO_ENABLED=0 GOAMD64=v3 GOTOOLCHAIN=local bash scripts/check.sh
```

Use the actual package directory for `PI_TEST_PACKAGE_DIR`.
The [shared check script](../scripts/check.sh) remains the complete offline repository check.
Do not weaken its vet baseline or configuration rejection checks.

Run focused race tests separately:

```bash
cd fork/xray-core
CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -timeout=120s \
  ./common/buf ./proxy ./proxy/vless/inbound ./proxy/freedom
```

The race command needs a supported native toolchain and C compiler.
The production static build still uses `CGO_ENABLED=0`.
Do not compare race-build allocation results with production-build results.

Run the planned focused benchmarks:

```bash
cd fork/xray-core
GOMAXPROCS=1 CGO_ENABLED=0 GOAMD64=v3 GOTOOLCHAIN=local go test \
  ./common/buf ./proxy ./proxy/vless/inbound ./proxy/freedom \
  -run '^$' \
  -bench 'Benchmark(NewBuffer|IsCompleteRecord|ReshapeMultiBuffer|InitialRead|FreedomRetry)' \
  -benchmem -benchtime=1s -count=10
```

Add the named benchmarks only where they measure a real changed path.
Prepare and reset benchmark inputs without hiding relevant allocations.
Compare repeated results with `benchstat` if it is available.
Otherwise report the raw repeated measurements and their limitations.
Do not change dependencies only to obtain a reporting tool.

Run parser differential fuzzing separately with a bounded duration.
Record the seed and retain any failing input as a minimal, non-sensitive regression fixture.

### Stock-client compatibility

Offline parser and buffer tests do not prove a complete REALITY handshake.
The earlier live test used stock sing-box 1.14.2.
That result does not prove compatibility with every official Xray version or Hiddify platform.

Use unmodified clients for the final interoperability matrix:

| Client | Required evidence |
| --- | --- |
| Official Xray client, pinned current version | Authentication, Vision, TCP, XUDP, connection teardown |
| Official Xray client, pinned earlier supported version | Same checks, with the exact tested version recorded |
| Stock sing-box | Authentication and previously verified TCP/UDP behavior |
| Stock Hiddify on each supported platform | Actual app version and results, or an explicit untested status |

Do not substitute a custom client when a stock client fails.
Keep the existing schema and protocol behavior.
Do not remove Mux or XUDP because the main transport is TCP.

Use only approved local or isolated test environments.
If a test needs an external destination or a production change, obtain the required approval first.
CI remains offline.
Do not replace the locked REALITY destination to make a live test pass.

Include these cases:

- IPv4 and IPv6 outbound traffic.
- DNS and other small UDP datagrams through the existing framing.
- TLS application traffic and non-TLS application traffic.
- Small writes, fragmented records, and large transfers.
- Abrupt disconnects, half-closes, cancellation, and concurrent connections.
- Shared-account concurrency at the capacity defined in the constraints.
- Several application connections per device.

Use synthetic credentials in fixtures.
Keep live credentials out of reports, command text, profiles, and test logs.

### Performance comparison and acceptance

Use the unchanged reference build and the candidate build on the same hardware.
Keep server configuration, compiler, client versions, traffic, and load conditions identical.
Use `GOMAXPROCS=1` for the primary comparison against the single-vCPU target.
Record warmup, pool state, and processor settings for every benchmark.
Regenerate the baseline with those settings instead of comparing against the earlier local timing numbers.
Run at least five repeated end-to-end samples where the environment permits them.

Record:

- Throughput and CPU use for uploads and downloads.
- Handshake latency and p50/p95 latency under connection churn.
- UDP response latency, loss, and byte integrity.
- Allocations, garbage-collection activity, and peak memory.
- Single-connection and concurrent-connection results.
- The exact client, server, compiler, and measurement versions.

Report local microbenchmarks separately from server measurements.
A shared VPS can add noise from other tenants.
Do not call a small unrepeatable difference an improvement.

Accept a change when correctness passes and its intended cost decreases.
Reject a reproducible regression in throughput, latency, memory, or cancellation behavior.
If only allocations improve, describe the result as an allocation improvement.
Do not claim a percentage tunnel speedup from a buffer benchmark.

## Rollback and completion

Reverse a failing source change without discarding unrelated work.
Retest the affected behavior after reversal.
Keep the existing production binary unchanged until deployment receives approval.
If deployment receives approval, retain its verified previous binary only for the bounded rollback window.
Remove temporary comparison and rollback files after final acceptance.

Completion requires evidence for every accepted item:

- [ ] Item 1 preserves buffer ownership and reduces slice-boxing allocations.
- [ ] Item 2 preserves parser results without copying the payload.
- [ ] Item 3 preserves reshaping while skipping disabled debug formatting.
- [ ] Item 4 passes ownership and cancellation checks, or has an explicit blocked status.
- [ ] Item 5 preserves the retry schedule and responds to cancellation.
- [ ] The final offline checks pass.
- [ ] Stock-client test results identify exact versions and remaining gaps.
- [ ] Performance claims match repeated measurements.
- [ ] No unauthorized source, configuration, production, or credential changes remain.
