package buf_test

import (
	"bytes"
	"crypto/rand"
	"runtime"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/xtls/xray-core/common"
	. "github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
)

func TestBufferClear(t *testing.T) {
	buffer := New()
	defer buffer.Release()

	payload := "Bytes"
	buffer.Write([]byte(payload))
	if diff := cmp.Diff(buffer.Bytes(), []byte(payload)); diff != "" {
		t.Error(diff)
	}

	buffer.Clear()
	if buffer.Len() != 0 {
		t.Error("expect 0 length, but got ", buffer.Len())
	}
}

func TestBufferIsEmpty(t *testing.T) {
	buffer := New()
	defer buffer.Release()

	if buffer.IsEmpty() != true {
		t.Error("expect empty buffer, but not")
	}
}

func TestBufferString(t *testing.T) {
	buffer := New()
	defer buffer.Release()

	const payload = "Test String"
	common.Must2(buffer.WriteString(payload))
	if buffer.String() != payload {
		t.Error("expect buffer content as ", payload, " but actually ", buffer.String())
	}
}

func TestBufferByte(t *testing.T) {
	{
		buffer := New()
		if err := common.Must(buffer.WriteByte('m')); err != nil {
			t.Fatal(err)
		}
		if buffer.String() != "m" {
			t.Error("expect buffer content as ", "m", " but actually ", buffer.String())
		}
		buffer.Release()
	}
	{
		buffer := StackNew()
		if err := common.Must(buffer.WriteByte('n')); err != nil {
			t.Fatal(err)
		}
		if buffer.String() != "n" {
			t.Error("expect buffer content as ", "n", " but actually ", buffer.String())
		}
		buffer.Release()
	}
	{
		buffer := StackNew()
		common.Must2(buffer.WriteString("HELLOWORLD"))
		if b := buffer.Byte(5); b != 'W' {
			t.Error("unexpected byte ", b)
		}

		buffer.SetByte(5, 'M')
		if buffer.String() != "HELLOMORLD" {
			t.Error("expect buffer content as ", "n", " but actually ", buffer.String())
		}
		buffer.Release()
	}
}

func TestBufferResize(t *testing.T) {
	buffer := New()
	defer buffer.Release()

	const payload = "Test String"
	common.Must2(buffer.WriteString(payload))
	if buffer.String() != payload {
		t.Error("expect buffer content as ", payload, " but actually ", buffer.String())
	}

	buffer.Resize(-6, -3)
	if l := buffer.Len(); int(l) != 3 {
		t.Error("len error ", l)
	}

	if s := buffer.String(); s != "Str" {
		t.Error("unexpect buffer ", s)
	}

	buffer.Resize(int32(len(payload)), 200)
	if l := buffer.Len(); int(l) != 200-len(payload) {
		t.Error("len error ", l)
	}
}

func TestBufferSlice(t *testing.T) {
	{
		b := New()
		common.Must2(b.Write([]byte("abcd")))
		bytes := b.BytesFrom(-2)
		if diff := cmp.Diff(bytes, []byte{'c', 'd'}); diff != "" {
			t.Error(diff)
		}
	}

	{
		b := New()
		common.Must2(b.Write([]byte("abcd")))
		bytes := b.BytesTo(-2)
		if diff := cmp.Diff(bytes, []byte{'a', 'b'}); diff != "" {
			t.Error(diff)
		}
	}

	{
		b := New()
		common.Must2(b.Write([]byte("abcd")))
		bytes := b.BytesRange(-3, -1)
		if diff := cmp.Diff(bytes, []byte{'b', 'c'}); diff != "" {
			t.Error(diff)
		}
	}
}

func TestBufferReadFullFrom(t *testing.T) {
	payload := make([]byte, 1024)
	common.Must2(rand.Read(payload))

	reader := bytes.NewReader(payload)
	b := New()
	n, err := b.ReadFullFrom(reader, 1024)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if n != 1024 {
		t.Error("expect reading 1024 bytes, but actually ", n)
	}

	if diff := cmp.Diff(payload, b.Bytes()); diff != "" {
		t.Error(diff)
	}
}

