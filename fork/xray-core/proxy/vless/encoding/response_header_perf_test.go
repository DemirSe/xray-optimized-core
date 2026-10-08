package encoding_test

// N3: EncodeResponseHeader writes the common version-zero no-addon response
// from a shared read-only two-byte sequence. This file compares the fast path
// with the exact HEAD encoder kept as a test-only reference. It covers
// versions 0, 1, and 255, empty, unknown, and Vision addons, nil arguments,
// writer failures, short writes, the actual buf.BufferedWriter caller, and
// concurrent shared-byte use. BenchmarkEncodeResponseHeaderEmpty measures the
// paired baseline and candidate fixtures.

import (
	"bytes"
	stdErrors "errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	xerrors "github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/vless"
	. "github.com/xtls/xray-core/proxy/vless/encoding"
)

// encWriteFunc is one response-header encoder under test: the legacy HEAD
// reference or the current production function.
type encWriteFunc func(writer io.Writer, request *protocol.RequestHeader, responseAddons *Addons) error

// encLegacyEncodeResponseHeader is the exact EncodeResponseHeader body from
// HEAD 1dfcee33f592d9cf055dd603a2e071a920b03ad7, kept as the test baseline.
// Only the receiver and the local import aliases differ from the source.
func encLegacyEncodeResponseHeader(writer io.Writer, request *protocol.RequestHeader, responseAddons *Addons) error {
	buffer := buf.StackNew()
	defer buffer.Release()

	if err := buffer.WriteByte(request.Version); err != nil {
		return xerrors.New("failed to write response version").Base(err)
	}

	if err := EncodeHeaderAddons(&buffer, responseAddons); err != nil {
		return xerrors.New("failed to encode response header addons").Base(err)
	}

	if _, err := writer.Write(buffer.Bytes()); err != nil {
		return xerrors.New("failed to write response header").Base(err)
	}

	return nil
}

// encRecordingWriter records every Write call and returns the configured
// result. A negative n reports len(p).
type encRecordingWriter struct {
	calls   int
	payload []byte
	n       int
	err     error
}

func (w *encRecordingWriter) Write(p []byte) (int, error) {
	w.calls++
	w.payload = append(w.payload, p...)
	if w.n < 0 {
		return len(p), w.err
	}
	return w.n, w.err
}

// encWrapperText removes the caller package prefix that common/errors adds at
// the errors.New call site. The test-only reference lives in encoding_test, so
// only that prefix differs from the production package.
func encWrapperText(err error) string {
	if index := strings.Index(err.Error(), ": "); index >= 0 {
		return err.Error()[index+2:]
	}
	return err.Error()
}

// encPanicMessage runs encode and returns the recovered panic text.
func encPanicMessage(t *testing.T, encode func(io.Writer) error) (message string) {
	t.Helper()
	writer := &encRecordingWriter{n: -1}
	defer func() {
		recovered := recover()
		if recovered == nil {
			message = ""
			return
		}
		message = fmt.Sprint(recovered)
	}()
	_ = encode(writer)
	return ""
}

func TestEncodeResponseHeaderMatchesLegacy(t *testing.T) {
	testCases := []struct {
		name    string
		version byte
		addons  *Addons
	}{
		{name: "version0-empty-addons", version: 0, addons: &Addons{}},
		{name: "version0-unknown-flow", version: 0, addons: &Addons{Flow: "xtls-rprx-direct"}},
		{name: "version1-empty-addons", version: 1, addons: &Addons{}},
		{name: "version255-empty-addons", version: 255, addons: &Addons{}},
		{name: "version0-vision-addons", version: 0, addons: &Addons{Flow: vless.XRV}},
		{name: "version255-vision-addons", version: 255, addons: &Addons{Flow: vless.XRV}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := &protocol.RequestHeader{Version: testCase.version}
			baselineWriter := &encRecordingWriter{n: -1}
			candidateWriter := &encRecordingWriter{n: -1}
			if err := encLegacyEncodeResponseHeader(baselineWriter, request, testCase.addons); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			if err := EncodeResponseHeader(candidateWriter, request, testCase.addons); err != nil {
				t.Fatalf("candidate: %v", err)
			}
			if baselineWriter.calls != 1 || candidateWriter.calls != 1 {
				t.Fatalf("writer calls = %d/%d, want 1/1", baselineWriter.calls, candidateWriter.calls)
			}
			if !bytes.Equal(candidateWriter.payload, baselineWriter.payload) {
				t.Fatalf("candidate bytes = %v, want the baseline bytes %v", candidateWriter.payload, baselineWriter.payload)
			}
			if len(candidateWriter.payload) == 0 || candidateWriter.payload[0] != testCase.version {
				t.Fatalf("candidate bytes = %v, want the version byte %d first", candidateWriter.payload, testCase.version)
			}
			if testCase.version == 0 && testCase.addons.Flow != vless.XRV && !bytes.Equal(candidateWriter.payload, []byte{0, 0}) {
				t.Fatalf("no-addon header = %v, want two zero bytes", candidateWriter.payload)
			}
		})
	}
}

