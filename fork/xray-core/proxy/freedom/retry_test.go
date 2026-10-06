package freedom

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xtls/xray-core/common/buf"
	cerrors "github.com/xtls/xray-core/common/errors"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

// This file covers item 5: the Freedom dial loop keeps five attempts.
// It stops on cancellation and removes the wait after the final failure.

// The fixture block between the markers also compiles on the pre-change
// revision. The measurement copies it into a scratch test file in a clone of
// that revision. Keep the block free of retryDial, waitForRetry, and
// retryDialAttempts.
//
// BEGIN-BASELINE-FIXTURE

// errRetryFakeDial is the default fake dial failure.
var errRetryFakeDial = errors.New("fake dial failure")

// retryTestDialer is a fake internet.Dialer. It counts the dial calls.
type retryTestDialer struct {
	mu          sync.Mutex
	calls       int
	lastDest    xnet.Destination
	lastGateway *session.Outbound
	dial        func(call int, dest xnet.Destination) (net.Conn, error)
}

func (d *retryTestDialer) Dial(_ context.Context, dest xnet.Destination) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.lastDest = dest
	dial := d.dial
	d.mu.Unlock()
	if dial == nil {
		return nil, errRetryFakeDial
	}
	return dial(call, dest)
}

func (d *retryTestDialer) DestIpAddress() xnet.IP { return nil }

func (d *retryTestDialer) SetOutboundGateway(_ context.Context, ob *session.Outbound) {
	d.mu.Lock()
	d.lastGateway = ob
	d.mu.Unlock()
}

func (d *retryTestDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *retryTestDialer) outbound() *session.Outbound {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastGateway
}

func (d *retryTestDialer) destination() xnet.Destination {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastDest
}

var _ internet.Dialer = (*retryTestDialer)(nil)

// retryTestConn is an in-memory net.Conn. A read returns the scripted payload,
// then io.EOF. A write goes to a buffer.
type retryTestConn struct {
	mu         sync.Mutex
	written    bytes.Buffer
	writeErr   error
	payload    []byte
	closed     bool
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newRetryTestConn() *retryTestConn {
	return &retryTestConn{
		localAddr:  &net.TCPAddr{IP: net.ParseIP("192.0.2.5"), Port: 23456},
		remoteAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 443},
	}
}

func (c *retryTestConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.payload)
	c.payload = c.payload[n:]
	return n, nil
}

func (c *retryTestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.written.Write(p)
}

func (c *retryTestConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *retryTestConn) LocalAddr() net.Addr              { return c.localAddr }
func (c *retryTestConn) RemoteAddr() net.Addr             { return c.remoteAddr }
func (c *retryTestConn) SetDeadline(time.Time) error      { return nil }
func (c *retryTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *retryTestConn) SetWriteDeadline(time.Time) error { return nil }

func (c *retryTestConn) writtenBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.written.Bytes()...)
}

func (c *retryTestConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// retryTestPolicyManager is a minimal policy.Manager for the Process tests.
type retryTestPolicyManager struct{}

func (retryTestPolicyManager) ForLevel(uint32) policy.Session {
	return policy.Session{
		Timeouts: policy.Timeout{
			Handshake:      time.Second,
			ConnectionIdle: time.Minute,
			UplinkOnly:     time.Second,
			DownlinkOnly:   time.Second,
		},
	}
}

func (retryTestPolicyManager) ForSystem() policy.System { return policy.System{} }
func (retryTestPolicyManager) Start() error             { return nil }
func (retryTestPolicyManager) Close() error             { return nil }
func (retryTestPolicyManager) Type() interface{}        { return policy.ManagerType() }

// retryTestHandler returns a Handler with the test policy manager.
func retryTestHandler(config *Config) *Handler {
	return &Handler{config: config, policyManager: retryTestPolicyManager{}}
}

// retryTestContext returns a session context for Process. The target uses an IP
// address, so the dial loop never resolves a domain.
func retryTestContext() context.Context {
	target := xnet.TCPDestination(xnet.ParseAddress("203.0.113.10"), 443)
	ob := &session.Outbound{Target: target, OriginalTarget: target}
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{ob})
	return session.ContextWithInbound(ctx, &session.Inbound{
		Source: xnet.TCPDestination(xnet.ParseAddress("192.0.2.1"), 12345),
	})
}