func TestBufferNewReleaseCycle(t *testing.T) {
	const payload = "first payload"
	const nextPayload = "second payload"

	for i := 0; i < 4; i++ {
		buffer := New()
		if l := buffer.Len(); l != 0 {
			t.Fatal("new buffer is not empty, len: ", l)
		}
		if c := buffer.Cap(); c != Size {
			t.Fatal("unexpected capacity: ", c)
		}
		common.Must2(buffer.Write([]byte(payload)))
		if s := buffer.String(); s != payload {
			t.Fatal("unexpected content: ", s)
		}
		buffer.Release()

		next := New()
		common.Must2(next.Write([]byte(nextPayload)))
		if s := next.String(); s != nextPayload {
			t.Fatal("unexpected content after reacquire: ", s)
		}
		next.Release()
	}
}

func TestBufferStackNewReleaseCycle(t *testing.T) {
	const payload = "stack payload"

	for i := 0; i < 4; i++ {
		buffer := StackNew()
		if l := buffer.Len(); l != 0 {
			t.Fatal("new stack buffer is not empty, len: ", l)
		}
		if c := buffer.Cap(); c != Size {
			t.Fatal("unexpected capacity: ", c)
		}
		common.Must2(buffer.Write([]byte(payload)))
		if s := buffer.String(); s != payload {
			t.Fatal("unexpected content: ", s)
		}
		buffer.Release()
	}
}

func TestBufferLiveBuffersDoNotShareStorage(t *testing.T) {
	first := New()
	defer first.Release()
	second := New()
	defer second.Release()

	common.Must2(first.Write([]byte{0xAA, 0xBB, 0xCC, 0xDD}))
	common.Must2(second.Write([]byte{0x11, 0x22, 0x33, 0x44}))

	// Fill the full storage of first, then confirm second keeps its bytes.
	ext := first.Extend(Size - 4)
	for i := range ext {
		ext[i] = 0xFF
	}
	for i := int32(0); i < 4; i++ {
		first.SetByte(i, 0xFF)
	}
	if diff := cmp.Diff(second.Bytes(), []byte{0x11, 0x22, 0x33, 0x44}); diff != "" {
		t.Error("live buffers share storage: ", diff)
	}

	stackFirst := StackNew()
	defer stackFirst.Release()
	stackSecond := StackNew()
	defer stackSecond.Release()
	common.Must2(stackFirst.Write([]byte{0xE1}))
	common.Must2(stackSecond.Write([]byte{0xF2}))
	if stackFirst.String() != "\xe1" || stackSecond.String() != "\xf2" {
		t.Error("live stack buffers share storage")
	}
}

func TestBufferReleaseNilAndRepeated(t *testing.T) {
	var nilBuffer *Buffer
	nilBuffer.Release()

	dest := net.UDPDestination(net.ParseAddress("8.8.8.8"), net.Port(53))
	buffer := New()
	common.Must2(buffer.Write([]byte("repeated release")))
	buffer.UDP = &dest
	buffer.Release()
	if buffer.UDP != nil {
		t.Error("managed Release must clear UDP metadata")
	}
	if l := buffer.Len(); l != 0 {
		t.Error("released buffer is not empty, len: ", l)
	}

	buffer.Release()
	buffer.Release()

	stack := StackNew()
	stack.Release()
	stack.Release()

	next := New()
	defer next.Release()
	if next.UDP != nil {
		t.Error("new buffer must not keep UDP metadata")
	}
	common.Must2(next.Write([]byte("after repeated release")))
	if s := next.String(); s != "after repeated release" {
		t.Error("unexpected content: ", s)
	}
}

