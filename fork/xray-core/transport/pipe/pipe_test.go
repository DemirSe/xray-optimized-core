package pipe_test

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sync/errgroup"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	. "github.com/xtls/xray-core/transport/pipe"
)

func TestPipeReadWrite(t *testing.T) {
	pReader, pWriter := New(1024, false)

	b := buf.New()
	b.WriteString("abcd")
	if err := common.Must(pWriter.WriteMultiBuffer(buf.MultiBuffer{b})); err != nil {
		t.Fatal(err)
	}

	b2 := buf.New()
	b2.WriteString("efg")
	if err := common.Must(pWriter.WriteMultiBuffer(buf.MultiBuffer{b2})); err != nil {
		t.Fatal(err)
	}

	rb, err := pReader.ReadMultiBuffer()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if r := cmp.Diff(rb.String(), "abcdefg"); r != "" {
		t.Error(r)
	}
}

func TestPipeInterrupt(t *testing.T) {
	pReader, pWriter := New(1024, false)
	payload := []byte{'a', 'b', 'c', 'd'}
	b := buf.New()
	b.Write(payload)
	if err := common.Must(pWriter.WriteMultiBuffer(buf.MultiBuffer{b})); err != nil {
		t.Fatal(err)
	}
	pWriter.Interrupt()

	rb, err := pReader.ReadMultiBuffer()
	if err != io.ErrClosedPipe {
		t.Fatal("expect io.ErrClosePipe, but got ", err)
	}
	if !rb.IsEmpty() {
		t.Fatal("expect empty buffer, but got ", rb.Len())
	}
}

func TestPipeClose(t *testing.T) {
	pReader, pWriter := New(1024, false)
	payload := []byte{'a', 'b', 'c', 'd'}
	b := buf.New()
	common.Must2(b.Write(payload))
	if err := common.Must(pWriter.WriteMultiBuffer(buf.MultiBuffer{b})); err != nil {
		t.Fatal(err)
	}
	if err := common.Must(pWriter.Close()); err != nil {
		t.Fatal(err)
	}

	rb, err := pReader.ReadMultiBuffer()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if rb.String() != string(payload) {
		t.Fatal("expect content ", string(payload), " but actually ", rb.String())
	}

	rb, err = pReader.ReadMultiBuffer()
	if err != io.EOF {
		t.Fatal("expected EOF, but got ", err)
	}
	if !rb.IsEmpty() {
		t.Fatal("expect empty buffer, but got ", rb.String())
	}
}

func TestPipeLimitZero(t *testing.T) {
	pReader, pWriter := New(0, false)
	bb := buf.New()
	common.Must2(bb.Write([]byte{'a', 'b'}))
	if err := common.Must(pWriter.WriteMultiBuffer(buf.MultiBuffer{bb})); err != nil {
		t.Fatal(err)
	}

	var errg errgroup.Group
	errg.Go(func() error {
		b := buf.New()
		b.Write([]byte{'c', 'd'})
		return pWriter.WriteMultiBuffer(buf.MultiBuffer{b})
	})
	errg.Go(func() error {
		time.Sleep(time.Second)

		var container buf.MultiBufferContainer
		if err := buf.Copy(pReader, &container); err != nil {
			return err
		}

		if r := cmp.Diff(container.String(), "abcd"); r != "" {
			return errors.New(r)
		}
		return nil
	})
	errg.Go(func() error {
		time.Sleep(time.Second * 2)
		return pWriter.Close()
	})
	if err := errg.Wait(); err != nil {
		t.Error(err)
	}
}

func TestPipeWriteMultiThread(t *testing.T) {
	pReader, pWriter := New(0, false)

	var errg errgroup.Group
	for i := 0; i < 10; i++ {
		errg.Go(func() error {
			b := buf.New()
			b.WriteString("abcd")
			return pWriter.WriteMultiBuffer(buf.MultiBuffer{b})
		})
	}
	time.Sleep(time.Millisecond * 100)
	pWriter.Close()
	errg.Wait()

	b, err := pReader.ReadMultiBuffer()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if r := cmp.Diff(b[0].Bytes(), []byte{'a', 'b', 'c', 'd'}); r != "" {
		t.Error(r)
	}
}

func TestInterfaces(t *testing.T) {
	_ = (buf.Reader)(new(Reader))
	_ = (buf.TimeoutReader)(new(Reader))

	_ = (common.Interruptible)(new(Reader))
	_ = (common.Interruptible)(new(Writer))
	_ = (common.Closable)(new(Writer))
}

func BenchmarkPipeReadWrite(b *testing.B) {
	reader, writer := New(-1, false)
	a := buf.New()
	a.Extend(buf.Size)
	c := buf.MultiBuffer{a}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := common.Must(writer.WriteMultiBuffer(c)); err != nil {
			b.Fatal(err)
		}
		d, err := reader.ReadMultiBuffer()
		if err := common.Must(err); err != nil {
			b.Fatal(err)
		}
		c = d
	}
}
