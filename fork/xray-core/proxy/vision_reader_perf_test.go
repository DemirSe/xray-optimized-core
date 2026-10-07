package proxy

// C1: VisionReader.ReadMultiBuffer compacts the list that the supplier hands
// over, instead of building a replacement list. This file keeps the previous
// reader body as a test-only reference, compares both readers on identical
// fixtures, checks list and release state, and measures the list allocation.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	cerrors "github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// visionTestUUID is the user UUID of every fixture. It matches the UUID of
// newVisionTestState in proxy_perf_test.go.
var visionTestUUID = bytes.Repeat([]byte{0x5a}, 16)

// errVisionReadPayload is returned with a non-empty payload.
var errVisionReadPayload = errors.New("vision read payload error")

// visionReadLegacy is the production body of VisionReader.ReadMultiBuffer
// before C1. The differential tests and the benchmark use it as the reference.
func visionReadLegacy(w *VisionReader) (buf.MultiBuffer, error) {
	buffer, err := w.Reader.ReadMultiBuffer()
	if buffer.IsEmpty() {
		return buffer, err
	}

	var withinPaddingBuffers *bool
	var remainingContent *int32
	var remainingPadding *int32
	var currentCommand *int
	var switchToDirectCopy *bool
	if w.isUplink {
		withinPaddingBuffers = &w.trafficState.Inbound.WithinPaddingBuffers
		remainingContent = &w.trafficState.Inbound.RemainingContent
		remainingPadding = &w.trafficState.Inbound.RemainingPadding
		currentCommand = &w.trafficState.Inbound.CurrentCommand
		switchToDirectCopy = &w.trafficState.Inbound.UplinkReaderDirectCopy
	} else {
		withinPaddingBuffers = &w.trafficState.Outbound.WithinPaddingBuffers
		remainingContent = &w.trafficState.Outbound.RemainingContent
		remainingPadding = &w.trafficState.Outbound.RemainingPadding
		currentCommand = &w.trafficState.Outbound.CurrentCommand
		switchToDirectCopy = &w.trafficState.Outbound.DownlinkReaderDirectCopy
	}

	if *switchToDirectCopy {
		if w.directReadCounter != nil {
			w.directReadCounter.Add(int64(buffer.Len()))
		}
		return buffer, err
	}

	if *withinPaddingBuffers || w.trafficState.NumberOfPacketToFilter > 0 {
		mb2 := make(buf.MultiBuffer, 0, len(buffer))
		for _, b := range buffer {
			newbuffer := XtlsUnpadding(b, w.trafficState, w.isUplink, w.ctx)
			if newbuffer.Len() > 0 {
				mb2 = append(mb2, newbuffer)
			}
		}
		buffer = mb2
		if *remainingContent > 0 || *remainingPadding > 0 || *currentCommand == 0 {
			*withinPaddingBuffers = true
		} else if *currentCommand == 1 {
			*withinPaddingBuffers = false
		} else if *currentCommand == 2 {
			*withinPaddingBuffers = false
			*switchToDirectCopy = true
		} else {
			cerrors.LogDebug(w.ctx, "XtlsRead unknown command ", *currentCommand, buffer.Len())
		}
	}
	if w.trafficState.NumberOfPacketToFilter > 0 {
		XtlsFilterTls(buffer, w.trafficState, w.ctx)
	}

	if *switchToDirectCopy {
		// XTLS Vision processes TLS-like conn's input and rawInput
		if inputBuffer, err := buf.ReadFrom(w.input); err == nil && !inputBuffer.IsEmpty() {
			buffer, _ = buf.MergeMulti(buffer, inputBuffer)
		}
		if rawInputBuffer, err := buf.ReadFrom(w.rawInput); err == nil && !rawInputBuffer.IsEmpty() {
			buffer, _ = buf.MergeMulti(buffer, rawInputBuffer)
		}
		*w.input = bytes.Reader{} // release memory
		w.input = nil
		*w.rawInput = bytes.Buffer{} // release memory
		w.rawInput = nil

		if inbound := session.InboundFromContext(w.ctx); inbound != nil && inbound.Conn != nil {
			if !w.isUplink && w.ob != nil && w.ob.CanSpliceCopy == 2 {
				w.ob.CanSpliceCopy = 1
			}
		}
		readerConn, readCounter, _ := UnwrapRawConn(w.conn)
		w.directReadCounter = readCounter
		w.Reader = buf.NewReader(readerConn)
	}
	return buffer, err
}

// visionReadCounter records the counter calls of one reader run.
type visionReadCounter struct {
	total int64
}