func TestEncodeResponseHeaderNilArguments(t *testing.T) {
	testCases := []struct {
		name    string
		request *protocol.RequestHeader
		addons  *Addons
	}{
		{name: "nil-request", request: nil, addons: &Addons{}},
		{name: "nil-addons-version0", request: &protocol.RequestHeader{Version: 0}, addons: nil},
		{name: "nil-addons-version1", request: &protocol.RequestHeader{Version: 1}, addons: nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			baselineMessage := encPanicMessage(t, func(writer io.Writer) error {
				return encLegacyEncodeResponseHeader(writer, testCase.request, testCase.addons)
			})
			candidateMessage := encPanicMessage(t, func(writer io.Writer) error {
				return EncodeResponseHeader(writer, testCase.request, testCase.addons)
			})
			if baselineMessage == "" || candidateMessage == "" {
				t.Fatalf("panic messages = %q/%q, want a panic on both paths", baselineMessage, candidateMessage)
			}
			if baselineMessage != candidateMessage {
				t.Fatalf("panic messages differ: candidate %q, baseline %q", candidateMessage, baselineMessage)
			}
		})
	}
}

func TestEncodeResponseHeaderWriterErrors(t *testing.T) {
	sentinel := stdErrors.New("fixture write error")
	testCases := []struct {
		name string
		n    int
	}{
		{name: "no-bytes", n: 0},
		{name: "one-byte", n: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := &protocol.RequestHeader{Version: 0}
			addons := &Addons{}
			baselineWriter := &encRecordingWriter{n: testCase.n, err: sentinel}
			candidateWriter := &encRecordingWriter{n: testCase.n, err: sentinel}
			baselineErr := encLegacyEncodeResponseHeader(baselineWriter, request, addons)
			candidateErr := EncodeResponseHeader(candidateWriter, request, addons)
			if baselineErr == nil || candidateErr == nil {
				t.Fatalf("errors = %v/%v, want an error on both paths", candidateErr, baselineErr)
			}
			if encWrapperText(candidateErr) != encWrapperText(baselineErr) {
				t.Fatalf("wrappers differ: candidate %q, baseline %q", encWrapperText(candidateErr), encWrapperText(baselineErr))
			}
			if got := xerrors.Cause(candidateErr); got != sentinel {
				t.Fatalf("candidate cause = %#v, want the fixture error", got)
			}
			if got := xerrors.Cause(baselineErr); got != sentinel {
				t.Fatalf("baseline cause = %#v, want the fixture error", got)
			}
			if baselineWriter.calls != 1 || candidateWriter.calls != 1 {
				t.Fatalf("writer calls = %d/%d, want 1/1", baselineWriter.calls, candidateWriter.calls)
			}
			if !bytes.Equal(candidateWriter.payload, baselineWriter.payload) {
				t.Fatalf("written bytes differ: candidate %v, baseline %v", candidateWriter.payload, baselineWriter.payload)
			}
		})
	}
}

func TestEncodeResponseHeaderShortWrite(t *testing.T) {
	testCases := []struct {
		name string
		n    int
	}{
		{name: "one-of-two", n: 1},
		{name: "zero", n: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := &protocol.RequestHeader{Version: 0}
			addons := &Addons{}
			baselineWriter := &encRecordingWriter{n: testCase.n}
			candidateWriter := &encRecordingWriter{n: testCase.n}
			if err := encLegacyEncodeResponseHeader(baselineWriter, request, addons); err != nil {
				t.Fatalf("baseline short-write error = %v, want nil", err)
			}
			if err := EncodeResponseHeader(candidateWriter, request, addons); err != nil {
				t.Fatalf("candidate short-write error = %v, want nil", err)
			}
			if baselineWriter.calls != 1 || candidateWriter.calls != 1 {
				t.Fatalf("writer calls = %d/%d, want 1/1", baselineWriter.calls, candidateWriter.calls)
			}
			if !bytes.Equal(candidateWriter.payload, []byte{0, 0}) {
				t.Fatalf("offered bytes = %v, want the two header bytes", candidateWriter.payload)
			}
		})
	}
}

