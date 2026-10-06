package proxy

// IsCompleteRecord inspects the active buffer views of a MultiBuffer directly,
// without flattening the payload. This file keeps the previous implementation
// as a test-only reference and compares both parsers on the same inputs.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
)

// isCompleteRecordReference is the previous production implementation. It
// allocates a flat byte slice and copies the whole MultiBuffer first.
func isCompleteRecordReference(buffer buf.MultiBuffer) bool {
	b := make([]byte, buffer.Len())
	if buffer.Copy(b) != int(buffer.Len()) {
		panic("impossible bytes allocation")
	}
	var headerLen int = 5
	var recordLen int

	totalLen := len(b)
	i := 0
	for i < totalLen {
		// record header: 0x17 0x3 0x3 + 2 bytes length
		if headerLen > 0 {
			data := b[i]
			i++
			switch headerLen {
			case 5:
				if data != 0x17 {
					return false
				}
			case 4:
				if data != 0x03 {
					return false
				}
			case 3:
				if data != 0x03 {
					return false
				}
			case 2:
				recordLen = int(data) << 8
			case 1:
				recordLen = recordLen | int(data)
			}
			headerLen--
		} else if recordLen > 0 {
			remaining := totalLen - i
			if remaining < recordLen {
				return false
			} else {
				i += recordLen
				recordLen = 0
				headerLen = 5
			}
		} else {
			return false
		}
	}
	if headerLen == 5 && recordLen == 0 {
		return true
	}
	return false
}

// parseOutcome is the result of one parser call. recovered is nil unless the
// parser panicked.
type parseOutcome struct {
	complete  bool
	recovered any
}

// runIsCompleteRecord calls one parser and records a panic instead of failing.
func runIsCompleteRecord(parse func(buf.MultiBuffer) bool, mb buf.MultiBuffer) (outcome parseOutcome) {
	defer func() {
		outcome.recovered = recover()
	}()
	outcome.complete = parse(mb)
	return outcome
}

// tlsRecord builds one TLS application-data record with a payload of exactly
// payloadLen bytes. Payload bytes are deterministic.
func tlsRecord(payloadLen int) []byte {
	record := make([]byte, 5+payloadLen)
	record[0] = 0x17
	record[1] = 0x03
	record[2] = 0x03
	record[3] = byte(payloadLen >> 8)
	record[4] = byte(payloadLen)
	for i := 5; i < len(record); i++ {
		record[i] = byte(i)
	}
	return record
}

// tlsRecords concatenates records with the given payload lengths.
func tlsRecords(payloadLens ...int) []byte {
	var records []byte
	for _, payloadLen := range payloadLens {
		records = append(records, tlsRecord(payloadLen)...)
	}
	return records
}

// multiBufferOf wraps fragments in unmanaged buffers. Release is a no-op for
// unmanaged buffers, so the test keeps ownership of the bytes.
func multiBufferOf(fragments ...[]byte) buf.MultiBuffer {
	mb := make(buf.MultiBuffer, 0, len(fragments))
	for _, fragment := range fragments {
		mb = append(mb, buf.FromBytes(fragment))
	}
	return mb
}

// offsetBuffer returns a buffer whose active range starts at the given offset
// inside its backing array.
func offsetBuffer(data []byte, offset int) *buf.Buffer {
	storage := make([]byte, offset+len(data))
	copy(storage[offset:], data)
	b := buf.FromBytes(storage)
	b.Advance(int32(offset))
	return b
}

// fragments splits data with the given sizes. A size of 0 creates an empty
// fragment. The last fragment holds the remaining bytes. An empty size list
// keeps all bytes in one fragment.
func fragments(data []byte, sizes ...int) [][]byte {
	result := make([][]byte, 0, len(sizes)+1)
	pos := 0
	for _, size := range sizes {
		if pos == len(data) {
			break
		}
		end := pos + size
		if end > len(data) {
			end = len(data)
		}
		result = append(result, data[pos:end])
		pos = end
	}
	if pos < len(data) || len(result) == 0 {
		result = append(result, data[pos:])
	}
	return result
}