func (c *visionReadCounter) Add(delta int64) int64 {
	previous := c.total
	c.total += delta
	return previous
}

func (c *visionReadCounter) Set(value int64) int64 {
	previous := c.total
	c.total = value
	return previous
}

func (c *visionReadCounter) Value() int64 { return c.total }

// visionReadStep is one scripted supplier read.
type visionReadStep struct {
	buffers func() buf.MultiBuffer
	err     error
}

// scriptedVisionReader hands the fixture buffers to the reader under test. It
// records the supplier slice and its original elements before the read touches
// them, so the test can inspect released state and cleared slots afterwards.
type scriptedVisionReader struct {
	steps []visionReadStep
	next  int

	lastBuffers  buf.MultiBuffer
	lastPointers []*buf.Buffer
}

func (r *scriptedVisionReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.next >= len(r.steps) {
		return nil, io.EOF
	}
	step := r.steps[r.next]
	r.next++
	mb := step.buffers()
	r.lastBuffers = mb
	r.lastPointers = append(r.lastPointers[:0], mb...)
	return mb, step.err
}

// visionReadFixture describes one differential input. Every builder returns
// fresh state and fresh managed buffers, so the two runs never share storage.
type visionReadFixture struct {
	name        string
	state       func(isUplink bool) *TrafficState
	steps       []visionReadStep
	inputBytes  []byte
	rawInput    []byte
	conn        net.Conn
	withInbound bool
	obSplice    int
}

// visionReadDefaultState returns the state of a fresh uplink or downlink read.
func visionReadDefaultState() *TrafficState {
	return NewTrafficState(visionTestUUID)
}

func visionReadStateWith(mutate func(*TrafficState)) func(isUplink bool) *TrafficState {
	return func(isUplink bool) *TrafficState {
		state := NewTrafficState(visionTestUUID)
		mutate(state)
		return state
	}
}

func visionReadStateDirectional(mutate func(*TrafficState, bool)) func(isUplink bool) *TrafficState {
	return func(isUplink bool) *TrafficState {
		state := NewTrafficState(visionTestUUID)
		mutate(state, isUplink)
		return state
	}
}

// visionReadSetDirectCopy sets the reader direct-copy flag of one direction.
func visionReadSetDirectCopy(state *TrafficState, isUplink bool, value bool) {
	if isUplink {
		state.Inbound.UplinkReaderDirectCopy = value
	} else {
		state.Outbound.DownlinkReaderDirectCopy = value
	}
}

// visionReadSetWithinPadding sets the reader padding flag of one direction.
func visionReadSetWithinPadding(state *TrafficState, isUplink bool, value bool) {
	if isUplink {
		state.Inbound.WithinPaddingBuffers = value
	} else {
		state.Outbound.WithinPaddingBuffers = value
	}
}

// visionReadPayload returns deterministic payload bytes.
func visionReadPayload(n int, seed byte) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(int(seed) + i)
	}
	return data
}

// visionReadBlock encodes one Vision padding block with optional UUID.
func visionReadBlock(hasUUID bool, command byte, content []byte, padding int) []byte {
	block := make([]byte, 0, 21+len(content)+padding)
	if hasUUID {
		block = append(block, visionTestUUID...)
	}
	block = append(block,
		command,
		byte(len(content)>>8), byte(len(content)),
		byte(padding>>8), byte(padding))
	block = append(block, content...)
	block = append(block, make([]byte, padding)...)
	return block
}

// visionReadManaged copies data into a managed buffer, so Release is
// observable through Cap.
func visionReadManaged(data []byte) *buf.Buffer {
	b := buf.New()
	if len(data) > 0 {
		if _, err := b.Write(data); err != nil {
			panic("vision read fixture buffer is too large: " + err.Error())
		}
	}
	return b
}

// visionReadBlocks makes one supplier step with one managed buffer per block.
func visionReadBlocks(err error, blocks ...[]byte) visionReadStep {
	return visionReadStep{
		buffers: func() buf.MultiBuffer {
			mb := make(buf.MultiBuffer, 0, len(blocks))
			for _, block := range blocks {
				mb = append(mb, visionReadManaged(block))
			}
			return mb
		},
		err: err,
	}
}

// visionReadStepOf makes one supplier step from an explicit buffer builder.
func visionReadStepOf(err error, build func() buf.MultiBuffer) visionReadStep {
	return visionReadStep{buffers: build, err: err}
}