func TestBufferUnmanagedRelease(t *testing.T) {
	original := []byte("unmanaged payload")
	buffer := FromBytes(original)
	buffer.Release()
	if diff := cmp.Diff(buffer.Bytes(), original); diff != "" {
		t.Error("unmanaged Release must keep the original bytes: ", diff)
	}
	buffer.SetByte(0, 'U')
	if original[0] != 'U' {
		t.Error("unmanaged buffer must keep sharing the original storage")
	}

	dest := net.UDPDestination(net.ParseAddress("8.8.8.8"), net.Port(53))
	sized := make([]byte, Size)
	unmanagedSized := FromBytes(sized)
	unmanagedSized.UDP = &dest
	unmanagedSized.Release()
	if unmanagedSized.UDP == nil {
		t.Error("unmanaged Release must keep UDP metadata")
	}

	// An unmanaged slice with Size capacity must not enter the pool.
	for i := 0; i < 4; i++ {
		filler := New()
		storage := filler.Extend(Size)
		for j := range storage {
			storage[j] = 0xFF
		}
		filler.Release()
	}
	if !bytes.Equal(sized, make([]byte, Size)) {
		t.Error("unmanaged storage entered the pool")
	}
}

func TestNewWithSizeCapacityAndRelease(t *testing.T) {
	cases := []struct {
		name    string
		size    int32
		wantCap int32
	}{
		{"zero", 0, 0},
		{"belowSize", Size - 1, Size - 1},
		{"equalSize", Size, Size},
		{"aboveSize", Size + 1, Size + 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buffer := NewWithSize(c.size)
			if l := buffer.Len(); l != 0 {
				t.Fatal("NewWithSize buffer is not empty, len: ", l)
			}
			if gotCap := buffer.Cap(); gotCap != c.wantCap {
				t.Fatal("unexpected capacity: ", gotCap)
			}
			if c.wantCap == 0 {
				if _, err := buffer.Write([]byte{0x01}); err != ErrBufferFull {
					t.Fatal("write to zero size buffer must fail, got: ", err)
				}
			} else {
				common.Must2(buffer.Write(make([]byte, c.wantCap)))
				if l := buffer.Len(); l != c.wantCap {
					t.Fatal("unexpected length: ", l)
				}
				if _, err := buffer.Write([]byte{0x01}); err != ErrBufferFull {
					t.Fatal("write above capacity must fail, got: ", err)
				}
			}
			buffer.Release()

			next := New()
			defer next.Release()
			if gotCap := next.Cap(); gotCap != Size {
				t.Fatal("unexpected capacity after release: ", gotCap)
			}
		})
	}
}

func TestBufferClearResetsActiveRange(t *testing.T) {
	buffer := New()
	defer buffer.Release()

	common.Must2(buffer.Write([]byte("payload")))
	buffer.Clear()
	if l := buffer.Len(); l != 0 {
		t.Fatal("Clear must reset the length, got: ", l)
	}
	if n := len(buffer.Bytes()); n != 0 {
		t.Fatal("Clear must reset the active range, got: ", n)
	}
	if a := buffer.Available(); a != Size {
		t.Fatal("unexpected available capacity: ", a)
	}
	common.Must2(buffer.Write([]byte("next")))
	if s := buffer.String(); s != "next" {
		t.Fatal("unexpected content after Clear: ", s)
	}
}

func TestBufferExtendZeroesStorageAfterReuse(t *testing.T) {
	dirty := New()
	_ = dirty.Extend(64)
	for i := int32(0); i < 64; i++ {
		dirty.SetByte(i, 0xFF)
	}
	dirty.Release()

	buffer := New()
	defer buffer.Release()
	common.Must2(buffer.Write(bytes.Repeat([]byte{0xFF}, 64)))
	buffer.Clear()
	ext := buffer.Extend(64)
	for i, v := range ext {
		if v != 0 {
			t.Fatal("Extend must zero byte ", i)
		}
	}
}

