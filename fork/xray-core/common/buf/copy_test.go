package buf_test

import (
	"crypto/rand"
	"io"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
)

// ponytail: local fakes replace deleted testing/mocks Reader/Writer.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("error")
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("error")
}

func TestReadError(t *testing.T) {
	err := buf.Copy(buf.NewReader(errReader{}), buf.Discard)
	if err == nil {
		t.Fatal("expected error, but nil")
	}

	if !buf.IsReadError(err) {
		t.Error("expected to be ReadError, but not")
	}

	if err.Error() != "common/buf_test: error" {
		t.Fatal("unexpected error message: ", err.Error())
	}
}

func TestWriteError(t *testing.T) {
	err := buf.Copy(buf.NewReader(rand.Reader), buf.NewWriter(errWriter{}))
	if err == nil {
		t.Fatal("expected error, but nil")
	}

	if !buf.IsWriteError(err) {
		t.Error("expected to be WriteError, but not")
	}

	if err.Error() != "common/buf_test: error" {
		t.Fatal("unexpected error message: ", err.Error())
	}
}

type TestReader struct{}

func (TestReader) Read(b []byte) (int, error) {
	return len(b), nil
}

func BenchmarkCopy(b *testing.B) {
	reader := buf.NewReader(io.LimitReader(TestReader{}, 10240))
	writer := buf.Discard

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buf.Copy(reader, writer)
	}
}