// visionEligibleFixture builds n blocks that reach the list assembly: the
// first block carries the UUID, every later block continues with command 0.
// Even blocks after the first are padding-only, so their empty outputs move
// the write index.
func visionEligibleFixture(name string, n int) visionReadFixture {
	blocks := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		hasUUID := i == 0
		if i%2 == 1 && i != 0 {
			blocks = append(blocks, visionReadBlock(hasUUID, CommandPaddingContinue, nil, 1024))
			continue
		}
		blocks = append(blocks, visionReadBlock(hasUUID, CommandPaddingContinue, visionReadPayload(1024, byte(i)), 256))
	}
	return visionReadFixture{
		name:  name,
		state: visionReadStateWith(func(state *TrafficState) { state.NumberOfPacketToFilter = 0 }),
		steps: []visionReadStep{visionReadBlocks(nil, blocks...)},
	}
}

// visionReadControlFixture builds the unchanged controls: direct-read and
// no-assembly states must behave exactly like the reference.
func visionReadControlFixture(name string, mutate func(*TrafficState, bool)) visionReadFixture {
	return visionReadFixture{
		name:  name,
		state: visionReadStateDirectional(mutate),
		steps: []visionReadStep{
			visionReadBlocks(nil, visionReadPayload(64, 1), visionReadPayload(32, 2)),
		},
	}
}