// BenchmarkProcessFiveFailedDials measures the real latency of five failed
// dials through Process. Run it with -benchtime=3x.
func BenchmarkProcessFiveFailedDials(b *testing.B) {
	h := retryTestHandler(new(Config))
	ctx := retryTestContext()
	total := time.Duration(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dialer := &retryTestDialer{}
		start := time.Now()
		err := h.Process(ctx, &transport.Link{}, dialer)
		total += time.Since(start)
		if err == nil {
			b.Fatal("Process error = nil, want a dial failure")
		}
		if got := dialer.callCount(); got != 5 {
			b.Fatalf("dial calls = %d, want 5", got)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(total/time.Duration(b.N))/float64(time.Millisecond), "ms/op")
}

// BenchmarkProcessCancelDuringRetryWait measures the real delay between context
// cancellation and the Process return. The cancellation happens during the
// first positive retry wait. Run it with -benchtime=3x.
func BenchmarkProcessCancelDuringRetryWait(b *testing.B) {
	h := retryTestHandler(new(Config))
	base := retryTestContext()
	total := time.Duration(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(base)
		dialer := &retryTestDialer{}
		canceled := make(chan time.Time, 1)
		go func() {
			time.Sleep(50 * time.Millisecond)
			at := time.Now()
			cancel()
			canceled <- at
		}()
		err := h.Process(ctx, &transport.Link{}, dialer)
		returnAt := time.Now()
		total += returnAt.Sub(<-canceled)
		if err == nil {
			b.Fatal("Process error = nil, want a failure")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(total/time.Duration(b.N))/float64(time.Millisecond), "cancel-ms/op")
}

// END-BASELINE-FIXTURE

// retryEOFReader is an empty request reader. It ends the request stream at once.
type retryEOFReader struct{}

func (retryEOFReader) ReadMultiBuffer() (buf.MultiBuffer, error) { return nil, io.EOF }

// retryDiscardWriter is an in-memory response consumer. It drops every buffer.
type retryDiscardWriter struct {
	mu     sync.Mutex
	closed bool
}

func (w *retryDiscardWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	return nil
}

func (w *retryDiscardWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *retryDiscardWriter) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

var (
	_ buf.Reader = retryEOFReader{}
	_ buf.Writer = (*retryDiscardWriter)(nil)
)

// retryTestLink returns a transport.Link with an in-memory consumer.
func retryTestLink() (*transport.Link, *retryDiscardWriter) {
	output := &retryDiscardWriter{}
	return &transport.Link{Reader: retryEOFReader{}, Writer: output}, output
}

// recordingWait returns a wait function that records the delays and never blocks.
func recordingWait(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, delay time.Duration) error {
		*waits = append(*waits, delay)
		return nil
	}
}

// retryDelaySequence returns the waits before attempts 2..attemptCount.
func retryDelaySequence(attemptCount int) []time.Duration {
	waits := make([]time.Duration, 0, attemptCount-1)
	for attempt := 0; attempt < attemptCount-1; attempt++ {
		waits = append(waits, time.Duration(attempt)*retryDialWaitUnit)
	}
	return waits
}

// retryWaitsThrough returns the waits with the indexes 0..count-1. The last one
// is the wait that the context stopped.
func retryWaitsThrough(count int) []time.Duration {
	waits := make([]time.Duration, 0, count)
	for index := 0; index < count; index++ {
		waits = append(waits, time.Duration(index)*retryDialWaitUnit)
	}
	return waits
}

// retryScheduleTotal returns the sum of the inter-attempt waits.
func retryScheduleTotal(attemptCount int) time.Duration {
	total := time.Duration(0)
	for _, delay := range retryDelaySequence(attemptCount) {
		total += delay
	}
	return total
}

func TestRetryDialSucceedsOnEachAttempt(t *testing.T) {
	for successAt := 1; successAt <= retryDialAttempts; successAt++ {
		t.Run(fmt.Sprintf("attempt-%d", successAt), func(t *testing.T) {
			dials := 0
			dial := func() error {
				dials++
				if dials < successAt {
					return errRetryFakeDial
				}
				return nil
			}
			var waits []time.Duration
			if err := retryDial(context.Background(), dial, recordingWait(&waits)); err != nil {
				t.Fatalf("retryDial error = %v, want nil", err)
			}
			if dials != successAt {
				t.Fatalf("dial calls = %d, want %d", dials, successAt)
			}
			if want := retryDelaySequence(successAt); !slices.Equal(waits, want) {
				t.Fatalf("waits = %v, want %v", waits, want)
			}
		})
	}
}

func TestRetryDialFiveFailuresKeepLastCauseAndFourWaits(t *testing.T) {
	dialErrs := make([]error, retryDialAttempts)
	for i := range dialErrs {
		dialErrs[i] = fmt.Errorf("dial failure %d", i+1)
	}
	dials := 0
	dial := func() error {
		err := dialErrs[dials]
		dials++
		return err
	}
	var waits []time.Duration
	err := retryDial(context.Background(), dial, recordingWait(&waits))
	if dials != retryDialAttempts {
		t.Fatalf("dial calls = %d, want %d", dials, retryDialAttempts)
	}
	if len(waits) != retryDialAttempts-1 {
		t.Fatalf("wait count = %d, want %d (no wait after the last failure)", len(waits), retryDialAttempts-1)
	}
	if want := retryDelaySequence(retryDialAttempts); !slices.Equal(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	if err != dialErrs[retryDialAttempts-1] {
		t.Fatalf("error = %v, want the exact last dial error", err)
	}
	if !errors.Is(err, dialErrs[retryDialAttempts-1]) {
		t.Fatalf("errors.Is did not match the last dial error")
	}
}

func TestRetryDialAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dials := 0
	waitCalls := 0
	wait := func(context.Context, time.Duration) error {
		waitCalls++
		return nil
	}
	err := retryDial(ctx, func() error { dials++; return nil }, wait)
	if dials != 0 {
		t.Fatalf("dial calls = %d, want 0", dials)
	}
	if waitCalls != 0 {
		t.Fatalf("wait calls = %d, want 0", waitCalls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if cerrors.Cause(err) != context.Canceled {
		t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
	}
}

func TestRetryDialAlreadyCanceledUsesNoTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dials := 0
		start := time.Now()
		err := retryDial(ctx, func() error { dials++; return nil }, waitForRetry)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("virtual elapsed = %v, want 0 for a canceled context", elapsed)
		}
		if dials != 0 {
			t.Fatalf("dial calls = %d, want 0", dials)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestWaitForRetryTimerFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := waitForRetry(context.Background(), retryDialWaitUnit); err != nil {
			t.Fatalf("waitForRetry error = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed != retryDialWaitUnit {
			t.Fatalf("elapsed = %v, want %v", elapsed, retryDialWaitUnit)
		}
	})
}

func TestWaitForRetryContextWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The cancel goroutine runs before the fake clock advances.
		go cancel()
		start := time.Now()
		err := waitForRetry(ctx, time.Hour)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("elapsed = %v, want 0 after cancellation", elapsed)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if cerrors.Cause(err) != context.Canceled {
			t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
		}
	})
}

func TestWaitForRetryZeroDelayChecksContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := waitForRetry(context.Background(), 0); err != nil {
			t.Fatalf("waitForRetry(0) error = %v, want nil", err)
		}
		if err := waitForRetry(context.Background(), -time.Second); err != nil {
			t.Fatalf("waitForRetry(-1s) error = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("elapsed = %v, want 0 for a zero delay", elapsed)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForRetry(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForRetry(0) error = %v, want context.Canceled", err)
		}
	})
}

func TestRetryDialDeadlineStopsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(retryDialWaitUnit/2))
		defer cancel()
		dials := 0
		var waits []time.Duration
		start := time.Now()
		err := retryDial(ctx, func() error { dials++; return errRetryFakeDial }, func(ctx context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return waitForRetry(ctx, delay)
		})
		elapsed := time.Since(start)
		if dials != 2 {
			t.Fatalf("dial calls = %d, want 2", dials)
		}
		if want := retryWaitsThrough(2); !slices.Equal(waits, want) {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
		if want := retryDialWaitUnit / 2; elapsed != want {
			t.Fatalf("virtual elapsed = %v, want %v (the wait stopped at the deadline)", elapsed, want)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		if cerrors.Cause(err) != context.DeadlineExceeded {
			t.Fatalf("errors.Cause = %v, want context.DeadlineExceeded", cerrors.Cause(err))
		}
	})
}