// equalFragments splits data into fragments of at most size bytes.
func equalFragments(data []byte, size int) [][]byte {
	var result [][]byte
	for pos := 0; pos < len(data); pos += size {
		end := pos + size
		if end > len(data) {
			end = len(data)
		}
		result = append(result, data[pos:end])
	}
	return result
}

// snapshotMultiBuffer records the observable state of every element.
type bufferSnapshot struct {
	buffer    *buf.Buffer
	bytes     []byte
	length    int32
	capacity  int32
	available int32
	udp       *xnet.Destination
	udpValue  xnet.Destination
}

func snapshotMultiBuffer(mb buf.MultiBuffer) []bufferSnapshot {
	snapshots := make([]bufferSnapshot, len(mb))
	for i, b := range mb {
		snapshots[i].buffer = b
		if b == nil {
			continue
		}
		snapshots[i].bytes = append([]byte(nil), b.Bytes()...)
		snapshots[i].length = b.Len()
		snapshots[i].capacity = b.Cap()
		snapshots[i].available = b.Available()
		snapshots[i].udp = b.UDP
		if b.UDP != nil {
			snapshots[i].udpValue = *b.UDP
		}
	}
	return snapshots
}

// checkUnchanged fails when inspecting the input changed bytes, ranges, UDP
// metadata, order, or object identity.
func checkUnchanged(t *testing.T, mb buf.MultiBuffer, before []bufferSnapshot) {
	t.Helper()
	after := snapshotMultiBuffer(mb)
	if len(after) != len(before) {
		t.Fatalf("MultiBuffer has %d elements after inspection, want %d", len(after), len(before))
	}
	for i := range before {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Errorf("element %d changed after inspection", i)
		}
	}
}

// fragmentPattern builds one MultiBuffer layout for a test input.
type fragmentPattern struct {
	name        string
	maxBytes    int // 0 means no size limit
	multiBuffer func(data []byte) buf.MultiBuffer
}

func fragmentPatterns() []fragmentPattern {
	return []fragmentPattern{
		{
			name:        "contiguous",
			multiBuffer: func(data []byte) buf.MultiBuffer { return multiBufferOf(data) },
		},
		{
			name: "two-halves",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				return multiBufferOf(fragments(data, len(data)/2)...)
			},
		},
		{
			name: "header-byte-by-byte",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				return multiBufferOf(fragments(data, 1, 1, 1, 1, 1)...)
			},
		},
		{
			name: "payload-in-pieces",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				return multiBufferOf(fragments(data, 5, 1, 1, 1)...)
			},
		},
		{
			name: "empty-fragments",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				return multiBufferOf(fragments(data, 2, 0, 3, 0, 1, 0)...)
			},
		},
		{
			name:     "byte-at-a-time",
			maxBytes: 64,
			multiBuffer: func(data []byte) buf.MultiBuffer {
				sizes := make([]int, len(data))
				for i := range sizes {
					sizes[i] = 1
				}
				return multiBufferOf(fragments(data, sizes...)...)
			},
		},
		{
			name: "nonzero-offsets",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				chunks := fragments(data, 3, 1, 4)
				mb := make(buf.MultiBuffer, 0, len(chunks))
				for i, chunk := range chunks {
					mb = append(mb, offsetBuffer(chunk, i+1))
				}
				return mb
			},
		},
		{
			name: "released-empty-fragment",
			multiBuffer: func(data []byte) buf.MultiBuffer {
				mb := multiBufferOf(fragments(data, 2)...)
				emptied := buf.New()
				emptied.Release()
				return append(mb, emptied)
			},
		},
	}
}