// visionReadFixtures is the differential matrix. The benchmark reuses the same
// builders, so every measurement starts from identical state and input.
func visionReadFixtures() []visionReadFixture {
	wrongUUID := bytes.Repeat([]byte{0x11}, 16)
	short := append(append([]byte(nil), visionTestUUID...), 0x01, 0x00)
	partial := visionReadBlock(true, CommandPaddingContinue, visionReadPayload(300, 9), 0)[:16+5+100]
	// A valid server hello: the filter reads the session ID length at index 43
	// and the cipher suite right after the session ID.
	serverHello := make([]byte, 86)
	serverHello[0], serverHello[1], serverHello[2] = 0x16, 0x03, 0x03
	serverHello[3], serverHello[4] = 0x00, 0x50
	serverHello[5] = TlsHandshakeTypeServerHello
	serverHello[43] = 32
	clientHello := make([]byte, 64)
	clientHello[0], clientHello[1], clientHello[5] = 0x16, 0x03, TlsHandshakeTypeClientHello

	fixtures := []visionReadFixture{
		{
			name:  "empty-list",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer { return buf.MultiBuffer{} })},
		},
		{
			name:  "nil-list",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer { return nil })},
		},
		{
			name:  "empty-buffers-only",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				return buf.MultiBuffer{buf.New(), buf.New()}
			})},
		},
		{
			name:  "uuid-continue-block",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 32))},
		},
		{
			name:  "uuid-end-block",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, visionReadBlock(true, CommandPaddingEnd, visionReadPayload(64, 1), 32))},
		},
		{
			name:        "uuid-direct-block",
			state:       func(bool) *TrafficState { return visionReadDefaultState() },
			steps:       []visionReadStep{visionReadBlocks(nil, visionReadBlock(true, CommandPaddingDirect, visionReadPayload(64, 1), 32))},
			inputBytes:  visionReadPayload(48, 3),
			rawInput:    visionReadPayload(16, 4),
			conn:        &recordingConn{},
			withInbound: true,
			obSplice:    2,
		},
		{
			name:  "unmatched-uuid",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, append(append([]byte(nil), wrongUUID...),
				visionReadBlock(false, CommandPaddingContinue, visionReadPayload(64, 1), 0)...))},
		},
		{
			name:  "short-header-with-uuid-prefix",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, short)},
		},
		{
			name:  "partial-block",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, partial)},
		},
		{
			name:  "padding-only-block",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, visionReadBlock(true, CommandPaddingContinue, nil, 256))},
		},
		{
			name:  "content-only-block",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(300, 5), 0))},
		},
		{
			name:  "two-blocks-one-buffer",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil,
				visionReadBlock(true, CommandPaddingContinue, visionReadPayload(32, 1), 16),
				visionReadBlock(false, CommandPaddingEnd, visionReadPayload(32, 2), 16),
			)},
		},
		{
			name:  "end-then-trailing",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil, append(
				visionReadBlock(true, CommandPaddingEnd, visionReadPayload(8, 1), 0),
				visionReadPayload(5, 2)...))},
		},
		{
			name:  "two-buffers-continue",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(nil,
				visionReadBlock(true, CommandPaddingContinue, visionReadPayload(100, 1), 0),
				visionReadBlock(false, CommandPaddingContinue, visionReadPayload(100, 2), 0),
			)},
		},
		{
			name:  "leading-empty-buffer",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				return buf.MultiBuffer{buf.New(), visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0))}
			})},
		},
		{
			name:  "middle-empty-buffer",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				return buf.MultiBuffer{
					visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0)),
					buf.New(),
					visionReadManaged(visionReadBlock(false, CommandPaddingContinue, visionReadPayload(64, 2), 0)),
				}
			})},
		},
		{
			name:  "trailing-empty-buffer",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				return buf.MultiBuffer{
					visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0)),
					buf.New(),
				}
			})},
		},
		{
			name:  "nil-element",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				return buf.MultiBuffer{
					visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0)),
					nil,
					visionReadManaged(visionReadBlock(false, CommandPaddingContinue, visionReadPayload(64, 2), 0)),
				}
			})},
		},
		{
			name:  "error-with-payload-and-eof",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(io.EOF, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 32))},
		},
		{
			name:  "error-with-payload-custom",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{visionReadBlocks(errVisionReadPayload, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 32))},
		},
		{
			name:  "multi-step-continue-then-end",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{
				visionReadBlocks(nil, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0)),
				visionReadBlocks(nil, visionReadBlock(false, CommandPaddingEnd, visionReadPayload(64, 2), 0)),
			},
		},
		{
			name:  "multi-step-end-then-new-uuid",
			state: func(bool) *TrafficState { return visionReadDefaultState() },
			steps: []visionReadStep{
				visionReadBlocks(nil, visionReadBlock(true, CommandPaddingEnd, visionReadPayload(64, 1), 0)),
				visionReadBlocks(nil, visionReadBlock(true, CommandPaddingEnd, visionReadPayload(64, 2), 0)),
			},
		},
		{
			name:  "filter-only-server-hello",
			state: visionReadStateWith(func(state *TrafficState) { state.NumberOfPacketToFilter = 8 }),
			steps: []visionReadStep{visionReadBlocks(nil, serverHello)},
		},
		{
			name:  "filter-only-client-hello",
			state: visionReadStateWith(func(state *TrafficState) { state.NumberOfPacketToFilter = 8 }),
			steps: []visionReadStep{visionReadBlocks(nil, clientHello)},
		},
		{
			name:  "sixteen-mixed-buffers",
			state: visionReadStateWith(func(state *TrafficState) { state.NumberOfPacketToFilter = 0 }),
			steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
				mb := make(buf.MultiBuffer, 0, 16)
				for i := 0; i < 16; i++ {
					if i == 0 {
						mb = append(mb, visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(512, byte(i)), 64)))
					} else if i%3 == 1 {
						mb = append(mb, visionManagedEmpty())
					} else if i%3 == 2 {
						mb = append(mb, visionReadManaged(visionReadBlock(false, CommandPaddingContinue, nil, 512)))
					} else {
						mb = append(mb, visionReadManaged(visionReadBlock(false, CommandPaddingContinue, visionReadPayload(512, byte(i)), 64)))
					}
				}
				return mb
			})},
		},
	}

	// The controls depend on the reader direction.
	fixtures = append(fixtures,
		visionReadControlFixture("control-direct-read", func(state *TrafficState, isUplink bool) {
			visionReadSetDirectCopy(state, isUplink, true)
		}),
		visionReadControlFixture("control-padding-no-filter", func(state *TrafficState, isUplink bool) {
			state.NumberOfPacketToFilter = 0
		}),
		visionReadControlFixture("control-no-padding-no-filter", func(state *TrafficState, isUplink bool) {
			state.NumberOfPacketToFilter = 0
			visionReadSetWithinPadding(state, isUplink, false)
		}),
	)
	return fixtures
}

// visionManagedEmpty returns one owned empty managed buffer.
func visionManagedEmpty() *buf.Buffer {
	return buf.New()
}

// visionReadStepOutcome records one read result.
type visionReadStepOutcome struct {
	buffers      [][]byte
	live         []bool
	err          error
	inputRelease []bool
	inputEmpty   []bool
	slotLeftover int
}

// visionReadRun records one full reader run.
type visionReadRun struct {
	steps       []visionReadStepOutcome
	state       TrafficState
	counter     int64
	obSplice    int
	inputNil    bool
	rawInputNil bool
	readerType  string
}