func TestEncodeResponseHeaderBufferedWriter(t *testing.T) {
	body := []byte{0xde, 0xad, 0xbe, 0xef}
	testCases := []struct {
		name   string
		encode encWriteFunc
	}{
		{name: "baseline-pooled", encode: encLegacyEncodeResponseHeader},
		{name: "candidate-fastpath", encode: EncodeResponseHeader},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sink := &encRecordingWriter{n: -1}
			writer := buf.NewBufferedWriter(buf.NewWriter(sink))
			request := &protocol.RequestHeader{Version: 0}
			if err := testCase.encode(writer, request, &Addons{}); err != nil {
				t.Fatalf("encode header: %v", err)
			}
			if sink.calls != 0 {
				t.Fatalf("sink calls = %d, want the header still buffered", sink.calls)
			}
			writer.SetFlushNext()
			if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(body)}); err != nil {
				t.Fatalf("write body: %v", err)
			}
			if sink.calls != 1 {
				t.Fatalf("sink calls = %d, want one flush", sink.calls)
			}
			want := append([]byte{0, 0}, body...)
			if !bytes.Equal(sink.payload, want) {
				t.Fatalf("flushed bytes = %v, want header then body %v", sink.payload, want)
			}
			if err := writer.Flush(); err != nil {
				t.Fatalf("final flush: %v", err)
			}
		})
	}
}

func TestEncodeResponseHeaderSharedBytes(t *testing.T) {
	t.Run("heavy-pool-use", func(t *testing.T) {
		fill := bytes.Repeat([]byte{0x5a}, buf.Size)
		for i := 0; i < 1000; i++ {
			recycled := buf.StackNew()
			if _, err := recycled.Write(fill); err != nil {
				t.Fatalf("fill pooled buffer: %v", err)
			}
			recycled.Release()
		}
		writer := &encRecordingWriter{n: -1}
		if err := EncodeResponseHeader(writer, &protocol.RequestHeader{Version: 0}, &Addons{}); err != nil {
			t.Fatalf("encode after pool use: %v", err)
		}
		if !bytes.Equal(writer.payload, []byte{0, 0}) {
			t.Fatalf("bytes after pool use = %v, want two zero bytes", writer.payload)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		const workers = 8
		const iterations = 64
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				request := &protocol.RequestHeader{Version: 0}
				for i := 0; i < iterations; i++ {
					recycled := buf.StackNew()
					if _, err := recycled.Write([]byte{0x5a, 0xa5}); err != nil {
						t.Errorf("fill pooled buffer: %v", err)
						return
					}
					recycled.Release()
					writer := &encRecordingWriter{n: -1}
					if err := EncodeResponseHeader(writer, request, &Addons{}); err != nil {
						t.Errorf("encode: %v", err)
						return
					}
					if !bytes.Equal(writer.payload, []byte{0, 0}) {
						t.Errorf("bytes = %v, want two zero bytes", writer.payload)
						return
					}
				}
			}()
		}
		wg.Wait()
	})
}

func BenchmarkEncodeResponseHeaderEmpty(b *testing.B) {
	request := &protocol.RequestHeader{Version: 0}
	versionOneRequest := &protocol.RequestHeader{Version: 1}
	emptyAddons := &Addons{}
	visionAddons := &Addons{Flow: vless.XRV}

	b.Run("controlled-writer/baseline-pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := encLegacyEncodeResponseHeader(io.Discard, request, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
	b.Run("controlled-writer/candidate-fastpath", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := EncodeResponseHeader(io.Discard, request, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
	b.Run("actual-buffered-writer/baseline-pooled", func(b *testing.B) {
		writer := buf.NewBufferedWriter(buf.NewWriter(io.Discard))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := encLegacyEncodeResponseHeader(writer, request, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
			if err := writer.Flush(); err != nil {
				b.Fatalf("flush: %v", err)
			}
		}
		b.StopTimer()
	})
	b.Run("actual-buffered-writer/candidate-fastpath", func(b *testing.B) {
		writer := buf.NewBufferedWriter(buf.NewWriter(io.Discard))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := EncodeResponseHeader(writer, request, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
			if err := writer.Flush(); err != nil {
				b.Fatalf("flush: %v", err)
			}
		}
		b.StopTimer()
	})
	b.Run("fallback-vision-addons/baseline-pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := encLegacyEncodeResponseHeader(io.Discard, request, visionAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
	b.Run("fallback-vision-addons/candidate-slow-path", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := EncodeResponseHeader(io.Discard, request, visionAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
	b.Run("fallback-version1/baseline-pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := encLegacyEncodeResponseHeader(io.Discard, versionOneRequest, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
	b.Run("fallback-version1/candidate-slow-path", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := EncodeResponseHeader(io.Discard, versionOneRequest, emptyAddons); err != nil {
				b.Fatalf("encode: %v", err)
			}
		}
	})
}