// recordCases lists the inputs of the differential matrix. want is the result
// of the previous parser for every layout of the same bytes.
func recordCases() []struct {
	name string
	data []byte
	want bool
} {
	return []struct {
		name string
		data []byte
		want bool
	}{
		{name: "empty", data: nil, want: true},
		{name: "single-record", data: tlsRecord(1024), want: true},
		{name: "three-records", data: tlsRecords(1, 512, 4096), want: true},
		{name: "one-byte-payload", data: tlsRecord(1), want: true},
		{name: "max-payload", data: tlsRecord(65535), want: true},
		{name: "zero-length-record", data: []byte{0x17, 0x03, 0x03, 0x00, 0x00}, want: false},
		{name: "zero-length-record-then-record", data: append([]byte{0x17, 0x03, 0x03, 0x00, 0x00}, tlsRecord(4)...), want: false},
		{name: "max-payload-header", data: []byte{0x17, 0x03, 0x03, 0xff, 0xff}, want: false},
		{name: "truncated-header", data: []byte{0x17, 0x03, 0x03, 0x00}, want: false},
		{name: "truncated-payload", data: tlsRecord(256)[:5+100], want: false},
		{name: "invalid-marker", data: []byte{0x16, 0x03, 0x03, 0x00, 0x01, 0x41}, want: false},
		{name: "invalid-version", data: []byte{0x17, 0x03, 0x02, 0x00, 0x01, 0x41}, want: false},
		{name: "trailing-partial-header", data: append(tlsRecord(8), 0x17), want: false},
		{name: "trailing-garbage", data: append(tlsRecord(8), 0x00), want: false},
		{name: "trailing-invalid-marker", data: append(tlsRecord(8), 0x16, 0x03, 0x03, 0x00, 0x00), want: false},
		{name: "trailing-truncated-payload", data: append(tlsRecord(8), tlsRecord(64)[:5+8]...), want: false},
	}
}

func TestIsCompleteRecordMatchesReference(t *testing.T) {
	patterns := fragmentPatterns()
	for _, testCase := range recordCases() {
		for _, pattern := range patterns {
			if pattern.maxBytes > 0 && len(testCase.data) > pattern.maxBytes {
				continue
			}
			t.Run(testCase.name+"/"+pattern.name, func(t *testing.T) {
				mb := pattern.multiBuffer(testCase.data)
				before := snapshotMultiBuffer(mb)

				got := runIsCompleteRecord(IsCompleteRecord, mb)
				want := runIsCompleteRecord(isCompleteRecordReference, mb)

				if got.recovered != nil {
					t.Fatalf("IsCompleteRecord panicked on a layout without nil elements: %v", got.recovered)
				}
				if want.recovered != nil {
					t.Fatalf("reference parser panicked: %v", want.recovered)
				}
				if got.complete != want.complete {
					t.Errorf("IsCompleteRecord = %v, reference = %v", got.complete, want.complete)
				}
				if got.complete != testCase.want {
					t.Errorf("IsCompleteRecord = %v, want %v", got.complete, testCase.want)
				}
				checkUnchanged(t, mb, before)
			})
		}
	}
}