// newVisionReadRun builds one independent reader from a fixture.
func newVisionReadRun(fixture visionReadFixture, isUplink bool) (*VisionReader, *scriptedVisionReader, *visionReadCounter, *session.Outbound) {
	supplier := &scriptedVisionReader{steps: fixture.steps}
	counter := &visionReadCounter{}
	ob := &session.Outbound{CanSpliceCopy: fixture.obSplice}
	ctx := context.Background()
	if fixture.withInbound {
		ctx = session.ContextWithInbound(ctx, &session.Inbound{Conn: &recordingConn{}})
	}
	w := NewVisionReader(supplier, fixture.state(isUplink), isUplink, ctx, fixture.conn,
		bytes.NewReader(fixture.inputBytes), bytes.NewBuffer(fixture.rawInput), ob)
	w.directReadCounter = counter
	return w, supplier, counter, ob
}

func visionStepOutcome(supplier *scriptedVisionReader, out buf.MultiBuffer, err error) visionReadStepOutcome {
	outcome := visionReadStepOutcome{err: err}
	for _, b := range out {
		outcome.buffers = append(outcome.buffers, append([]byte(nil), b.Bytes()...))
		outcome.live = append(outcome.live, b.Cap() > 0)
	}
	for i := len(out); i < len(supplier.lastBuffers); i++ {
		if supplier.lastBuffers[i] != nil {
			outcome.slotLeftover++
		}
	}
	for _, p := range supplier.lastPointers {
		if p == nil {
			outcome.inputRelease = append(outcome.inputRelease, true)
			outcome.inputEmpty = append(outcome.inputEmpty, true)
			continue
		}
		outcome.inputRelease = append(outcome.inputRelease, p.Cap() == 0)
		outcome.inputEmpty = append(outcome.inputEmpty, p.Len() == 0)
	}
	return outcome
}

// runVisionRead drives one reader implementation over the fixture steps.
func runVisionRead(fixture visionReadFixture, isUplink bool, read func(*VisionReader) (buf.MultiBuffer, error)) visionReadRun {
	w, supplier, counter, ob := newVisionReadRun(fixture, isUplink)
	run := visionReadRun{}
	for range fixture.steps {
		out, err := read(w)
		run.steps = append(run.steps, visionStepOutcome(supplier, out, err))
		buf.ReleaseMulti(out)
	}
	run.state = *w.trafficState
	run.counter = counter.total
	run.obSplice = ob.CanSpliceCopy
	run.inputNil = w.input == nil
	run.rawInputNil = w.rawInput == nil
	run.readerType = fmt.Sprintf("%T", w.Reader)
	return run
}

// checkVisionRunMatchesLegacy compares the candidate against the reference.
func checkVisionRunMatchesLegacy(t *testing.T, legacy, candidate visionReadRun) {
	t.Helper()
	if len(legacy.steps) != len(candidate.steps) {
		t.Fatalf("candidate read %d times, reference read %d times", len(candidate.steps), len(legacy.steps))
	}
	for step := range legacy.steps {
		want := legacy.steps[step]
		got := candidate.steps[step]
		if len(want.buffers) != len(got.buffers) {
			t.Fatalf("step %d: candidate returned %d buffers, reference %d", step, len(got.buffers), len(want.buffers))
		}
		for i := range want.buffers {
			if !bytes.Equal(want.buffers[i], got.buffers[i]) {
				t.Errorf("step %d: output %d has %d bytes, reference has %d; first mismatch at %d",
					step, i, len(got.buffers[i]), len(want.buffers[i]), firstDiff(want.buffers[i], got.buffers[i]))
			}
			if !got.live[i] {
				t.Errorf("step %d: output %d is released but returned to the consumer", step, i)
			}
		}
		if want.err != got.err {
			t.Errorf("step %d: error = %v, reference %v", step, got.err, want.err)
		}
		for i := range want.inputRelease {
			if want.inputRelease[i] && !got.inputRelease[i] {
				t.Errorf("step %d: input %d was released by the reference but not by the candidate", step, i)
			}
			if got.inputRelease[i] && !want.inputRelease[i] && !got.inputEmpty[i] {
				t.Errorf("step %d: candidate released non-empty input %d", step, i)
			}
		}
	}
	if !reflect.DeepEqual(legacy.state, candidate.state) {
		t.Errorf("traffic state mismatch:\ncandidate %+v\nreference %+v", candidate.state, legacy.state)
	}
	if legacy.counter != candidate.counter {
		t.Errorf("direct read counter = %d, reference %d", candidate.counter, legacy.counter)
	}
	if legacy.obSplice != candidate.obSplice {
		t.Errorf("outbound CanSpliceCopy = %d, reference %d", candidate.obSplice, legacy.obSplice)
	}
	if legacy.inputNil != candidate.inputNil || legacy.rawInputNil != candidate.rawInputNil {
		t.Errorf("input/rawInput nil = %v/%v, reference %v/%v",
			candidate.inputNil, candidate.rawInputNil, legacy.inputNil, legacy.rawInputNil)
	}
	if legacy.readerType != candidate.readerType {
		t.Errorf("reader type = %s, reference %s", candidate.readerType, legacy.readerType)
	}
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return len(a)
}