func TestRetryDialCancellationAtEachPositiveWait(t *testing.T) {
	for _, target := range []time.Duration{retryDialWaitUnit, 2 * retryDialWaitUnit, 3 * retryDialWaitUnit} {
		t.Run(target.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dials := 0
				var waits []time.Duration
				dial := func() error {
					dials++
					return errRetryFakeDial
				}
				wait := func(ctx context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					if delay == target {
						// The context stops this wait before the timer fires.
						go cancel()
					}
					return waitForRetry(ctx, delay)
				}
				start := time.Now()
				err := retryDial(ctx, dial, wait)
				elapsed := time.Since(start)
				if dials != int(target/retryDialWaitUnit)+1 {
					t.Fatalf("dial calls = %d, want %d after cancellation in the %v wait", dials, int(target/retryDialWaitUnit)+1, target)
				}
				if want := retryWaitsThrough(dials); !slices.Equal(waits, want) {
					t.Fatalf("waits = %v, want %v", waits, want)
				}
				if want := retryScheduleTotal(dials); elapsed != want {
					t.Fatalf("virtual elapsed = %v, want %v (the %v wait did not run to the end)", elapsed, want, target)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
				if cerrors.Cause(err) != context.Canceled {
					t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
				}
			})
		})
	}
}

func TestRetryDialStopsWhenContextIsCanceledAtTheRetry(t *testing.T) {
	// The cases cover the zero-delay retry and the context check that follows a
	// wait. The wait is injected. It creates no timer and it returns nil, so
	// these cases do not cover a timer race. See
	// TestRetryDialRealTimerAndCancelBothReady for the real timer case.
	for _, cancelDelay := range []time.Duration{0, retryDialWaitUnit} {
		t.Run(cancelDelay.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dials := 0
			var waits []time.Duration
			wait := func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay)
				if delay == cancelDelay {
					cancel()
				}
				return nil
			}
			err := retryDial(ctx, func() error { dials++; return errRetryFakeDial }, wait)
			wantDials := int(cancelDelay/retryDialWaitUnit) + 1
			if dials != wantDials {
				t.Fatalf("dial calls = %d, want %d (no dial after the cancellation)", dials, wantDials)
			}
			if want := retryWaitsThrough(wantDials); !slices.Equal(waits, want) {
				t.Fatalf("waits = %v, want %v", waits, want)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if cerrors.Cause(err) != context.Canceled {
				t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
			}
		})
	}
}

// gatedDoneContext delays Done until the test opens the gate. The test uses it
// to show that the real timer expired before the select runs. Done runs after
// waitForRetry creates the timer, because the select evaluates timer.C first.
type gatedDoneContext struct {
	context.Context
	entered chan<- struct{}
	release <-chan struct{}
}

func (c *gatedDoneContext) Done() <-chan struct{} {
	c.entered <- struct{}{}
	<-c.release
	return c.Context.Done()
}

func TestRetryDialRealTimerAndCancelBothReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base, cancel := context.WithCancel(context.Background())
		defer cancel()

		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		releaseGate := func() { once.Do(func() { close(release) }) }
		// The gate opens after a failed assertion too.
		defer releaseGate()
		ctx := &gatedDoneContext{Context: base, entered: entered, release: release}

		dials := 0
		result := make(chan error, 1)
		go func() {
			result <- retryDial(ctx, func() error { dials++; return errRetryFakeDial }, waitForRetry)
		}()

		// Attempt 1 fails and the zero-delay wait creates no timer. Attempt 2
		// fails and the 100 ms wait creates the real timer. Done then blocks.
		<-entered
		// The retry goroutine waits in Done. The fake clock advances to the
		// deadline of the real timer.
		time.Sleep(retryDialWaitUnit)
		// The timer expired. Cancel the context before the select starts.
		cancel()
		releaseGate()

		err := <-result
		if dials != 2 {
			t.Fatalf("dial calls = %d, want 2 (no dial after the cancellation)", dials)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if cerrors.Cause(err) != context.Canceled {
			t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
		}
	})
}