func TestIsCompleteRecordRejectsNilElements(t *testing.T) {
	released := buf.New()
	released.Release()
	testCases := []struct {
		name      string
		mb        buf.MultiBuffer
		wantPanic bool
		want      bool
	}{
		{name: "nil-multibuffer", mb: nil, want: true},
		{name: "empty-multibuffer", mb: buf.MultiBuffer{}, want: true},
		{name: "empty-buffers-only", mb: multiBufferOf(nil, nil), want: true},
		{name: "released-buffer-only", mb: buf.MultiBuffer{released}, want: true},
		{name: "nil-element-only", mb: buf.MultiBuffer{nil}, wantPanic: true},
		{name: "nil-elements-only", mb: buf.MultiBuffer{nil, nil}, wantPanic: true},
		{name: "nil-element-first", mb: append(buf.MultiBuffer{nil}, multiBufferOf(tlsRecord(8))...), wantPanic: true},
		{name: "nil-element-last", mb: append(multiBufferOf(tlsRecord(8)), nil), wantPanic: true},
		{name: "nil-element-between-records", mb: append(append(multiBufferOf(tlsRecord(8)), nil), multiBufferOf(tlsRecord(8))...), wantPanic: true},
		{name: "nil-element-after-invalid-prefix", mb: append(multiBufferOf([]byte{0x00, 0x01, 0x02, 0x03, 0x04}), nil), wantPanic: true},
		{name: "nil-element-after-empty-view", mb: append(append(buf.MultiBuffer{}, multiBufferOf(nil)...), nil), wantPanic: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := runIsCompleteRecord(IsCompleteRecord, testCase.mb)
			want := runIsCompleteRecord(isCompleteRecordReference, testCase.mb)

			if (got.recovered != nil) != (want.recovered != nil) {
				t.Fatalf("panic mismatch: IsCompleteRecord recovered %v, reference recovered %v", got.recovered, want.recovered)
			}
			if testCase.wantPanic {
				if got.recovered == nil {
					t.Fatal("IsCompleteRecord did not panic for a nil element")
				}
				return
			}
			if got.recovered != nil {
				t.Fatalf("IsCompleteRecord panicked: %v", got.recovered)
			}
			if got.complete != testCase.want {
				t.Errorf("IsCompleteRecord = %v, want %v", got.complete, testCase.want)
			}
		})
	}
}

func TestIsCompleteRecordPreservesBufferMetadata(t *testing.T) {
	record := tlsRecord(600)
	first := buf.New()
	first.Write(record[:300])
	first.UDP = &xnet.Destination{Network: xnet.Network_UDP, Address: xnet.ParseAddress("127.0.0.1"), Port: 5353}
	mb := buf.MultiBuffer{first, offsetBuffer(record[300:], 7), buf.New()}
	defer buf.ReleaseMulti(mb)

	before := snapshotMultiBuffer(mb)
	if !IsCompleteRecord(mb) {
		t.Fatal("IsCompleteRecord = false, want true")
	}
	checkUnchanged(t, mb, before)
	if first.UDP == nil || first.UDP.Port != 5353 {
		t.Errorf("UDP metadata = %v, want port 5353", first.UDP)
	}
}

// fragmentMultiBuffer splits data into at most maxFragments views. A size of 0
// creates an empty view. The layout is deterministic for a given seed.
func fragmentMultiBuffer(data []byte, seed uint64, maxFragments int) buf.MultiBuffer {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	mb := make(buf.MultiBuffer, 0, maxFragments)
	pos := 0
	for pos < len(data) && len(mb) < maxFragments {
		size := rng.IntN(64)
		if size == 0 {
			mb = append(mb, buf.FromBytes(nil))
			continue
		}
		if size > len(data)-pos {
			size = len(data) - pos
		}
		mb = append(mb, buf.FromBytes(data[pos:pos+size]))
		pos += size
	}
	if pos < len(data) {
		mb = append(mb, buf.FromBytes(data[pos:]))
	}
	return mb
}

// constructedRecords builds a small batch of valid TLS records. The record
// count and the payload lengths come from seed, so the fuzzer explores valid
// records next to arbitrary bytes. A payload is always at least one byte,
// because the record parser rejects zero-length records. The total size stays
// below 4 KiB.
func constructedRecords(data []byte, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 0x2545f4914f6cdd1d))
	var records []byte
	for count := 1 + rng.IntN(4); count > 0; count-- {
		payloadLen := 1 + rng.IntN(1023)
		records = append(records, 0x17, 0x03, 0x03, byte(payloadLen>>8), byte(payloadLen))
		for i := 0; i < payloadLen; i++ {
			payloadByte := byte(i)
			if len(data) > 0 {
				payloadByte = data[int((seed+uint64(i))%uint64(len(data)))]
			}
			records = append(records, payloadByte)
		}
	}
	return records
}