func TestVisionReaderListCompactionMatchesLegacy(t *testing.T) {
	for _, fixture := range visionReadFixtures() {
		for _, isUplink := range []bool{false, true} {
			direction := "downlink"
			if isUplink {
				direction = "uplink"
			}
			t.Run(fixture.name+"/"+direction, func(t *testing.T) {
				legacy := runVisionRead(fixture, isUplink, visionReadLegacy)
				candidate := runVisionRead(fixture, isUplink, (*VisionReader).ReadMultiBuffer)
				checkVisionRunMatchesLegacy(t, legacy, candidate)
			})
		}
	}
}

func TestVisionReaderListCompactionClearsUnusedSlots(t *testing.T) {
	fixture := visionEligibleFixture("sixteen", 16)
	w, supplier, _, _ := newVisionReadRun(fixture, true)
	out, err := w.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("ReadMultiBuffer: %v", err)
	}
	defer buf.ReleaseMulti(out)
	if len(out) == 0 {
		t.Fatal("ReadMultiBuffer returned no buffers")
	}
	if len(supplier.lastBuffers) != 16 {
		t.Fatalf("supplier handed over %d buffers, want 16", len(supplier.lastBuffers))
	}
	if &out[0] != &supplier.lastBuffers[0] {
		t.Error("candidate output does not share the supplier slice backing array")
	}
	for i := len(out); i < len(supplier.lastBuffers); i++ {
		if supplier.lastBuffers[i] != nil {
			t.Errorf("unused slot %d still holds a buffer pointer", i)
		}
	}
}

func TestVisionReaderListCompactionReleasesEmptyDiscardedInputs(t *testing.T) {
	empty := buf.New()
	fixture := visionReadFixture{
		name:  "leading-empty",
		state: func(bool) *TrafficState { return visionReadDefaultState() },
		steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer {
			return buf.MultiBuffer{empty, visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0))}
		})},
	}
	legacy := runVisionRead(fixture, true, visionReadLegacy)
	if legacy.steps[0].inputRelease[0] {
		t.Fatal("reference released the empty input; the fixture no longer shows the improvement")
	}
	fixture.steps[0].buffers = func() buf.MultiBuffer {
		return buf.MultiBuffer{buf.New(), visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0))}
	}
	candidate := runVisionRead(fixture, true, (*VisionReader).ReadMultiBuffer)
	if !candidate.steps[0].inputRelease[0] {
		t.Error("candidate did not release the discarded empty input")
	}
	if len(candidate.steps[0].buffers) != 1 {
		t.Errorf("candidate returned %d buffers, want the 1 non-empty output", len(candidate.steps[0].buffers))
	}
	if candidate.steps[0].slotLeftover != 0 {
		t.Errorf("candidate left %d non-nil supplier slots", candidate.steps[0].slotLeftover)
	}
}

func TestVisionReaderListCompactionKeepsHeldOutput(t *testing.T) {
	fixture := visionReadFixture{
		name:  "held-output",
		state: func(bool) *TrafficState { return visionReadDefaultState() },
		steps: []visionReadStep{
			visionReadBlocks(nil, visionReadBlock(true, CommandPaddingContinue, visionReadPayload(256, 1), 32)),
			visionReadBlocks(nil, visionReadBlock(false, CommandPaddingContinue, visionReadPayload(256, 2), 32)),
		},
	}
	w, _, _, _ := newVisionReadRun(fixture, true)
	held, err := w.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("first ReadMultiBuffer: %v", err)
	}
	defer buf.ReleaseMulti(held)
	heldBytes := make([][]byte, len(held))
	for i, b := range held {
		heldBytes[i] = append([]byte(nil), b.Bytes()...)
	}

	// A second read and heavy pool traffic must not touch the held outputs.
	second, err := w.ReadMultiBuffer()
	buf.ReleaseMulti(second)
	if err != nil {
		t.Fatalf("second ReadMultiBuffer: %v", err)
	}
	for i := 0; i < 64; i++ {
		scratch := buf.New()
		scratch.Write(visionReadPayload(128, byte(i%7)))
		scratch.Release()
	}
	for i, b := range held {
		if b.Cap() == 0 {
			t.Fatalf("held output %d was released", i)
		}
		if !bytes.Equal(b.Bytes(), heldBytes[i]) {
			t.Errorf("held output %d changed after later pool traffic", i)
		}
	}
}