func TestRetryDialWaitErrorStopsTheLoop(t *testing.T) {
	waitErr := fmt.Errorf("wait stopped: %w", context.Canceled)
	dials := 0
	wait := func(context.Context, time.Duration) error { return waitErr }
	err := retryDial(context.Background(), func() error { dials++; return errRetryFakeDial }, wait)
	if dials != 1 {
		t.Fatalf("dial calls = %d, want 1", dials)
	}
	if err != waitErr {
		t.Fatalf("error = %v, want the exact wait error", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false, want true")
	}
}

func TestProcessAlreadyCanceledContextSkipsDial(t *testing.T) {
	h := retryTestHandler(new(Config))
	dialer := &retryTestDialer{}
	ctx, cancel := context.WithCancel(retryTestContext())
	cancel()
	err := h.Process(ctx, &transport.Link{}, dialer)
	if err == nil {
		t.Fatal("Process error = nil, want a cancellation error")
	}
	if dialer.callCount() != 0 {
		t.Fatalf("dial calls = %d, want 0", dialer.callCount())
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled in the chain", err)
	}
	if cerrors.Cause(err) != context.Canceled {
		t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
	}
	if !strings.Contains(err.Error(), "failed to open connection to") {
		t.Fatalf("error = %q, want the freedom wrapper text", err.Error())
	}
}

func TestProcessFiveFailedDialsUseOnlyFourWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := retryTestHandler(new(Config))
		dialErrs := make([]error, retryDialAttempts)
		for i := range dialErrs {
			dialErrs[i] = fmt.Errorf("process dial failure %d", i+1)
		}
		dialer := &retryTestDialer{dial: func(call int, dest xnet.Destination) (net.Conn, error) {
			if call > len(dialErrs) {
				return nil, fmt.Errorf("unexpected dial call %d", call)
			}
			return nil, dialErrs[call-1]
		}}
		start := time.Now()
		err := h.Process(retryTestContext(), &transport.Link{}, dialer)
		elapsed := time.Since(start)
		if dialer.callCount() != retryDialAttempts {
			t.Fatalf("dial calls = %d, want %d", dialer.callCount(), retryDialAttempts)
		}
		if want := retryScheduleTotal(retryDialAttempts); elapsed != want {
			t.Fatalf("virtual elapsed = %v, want %v (no 400 ms wait after the fifth failure)", elapsed, want)
		}
		t.Logf("five failed dials took %v of virtual time", elapsed)
		if err == nil {
			t.Fatal("Process error = nil, want a dial failure")
		}
		if cerrors.Cause(err) != dialErrs[retryDialAttempts-1] {
			t.Fatalf("errors.Cause = %v, want the last dial error", cerrors.Cause(err))
		}
		if !errors.Is(err, dialErrs[retryDialAttempts-1]) {
			t.Fatalf("errors.Is did not match the last dial error")
		}
		if !strings.Contains(err.Error(), "failed to open connection to") {
			t.Fatalf("error = %q, want the freedom wrapper text", err.Error())
		}
		// A detached dial or retry goroutine would run here or deadlock the bubble.
		synctest.Wait()
		if dialer.callCount() != retryDialAttempts {
			t.Fatalf("dial calls = %d after the bubble drained, want %d", dialer.callCount(), retryDialAttempts)
		}
	})
}

func TestProcessCancellationStopsRetryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := retryTestHandler(new(Config))
		dialer := &retryTestDialer{}
		ctx, cancel := context.WithCancel(retryTestContext())
		defer cancel()
		go func() {
			time.Sleep(150 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		err := h.Process(ctx, &transport.Link{}, dialer)
		elapsed := time.Since(start)
		if dialer.callCount() != 3 {
			t.Fatalf("dial calls = %d, want 3", dialer.callCount())
		}
		if want := 150 * time.Millisecond; elapsed != want {
			t.Fatalf("virtual elapsed = %v, want %v (the wait stopped at the cancellation)", elapsed, want)
		}
		t.Logf("the canceled retry wait stopped after %v of virtual time", elapsed)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled in the chain", err)
		}
		if cerrors.Cause(err) != context.Canceled {
			t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
		}
		if !strings.Contains(err.Error(), "failed to open connection to") {
			t.Fatalf("error = %q, want the freedom wrapper text", err.Error())
		}
		// A detached dial or retry goroutine would run here or deadlock the bubble.
		synctest.Wait()
		if dialer.callCount() != 3 {
			t.Fatalf("dial calls = %d after the bubble drained, want 3", dialer.callCount())
		}
	})
}

func TestProcessCancelOnFinalDialPrefersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := retryTestHandler(new(Config))
		lastErr := errors.New("last dial failure")
		ctx, cancel := context.WithCancel(retryTestContext())
		defer cancel()
		dialer := &retryTestDialer{dial: func(call int, dest xnet.Destination) (net.Conn, error) {
			if call == retryDialAttempts {
				cancel()
			}
			return nil, lastErr
		}}
		err := h.Process(ctx, &transport.Link{}, dialer)
		if dialer.callCount() != retryDialAttempts {
			t.Fatalf("dial calls = %d, want %d", dialer.callCount(), retryDialAttempts)
		}
		if errors.Is(err, lastErr) {
			t.Fatalf("error = %v, want the cancellation error instead of the dial error", err)
		}
		if cerrors.Cause(err) != context.Canceled {
			t.Fatalf("errors.Cause = %v, want context.Canceled", cerrors.Cause(err))
		}
	})
}