func TestBufferResizeZeroesGrownRange(t *testing.T) {
	dirty := New()
	common.Must2(dirty.Write(make([]byte, 16)))
	for i := int32(0); i < 16; i++ {
		dirty.SetByte(i, 0xFF)
	}
	dirty.Clear()
	dirty.Release()

	buffer := New()
	defer buffer.Release()
	common.Must2(buffer.Write(bytes.Repeat([]byte{0xFF}, 16)))
	buffer.Clear()
	buffer.Resize(0, 16)
	if l := buffer.Len(); l != 16 {
		t.Fatal("unexpected length: ", l)
	}
	for i, v := range buffer.Bytes() {
		if v != 0 {
			t.Fatal("Resize must zero byte ", i)
		}
	}
}

func TestBufferPoolConcurrentAcquireRelease(t *testing.T) {
	const workers = 8
	const rounds = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id byte) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{id}, 32)
			for i := 0; i < rounds; i++ {
				buffer := New()
				if _, err := buffer.Write(payload); err != nil {
					t.Errorf("worker %d: %v", id, err)
					buffer.Release()
					return
				}
				if !bytes.Equal(buffer.Bytes(), payload) {
					t.Errorf("worker %d: unexpected buffer content", id)
					buffer.Release()
					return
				}
				buffer.Release()

				stack := StackNew()
				if _, err := stack.Write(payload); err != nil {
					t.Errorf("worker %d: %v", id, err)
					stack.Release()
					return
				}
				if !bytes.Equal(stack.Bytes(), payload) {
					t.Errorf("worker %d: unexpected stack buffer content", id)
					stack.Release()
					return
				}
				stack.Release()
			}
		}(byte(w + 1))
	}
	wg.Wait()
}

func TestBufferPoolAfterGC(t *testing.T) {
	// Two collections also drop the pool victim cache.
	runtime.GC()
	runtime.GC()

	buffer := New()
	if c := buffer.Cap(); c != Size {
		t.Fatal("unexpected capacity after GC: ", c)
	}
	common.Must2(buffer.Write([]byte("after gc")))
	if s := buffer.String(); s != "after gc" {
		t.Fatal("unexpected content: ", s)
	}
	buffer.Release()

	stack := StackNew()
	if c := stack.Cap(); c != Size {
		t.Fatal("unexpected stack capacity after GC: ", c)
	}
	common.Must2(stack.Write([]byte("after gc")))
	if s := stack.String(); s != "after gc" {
		t.Fatal("unexpected content: ", s)
	}
	stack.Release()
}

func BenchmarkNewBuffer(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buffer := New()
		buffer.Release()
	}
}

func BenchmarkNewBufferStack(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buffer := StackNew()
		buffer.Release()
	}
}

func BenchmarkWrite2(b *testing.B) {
	buffer := New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buffer.Write([]byte{'a', 'b'})
		buffer.Clear()
	}
}

func BenchmarkWrite8(b *testing.B) {
	buffer := New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buffer.Write([]byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'})
		buffer.Clear()
	}
}

func BenchmarkWrite32(b *testing.B) {
	buffer := New()
	payload := make([]byte, 32)
	rand.Read(payload)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buffer.Write(payload)
		buffer.Clear()
	}
}

func BenchmarkWriteByte2(b *testing.B) {
	buffer := New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buffer.WriteByte('a')
		_ = buffer.WriteByte('b')
		buffer.Clear()
	}
}

func BenchmarkWriteByte8(b *testing.B) {
	buffer := New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buffer.WriteByte('a')
		_ = buffer.WriteByte('b')
		_ = buffer.WriteByte('c')
		_ = buffer.WriteByte('d')
		_ = buffer.WriteByte('e')
		_ = buffer.WriteByte('f')
		_ = buffer.WriteByte('g')
		_ = buffer.WriteByte('h')
		buffer.Clear()
	}
}