func TestVisionReaderListCompactionBufferedReaderHandoff(t *testing.T) {
	first := buf.MultiBuffer{visionReadManaged(visionReadBlock(true, CommandPaddingContinue, visionReadPayload(64, 1), 0))}
	supplier := &scriptedVisionReader{steps: []visionReadStep{
		visionReadBlocks(nil, visionReadBlock(false, CommandPaddingContinue, visionReadPayload(64, 2), 0)),
	}}
	buffered := &buf.BufferedReader{Reader: supplier, Buffer: first}
	w := NewVisionReader(buffered, visionReadDefaultState(), true, context.Background(), nil,
		bytes.NewReader(nil), bytes.NewBuffer(nil), nil)
	w.directReadCounter = &visionReadCounter{}

	out, err := w.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("ReadMultiBuffer: %v", err)
	}
	if buffered.Buffer != nil {
		t.Error("BufferedReader kept the handed-off list")
	}
	if out.Len() != 64 {
		t.Fatalf("first read returned %d bytes, want 64", out.Len())
	}
	buf.ReleaseMulti(out)

	next, err := w.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("second ReadMultiBuffer: %v", err)
	}
	if next.Len() != 64 {
		t.Fatalf("second read returned %d bytes, want 64", next.Len())
	}
	buf.ReleaseMulti(next)
}

func TestVisionReaderListCompactionDirectTransition(t *testing.T) {
	inputBytes := visionReadPayload(48, 3)
	rawInputBytes := visionReadPayload(16, 4)
	block := visionReadBlock(true, CommandPaddingDirect, visionReadPayload(64, 1), 0)
	for _, isUplink := range []bool{false, true} {
		direction := "downlink"
		if isUplink {
			direction = "uplink"
		}
		t.Run(direction, func(t *testing.T) {
			directCounter := &visionReadCounter{}
			sessionCounter := &visionReadCounter{}
			conn := &stat.CounterConnection{Conn: &recordingConn{}, ReadCounter: sessionCounter}
			supplier := &scriptedVisionReader{steps: []visionReadStep{visionReadBlocks(nil, block)}}
			state := visionReadDefaultState()
			ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Conn: conn})
			ob := &session.Outbound{CanSpliceCopy: 2}
			w := NewVisionReader(supplier, state, isUplink, ctx, conn,
				bytes.NewReader(inputBytes), bytes.NewBuffer(rawInputBytes), ob)
			w.directReadCounter = directCounter

			out, err := w.ReadMultiBuffer()
			if err != nil {
				t.Fatalf("ReadMultiBuffer: %v", err)
			}
			if len(out) != 3 {
				t.Fatalf("output has %d buffers, want payload, TLS input, and raw input", len(out))
			}
			if got := out.Len(); got != 64+48+16 {
				t.Errorf("output has %d bytes, want %d", got, 64+48+16)
			}
			if w.input != nil || w.rawInput != nil {
				t.Error("TLS input buffers were not drained")
			}
			if got := visionReadDirectCopy(&state.Inbound, &state.Outbound, isUplink); !got {
				t.Error("direct copy flag was not set")
			}
			if isUplink {
				if state.Inbound.WithinPaddingBuffers {
					t.Error("padding flag stayed set on the direct command")
				}
			} else if state.Outbound.WithinPaddingBuffers {
				t.Error("padding flag stayed set on the direct command")
			}
			// The legacy path only promotes the downlink splice flag.
			wantSplice := 2
			if !isUplink {
				wantSplice = 1
			}
			if ob.CanSpliceCopy != wantSplice {
				t.Errorf("outbound CanSpliceCopy = %d, want %d", ob.CanSpliceCopy, wantSplice)
			}
			if w.directReadCounter != sessionCounter {
				t.Error("direct read counter was not replaced by the unwrapped read counter")
			}
			if directCounter.total != 0 {
				t.Errorf("replaced counter recorded %d bytes, want 0", directCounter.total)
			}

			// The next read goes through the raw reader with the counter attached.
			next := buf.MultiBuffer{visionReadManaged(visionReadPayload(32, 5))}
			w.Reader = &scriptedVisionReader{steps: []visionReadStep{visionReadStepOf(nil, func() buf.MultiBuffer { return next })}}
			direct, err := w.ReadMultiBuffer()
			if err != nil {
				t.Fatalf("direct ReadMultiBuffer: %v", err)
			}
			if direct.Len() != 32 {
				t.Errorf("direct read returned %d bytes, want 32", direct.Len())
			}
			if sessionCounter.total != 32 {
				t.Errorf("direct read counter = %d, want 32", sessionCounter.total)
			}
			buf.ReleaseMulti(out)
			buf.ReleaseMulti(direct)
		})
	}
}