// checkIsCompleteRecordLayout compares both parsers on one MultiBuffer layout.
// When wantComplete is true, the layout must hold only complete records. The
// check also verifies that neither parser changed the input and that a nil
// element panics in both parsers.
func checkIsCompleteRecordLayout(t *testing.T, mb buf.MultiBuffer, wantComplete bool, nilAt uint16) {
	t.Helper()
	before := snapshotMultiBuffer(mb)

	got := runIsCompleteRecord(IsCompleteRecord, mb)
	want := runIsCompleteRecord(isCompleteRecordReference, mb)
	if got.recovered != nil || want.recovered != nil {
		t.Fatalf("unexpected panic: IsCompleteRecord recovered %v, reference recovered %v", got.recovered, want.recovered)
	}
	if got.complete != want.complete {
		t.Fatalf("IsCompleteRecord = %v, reference = %v", got.complete, want.complete)
	}
	if wantComplete && !got.complete {
		t.Fatalf("IsCompleteRecord = false for a layout of complete records")
	}
	checkUnchanged(t, mb, before)

	// The previous parser copied every element before it parsed, so any nil
	// element panicked before the record check. Both parsers must keep that.
	nilIndex := int(nilAt) % (len(mb) + 1)
	withNil := make(buf.MultiBuffer, 0, len(mb)+1)
	withNil = append(withNil, mb[:nilIndex]...)
	withNil = append(withNil, nil)
	withNil = append(withNil, mb[nilIndex:]...)

	if got := runIsCompleteRecord(IsCompleteRecord, withNil); got.recovered == nil {
		t.Fatalf("IsCompleteRecord did not panic for a nil element at index %d", nilIndex)
	}
	if want := runIsCompleteRecord(isCompleteRecordReference, withNil); want.recovered == nil {
		t.Fatalf("reference parser did not panic for a nil element at index %d", nilIndex)
	}
}

func FuzzIsCompleteRecord(f *testing.F) {
	seeds := []struct {
		data  []byte
		seed  uint64
		nilAt uint16
	}{
		{data: nil, seed: 1},
		{data: []byte{}, seed: 2, nilAt: 1},
		{data: tlsRecord(1), seed: 3},
		{data: tlsRecord(17), seed: 4, nilAt: 2},
		{data: tlsRecord(512), seed: 5, nilAt: 3},
		{data: tlsRecords(1, 2, 40, 300), seed: 6, nilAt: 4},
		{data: tlsRecords(1024, 1024, 1024), seed: 7, nilAt: 5},
		{data: tlsRecords(1024, 1024, 1024), seed: 8},
		{data: tlsRecords(1024, 1024, 1024), seed: 9},
		{data: tlsRecord(16384), seed: 10, nilAt: 6},
		{data: tlsRecord(1024)[:5+9], seed: 11, nilAt: 7},
		{data: tlsRecord(1024)[:4], seed: 12},
		{data: []byte{0x16, 0x03, 0x03, 0x00, 0x01, 0x41}, seed: 13},
		{data: []byte{0x17, 0x03, 0x02, 0x00, 0x01, 0x41}, seed: 14},
		{data: []byte{0x17, 0x03, 0x03, 0x00, 0x00}, seed: 15, nilAt: 1},
		{data: []byte("0"), seed: 18, nilAt: 1},
	}
	for _, seed := range seeds {
		f.Add(seed.data, seed.seed, seed.nilAt)
	}

	f.Fuzz(func(t *testing.T, data []byte, seed uint64, nilAt uint16) {
		if len(data) > 32*1024 {
			t.Skip("bounded input")
		}
		checkIsCompleteRecordLayout(t, fragmentMultiBuffer(data, seed, 64), false, nilAt)

		// Valid records must stay complete whatever the fragment sizes are.
		mb := fragmentMultiBuffer(constructedRecords(data, seed), seed^0x5bf03635, 64)
		checkIsCompleteRecordLayout(t, mb, true, nilAt)
	})
}

// recordingWriter keeps a copy of every buffer written to it.
type recordingWriter struct {
	blocks [][]byte
}