func TestProcessRetriesThenSucceeds(t *testing.T) {
	h := retryTestHandler(new(Config))
	conn := newRetryTestConn()
	dialer := &retryTestDialer{dial: func(call int, _ xnet.Destination) (net.Conn, error) {
		if call == 1 {
			return nil, errRetryFakeDial
		}
		return conn, nil
	}}
	link, output := retryTestLink()
	start := time.Now()
	err := h.Process(retryTestContext(), link, dialer)
	if err != nil {
		t.Fatalf("Process error = %v, want nil", err)
	}
	if dialer.callCount() != 2 {
		t.Fatalf("dial calls = %d, want 2", dialer.callCount())
	}
	t.Logf("first successful retry took %v", time.Since(start))
	if !conn.isClosed() {
		t.Fatal("the fake connection is not closed")
	}
	if !output.isClosed() {
		t.Fatal("the response consumer is not closed")
	}
	if ob := dialer.outbound(); ob == nil || ob.Name != "freedom" {
		t.Fatalf("SetOutboundGateway outbound = %v, want the freedom outbound", ob)
	}
	if dest := dialer.destination(); dest.Address.String() != "203.0.113.10" || dest.Port != 443 {
		t.Fatalf("dial destination = %v, want 203.0.113.10:443", dest)
	}
}

func TestProcessDestinationOverrideReachesDialer(t *testing.T) {
	config := new(Config)
	config.DestinationOverride = &DestinationOverride{
		Server: &protocol.ServerEndpoint{
			Address: &xnet.IPOrDomain{Address: &xnet.IPOrDomain_Ip{Ip: net.ParseIP("192.0.2.44")}},
			Port:    8443,
		},
	}
	h := retryTestHandler(config)
	dialer := &retryTestDialer{dial: func(int, xnet.Destination) (net.Conn, error) {
		return newRetryTestConn(), nil
	}}
	link, _ := retryTestLink()
	if err := h.Process(retryTestContext(), link, dialer); err != nil {
		t.Fatalf("Process error = %v, want nil", err)
	}
	dest := dialer.destination()
	if dest.Network != xnet.Network_TCP || dest.Address.String() != "192.0.2.44" || dest.Port != 8443 {
		t.Fatalf("dial destination = %v, want the config override 192.0.2.44:8443", dest)
	}
}

func TestProcessProxyProtocolHeaderFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := new(Config)
		config.ProxyProtocol = 1
		h := retryTestHandler(config)
		writeErr := errors.New("proxy header write failed")
		conn := newRetryTestConn()
		conn.writeErr = writeErr
		dialer := &retryTestDialer{dial: func(int, xnet.Destination) (net.Conn, error) {
			return conn, nil
		}}
		start := time.Now()
		err := h.Process(retryTestContext(), &transport.Link{}, dialer)
		elapsed := time.Since(start)
		if dialer.callCount() != retryDialAttempts {
			t.Fatalf("dial calls = %d, want %d", dialer.callCount(), retryDialAttempts)
		}
		if want := retryScheduleTotal(retryDialAttempts); elapsed != want {
			t.Fatalf("virtual elapsed = %v, want %v", elapsed, want)
		}
		if !conn.isClosed() {
			t.Fatal("the connection is not closed after the header write failure")
		}
		if err == nil {
			t.Fatal("Process error = nil, want the header write error")
		}
		if !errors.Is(err, writeErr) {
			t.Fatalf("error = %v, want the header write error in the chain", err)
		}
		if cerrors.Cause(err) != writeErr {
			t.Fatalf("errors.Cause = %v, want the header write error", cerrors.Cause(err))
		}
	})
}

func TestProcessProxyProtocolHeaderSuccess(t *testing.T) {
	config := new(Config)
	config.ProxyProtocol = 1
	h := retryTestHandler(config)
	conn := newRetryTestConn()
	dialer := &retryTestDialer{dial: func(int, xnet.Destination) (net.Conn, error) {
		return conn, nil
	}}
	link, _ := retryTestLink()
	if err := h.Process(retryTestContext(), link, dialer); err != nil {
		t.Fatalf("Process error = %v, want nil", err)
	}
	if dialer.callCount() != 1 {
		t.Fatalf("dial calls = %d, want 1 (a successful dial stops the retries)", dialer.callCount())
	}
	header := conn.writtenBytes()
	if !bytes.HasPrefix(header, []byte("PROXY TCP4 192.0.2.1 203.0.113.9 12345 443")) {
		t.Fatalf("first write = %q, want the proxy protocol header", header)
	}
}