func visionReadDirectCopy(inbound *InboundState, outbound *OutboundState, isUplink bool) bool {
	if isUplink {
		return inbound.UplinkReaderDirectCopy
	}
	return outbound.DownlinkReaderDirectCopy
}

func TestVisionReaderListCompactionRemovesListAllocation(t *testing.T) {
	fixtures := []visionReadFixture{
		visionEligibleFixture("one", 1),
		visionEligibleFixture("four", 4),
		visionEligibleFixture("sixteen", 16),
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			candidateAllocs := testing.AllocsPerRun(200, func() {
				w, _, _, _ := newVisionReadRun(fixture, true)
				out, err := w.ReadMultiBuffer()
				if err != nil {
					t.Fatalf("ReadMultiBuffer: %v", err)
				}
				buf.ReleaseMulti(out)
			})
			legacyAllocs := testing.AllocsPerRun(200, func() {
				w, _, _, _ := newVisionReadRun(fixture, true)
				out, err := visionReadLegacy(w)
				if err != nil {
					t.Fatalf("reference ReadMultiBuffer: %v", err)
				}
				buf.ReleaseMulti(out)
			})
			if legacyAllocs < candidateAllocs+1 {
				t.Fatalf("allocations per read: reference %.1f, candidate %.1f; the reference must keep the extra list allocation",
					legacyAllocs, candidateAllocs)
			}
			t.Logf("allocations per read: reference %.1f, candidate %.1f", legacyAllocs, candidateAllocs)
		})
	}
}

// visionCompactionBenchmarkCases keeps the benchmark focused on eligible
// reads and the unchanged controls. Every subcase rebuilds state and buffers,
// so baseline and candidate start from identical inputs.
func visionCompactionBenchmarkCases() []visionReadFixture {
	cases := []visionReadFixture{
		visionEligibleFixture("eligible/1-buffer", 1),
		visionEligibleFixture("eligible/4-buffers", 4),
		visionEligibleFixture("eligible/16-buffers", 16),
	}
	controls := map[string]bool{
		"control-direct-read":          true,
		"control-padding-no-filter":    true,
		"control-no-padding-no-filter": true,
	}
	for _, fixture := range visionReadFixtures() {
		if controls[fixture.name] {
			cases = append(cases, fixture)
		}
	}
	return cases
}

// TestVisionReaderListCompactionFixtureDigest records the identity of every
// benchmark fixture. The digest covers the exact input bytes of every step.
func TestVisionReaderListCompactionFixtureDigest(t *testing.T) {
	for _, fixture := range visionCompactionBenchmarkCases() {
		hash := sha256.New()
		inputBytes := 0
		for _, step := range fixture.steps {
			mb := step.buffers()
			inputBytes += int(mb.Len())
			for _, b := range mb {
				if b != nil {
					hash.Write(b.Bytes())
				}
			}
			buf.ReleaseMulti(mb)
			if step.err != nil {
				hash.Write([]byte(step.err.Error()))
			}
		}
		w, _, _, _ := newVisionReadRun(fixture, true)
		out, err := w.ReadMultiBuffer()
		if err != nil && err != io.EOF {
			t.Fatalf("fixture %s: ReadMultiBuffer: %v", fixture.name, err)
		}
		outputBytes := int(out.Len())
		buf.ReleaseMulti(out)
		t.Logf("fixture %s: sha256 %x, input %d bytes, output %d bytes", fixture.name, hash.Sum(nil), inputBytes, outputBytes)
	}
}

func BenchmarkVisionReaderListCompaction(b *testing.B) {
	for _, fixture := range visionCompactionBenchmarkCases() {
		for _, implementation := range []struct {
			name string
			read func(*VisionReader) (buf.MultiBuffer, error)
		}{
			{name: "baseline-legacy-list", read: visionReadLegacy},
			{name: "candidate-compact-list", read: (*VisionReader).ReadMultiBuffer},
		} {
			b.Run(fixture.name+"/"+implementation.name, func(b *testing.B) {
				var total int
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					w, _, _, _ := newVisionReadRun(fixture, true)
					out, err := implementation.read(w)
					total += int(out.Len())
					buf.ReleaseMulti(out)
					if err != nil && err != io.EOF {
						b.Fatalf("ReadMultiBuffer: %v", err)
					}
				}
				b.StopTimer()
				if total == 0 {
					b.Fatal("benchmark read no payload bytes")
				}
			})
		}
	}
}