func (w *recordingWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	for _, b := range mb {
		if b == nil {
			continue
		}
		w.blocks = append(w.blocks, append([]byte(nil), b.Bytes()...))
	}
	buf.ReleaseMulti(mb)
	return nil
}

// recordingConn is a net.Conn that stores the bytes written to it.
type recordingConn struct {
	written bytes.Buffer
}

func (c *recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Write(p []byte) (int, error)      { return c.written.Write(p) }
func (c *recordingConn) Close() error                     { return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return nil }
func (c *recordingConn) RemoteAddr() net.Addr             { return nil }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

// visionPaddingBlock is one decoded Vision padding block.
type visionPaddingBlock struct {
	command      byte
	content      []byte
	paddingBytes int
}

// decodeVisionPaddingBlock decodes one block written by VisionWriter. The first
// block of a writer instance carries the 16-byte user UUID; later blocks do not.
func decodeVisionPaddingBlock(block []byte, hasUUID bool) (visionPaddingBlock, error) {
	prefix := 0
	if hasUUID {
		prefix = 16
	}
	if len(block) < prefix+5 {
		return visionPaddingBlock{}, fmt.Errorf("padding block has %d bytes, want at least %d", len(block), prefix+5)
	}
	contentLen := int(block[prefix+1])<<8 | int(block[prefix+2])
	paddingLen := int(block[prefix+3])<<8 | int(block[prefix+4])
	if len(block) < prefix+5+contentLen+paddingLen {
		return visionPaddingBlock{}, fmt.Errorf("padding block has %d bytes, want %d", len(block), prefix+5+contentLen+paddingLen)
	}
	return visionPaddingBlock{
		command:      block[prefix],
		content:      block[prefix+5 : prefix+5+contentLen],
		paddingBytes: paddingLen,
	}, nil
}

// decodeVisionPaddingBlocks decodes the blocks of one writer instance.
func decodeVisionPaddingBlocks(t *testing.T, blocks [][]byte) []visionPaddingBlock {
	t.Helper()
	decoded := make([]visionPaddingBlock, 0, len(blocks))
	for i, block := range blocks {
		decodedBlock, err := decodeVisionPaddingBlock(block, i == 0)
		if err != nil {
			t.Fatalf("padding block %d: %v", i, err)
		}
		decoded = append(decoded, decodedBlock)
	}
	return decoded
}

// newVisionTestState returns a traffic state that reaches the padding path.
func newVisionTestState(enableXtls bool) *TrafficState {
	state := NewTrafficState(bytes.Repeat([]byte{0x5a}, 16))
	state.EnableXtls = enableXtls
	state.IsTLS = true
	state.IsTLS12orAbove = true
	state.NumberOfPacketToFilter = 0
	return state
}

// isPaddingState returns the padding flag of the active direction.
func isPaddingState(state *TrafficState, isUplink bool) bool {
	if isUplink {
		return state.Outbound.IsPadding
	}
	return state.Inbound.IsPadding
}

// directCopyState returns the writer direct-copy flag of the active direction.
func directCopyState(state *TrafficState, isUplink bool) bool {
	if isUplink {
		return state.Outbound.UplinkWriterDirectCopy
	}
	return state.Inbound.DownlinkWriterDirectCopy
}

func TestVisionWriterPaddingDecisions(t *testing.T) {
	record := tlsRecord(1024)
	incomplete := record[:5+512]
	testCases := []struct {
		name       string
		data       []byte
		enableXtls bool
	}{
		{name: "complete-record/xtls-on", data: record, enableXtls: true},
		{name: "complete-record/xtls-off", data: record},
		{name: "incomplete-record/xtls-on", data: incomplete, enableXtls: true},
		{name: "incomplete-record/xtls-off", data: incomplete},
	}
	for _, testCase := range testCases {
		for _, isUplink := range []bool{false, true} {
			direction := "downlink"
			if isUplink {
				direction = "uplink"
			}
			t.Run(testCase.name+"/"+direction, func(t *testing.T) {
				// The padding decision must follow the reference parser result.
				complete := isCompleteRecordReference(multiBufferOf(testCase.data))
				wantCommand := CommandPaddingContinue
				switch {
				case complete && testCase.enableXtls:
					wantCommand = CommandPaddingDirect
				case complete:
					wantCommand = CommandPaddingEnd
				}
				wantDirect := complete && testCase.enableXtls

				state := newVisionTestState(testCase.enableXtls)
				recorder := &recordingWriter{}
				conn := &recordingConn{}
				writer := NewVisionWriter(recorder, state, isUplink, context.Background(), conn, nil, nil)

				if err := writer.WriteMultiBuffer(multiBufferOf(testCase.data)); err != nil {
					t.Fatalf("WriteMultiBuffer: %v", err)
				}

				if got := isPaddingState(state, isUplink); got != !complete {
					t.Errorf("IsPadding = %v, want %v", got, !complete)
				}
				if got := directCopyState(state, isUplink); got != wantDirect {
					t.Errorf("direct copy flag = %v, want %v", got, wantDirect)
				}

				blocks := decodeVisionPaddingBlocks(t, recorder.blocks)
				if len(blocks) != 1 {
					t.Fatalf("writer wrote %d padding blocks, want 1", len(blocks))
				}
				if blocks[0].command != wantCommand {
					t.Errorf("command = %d, want %d", blocks[0].command, wantCommand)
				}
				if !bytes.Equal(blocks[0].content, testCase.data) {
					t.Errorf("decoded content has %d bytes, want %d", len(blocks[0].content), len(testCase.data))
				}

				// The next write must follow the padding state of the first.
				if err := writer.WriteMultiBuffer(multiBufferOf(testCase.data)); err != nil {
					t.Fatalf("second WriteMultiBuffer: %v", err)
				}
				switch {
				case wantDirect:
					if got := conn.written.Bytes(); !bytes.Equal(got, testCase.data) {
						t.Errorf("direct copy wrote %d bytes, want the %d input bytes", len(got), len(testCase.data))
					}
					if len(recorder.blocks) != 1 {
						t.Errorf("padding writer received %d writes, want 1", len(recorder.blocks))
					}
				case complete:
					if len(recorder.blocks) != 2 {
						t.Fatalf("padding writer received %d writes, want 2", len(recorder.blocks))
					}
					if !bytes.Equal(recorder.blocks[1], testCase.data) {
						t.Errorf("unpadded write has %d bytes, want the %d input bytes", len(recorder.blocks[1]), len(testCase.data))
					}
					if conn.written.Len() != 0 {
						t.Errorf("connection received %d bytes, want 0", conn.written.Len())
					}
				default:
					if len(recorder.blocks) != 2 {
						t.Fatalf("padding writer received %d writes, want 2", len(recorder.blocks))
					}
					second, err := decodeVisionPaddingBlock(recorder.blocks[1], false)
					if err != nil {
						t.Fatalf("second padding block: %v", err)
					}
					if second.command != CommandPaddingContinue {
						t.Errorf("second command = %d, want %d", second.command, CommandPaddingContinue)
					}
					if !bytes.Equal(second.content, testCase.data) {
						t.Errorf("second decoded content has %d bytes, want %d", len(second.content), len(testCase.data))
					}
					if conn.written.Len() != 0 {
						t.Errorf("connection received %d bytes, want 0", conn.written.Len())
					}
				}
			})
		}
	}
}

func TestVisionWriterReshapeKeepsRecordDecision(t *testing.T) {
	// A buffer of at least buf.Size-21 bytes triggers ReshapeMultiBuffer.
	record := tlsRecord(buf.Size - 21)
	if complete := isCompleteRecordReference(multiBufferOf(record)); !complete {
		t.Fatalf("reference parser reports an incomplete record for %d bytes", len(record))
	}
	state := newVisionTestState(false)
	recorder := &recordingWriter{}
	conn := &recordingConn{}
	writer := NewVisionWriter(recorder, state, false, context.Background(), conn, nil, nil)

	if err := writer.WriteMultiBuffer(multiBufferOf(record)); err != nil {
		t.Fatalf("WriteMultiBuffer: %v", err)
	}

	blocks := decodeVisionPaddingBlocks(t, recorder.blocks)
	if len(blocks) != 2 {
		t.Fatalf("writer wrote %d padding blocks, want 2 after reshaping", len(blocks))
	}
	if blocks[0].command != CommandPaddingContinue {
		t.Errorf("first command = %d, want %d", blocks[0].command, CommandPaddingContinue)
	}
	if blocks[1].command != CommandPaddingEnd {
		t.Errorf("second command = %d, want %d", blocks[1].command, CommandPaddingEnd)
	}
	var content []byte
	for _, block := range blocks {
		content = append(content, block.content...)
	}
	if !bytes.Equal(content, record) {
		t.Errorf("decoded content has %d bytes, want %d", len(content), len(record))
	}
	if isPaddingState(state, false) {
		t.Error("IsPadding = true after a complete record")
	}
	if directCopyState(state, false) {
		t.Error("direct copy flag = true with Vision disabled")
	}

	if err := writer.WriteMultiBuffer(multiBufferOf(record)); err != nil {
		t.Fatalf("second WriteMultiBuffer: %v", err)
	}
	if len(recorder.blocks) != 3 {
		t.Fatalf("padding writer received %d writes, want 3", len(recorder.blocks))
	}
	if !bytes.Equal(recorder.blocks[2], record) {
		t.Errorf("unpadded write has %d bytes, want the %d input bytes", len(recorder.blocks[2]), len(record))
	}
}

// isCompleteRecordBenchmarkCase holds one prepared benchmark input.
type isCompleteRecordBenchmarkCase struct {
	name         string
	multiBuffer  buf.MultiBuffer
	wantComplete bool
}

// repeatedPayloadLengths returns count copies of one payload length.
func repeatedPayloadLengths(payloadLen, count int) []int {
	payloadLens := make([]int, count)
	for i := range payloadLens {
		payloadLens[i] = payloadLen
	}
	return payloadLens
}

func isCompleteRecordBenchmarkCases() []isCompleteRecordBenchmarkCase {
	singleRecord := tlsRecord(1024)
	multiRecords := tlsRecords(repeatedPayloadLengths(1024, 64)...)
	largeRecords := tlsRecords(repeatedPayloadLengths(4096, 64)...)
	return []isCompleteRecordBenchmarkCase{
		{name: "contiguous/single-1KiB", multiBuffer: multiBufferOf(singleRecord), wantComplete: true},
		{name: "fragmented/single-1KiB-header-5-way", multiBuffer: multiBufferOf(fragments(singleRecord, 1, 1, 1, 1, 1)...), wantComplete: true},
		{name: "contiguous/multi-64x1KiB", multiBuffer: multiBufferOf(equalFragments(multiRecords, buf.Size)...), wantComplete: true},
		{name: "fragmented/multi-64x1KiB-512B-views", multiBuffer: multiBufferOf(equalFragments(multiRecords, 512)...), wantComplete: true},
		{name: "contiguous/multi-64x4KiB", multiBuffer: multiBufferOf(equalFragments(largeRecords, buf.Size)...), wantComplete: true},
		{name: "truncated/multi-64x1KiB-tail", multiBuffer: multiBufferOf(equalFragments(multiRecords[:len(multiRecords)-1], buf.Size)...), wantComplete: false},
	}
}

func BenchmarkIsCompleteRecord(b *testing.B) {
	for _, testCase := range isCompleteRecordBenchmarkCases() {
		b.Run(testCase.name, func(b *testing.B) {
			var complete bool
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				complete = IsCompleteRecord(testCase.multiBuffer)
			}
			b.StopTimer()
			if complete != testCase.wantComplete {
				b.Fatalf("IsCompleteRecord = %v, want %v", complete, testCase.wantComplete)
			}
		})
	}
}
