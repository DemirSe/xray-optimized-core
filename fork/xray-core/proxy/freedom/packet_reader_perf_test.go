package freedom

// N2: NewPacketReader selects the direct concrete UDP socket after the
// existing statistics handling, and ReadMultiBuffer reads it with
// ReadFromUDPAddrPort. This file compares that path with the exact HEAD packet
// reader kept as a test-only reference, on equivalent loopback sockets. It
// covers both address families, mapped sources, overrides, source restoration,
// counters, wrapper fallback, empty datagrams, read errors, ownership, and the
// paired benchmark BenchmarkFreedomPacketReaderAddress.
//
// The fixtures reuse the generic helpers of packet_writer_perf_test.go:
// pwTestCounter, pwTestUDPConn, pwTestWrapper, pwMaskPacketConn, pwDatagram,
// and pwNoOverride.

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// prReadFunc is one packet read loop under test: the legacy HEAD reference or
// the current production method.
type prReadFunc func(r *PacketReader) (buf.MultiBuffer, error)

// prLegacyReadMultiBuffer is the exact PacketReader.ReadMultiBuffer body from
// HEAD 1dfcee33f592d9cf055dd603a2e071a920b03ad7, kept as the per-packet test
// baseline. It ignores the selected native socket and always uses the generic
// wrapper read, so every comparison runs the old method on the same concrete
// socket type and field layout as the candidate. The HEAD field layout itself
// is preserved separately in prLegacyPacketReader for the constructor
// baseline.
func prLegacyReadMultiBuffer(r *PacketReader) (buf.MultiBuffer, error) {
	b := buf.New()
	b.Resize(0, buf.Size)
	n, d, err := r.PacketConnWrapper.ReadFrom(b.Bytes())
	if err != nil {
		b.Release()
		return nil, err
	}
	b.Resize(0, int32(n))
	// if udp dest addr is changed, we are unable to get the correct src addr
	// so we don't attach src info to udp packet, break cone behavior, assuming the dial dest is the expected scr addr
	if !r.IsOverridden {
		address := xnet.IPAddress(d.(*net.UDPAddr).IP)
		if r.InitChangedAddr == address {
			address = r.InitUnchangedAddr
		}
		b.UDP = &xnet.Destination{
			Address: address,
			Port:    xnet.Port(d.(*net.UDPAddr).Port),
			Network: xnet.Network_UDP,
		}
	}
	if r.Counter != nil {
		r.Counter.Add(int64(n))
	}
	return buf.MultiBuffer{b}, nil
}

// prLegacyPacketReader is the exact HEAD PacketReader field layout without the
// native socket field. The constructor-overhead subcase needs the real HEAD
// size and allocation class, so it cannot reuse the current PacketReader type.
type prLegacyPacketReader struct {
	*internet.PacketConnWrapper
	stats.Counter
	IsOverridden      bool
	InitUnchangedAddr xnet.Address
	InitChangedAddr   xnet.Address
}

// ReadMultiBuffer is the exact PacketReader.ReadMultiBuffer body from HEAD
// 1dfcee33f592d9cf055dd603a2e071a920b03ad7, kept as the method of the HEAD
// reader type. The constructor baseline returns this type through buf.Reader.
func (r *prLegacyPacketReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	b := buf.New()
	b.Resize(0, buf.Size)
	n, d, err := r.PacketConnWrapper.ReadFrom(b.Bytes())
	if err != nil {
		b.Release()
		return nil, err
	}
	b.Resize(0, int32(n))
	// if udp dest addr is changed, we are unable to get the correct src addr
	// so we don't attach src info to udp packet, break cone behavior, assuming the dial dest is the expected scr addr
	if !r.IsOverridden {
		address := xnet.IPAddress(d.(*net.UDPAddr).IP)
		if r.InitChangedAddr == address {
			address = r.InitUnchangedAddr
		}
		b.UDP = &xnet.Destination{
			Address: address,
			Port:    xnet.Port(d.(*net.UDPAddr).Port),
			Network: xnet.Network_UDP,
		}
	}
	if r.Counter != nil {
		r.Counter.Add(int64(n))
	}
	return buf.MultiBuffer{b}, nil
}

// prLegacyNewPacketReader is the exact NewPacketReader body from HEAD
// 1dfcee33f592d9cf055dd603a2e071a920b03ad7 without the native socket
// selection. It returns the HEAD reader type through buf.Reader, so the
// constructor-overhead subcase measures the old allocation and interface
// escape.
func prLegacyNewPacketReader(conn net.Conn, UDPOverride xnet.Destination, DialDest xnet.Destination) buf.Reader {
	iConn := conn
	statConn, ok := iConn.(*stat.CounterConnection)
	if ok {
		iConn = statConn.Conn
	}
	var counter stats.Counter
	if statConn != nil {
		counter = statConn.ReadCounter
	}
	if c, ok := iConn.(*internet.PacketConnWrapper); ok {
		isOverridden := false
		if UDPOverride.Address != nil || UDPOverride.Port != 0 {
			isOverridden = true
		}

		return &prLegacyPacketReader{
			PacketConnWrapper: c,
			Counter:           counter,
			IsOverridden:      isOverridden,
			InitUnchangedAddr: DialDest.Address,
			InitChangedAddr:   xnet.DestinationFromAddr(conn.RemoteAddr()).Address,
		}
	}
	return &buf.PacketReader{Reader: conn}
}

// prReaderFixture is one loopback receive fixture: a sender socket and a
// PacketReader on its own real UDP socket, wired like the freedom dialer where
// the PacketConnWrapper is the connection.
type prReaderFixture struct {
	tb      testing.TB
	network string
	sender  *net.UDPConn
	conn    *net.UDPConn
	reader  *PacketReader
	counter *pwTestCounter
}

// prDestination returns the destination of one bound UDP socket.
func prDestination(conn *net.UDPConn) xnet.Destination {
	addr := conn.LocalAddr().(*net.UDPAddr)
	return xnet.UDPDestination(xnet.IPAddress(addr.IP), xnet.Port(addr.Port))
}

// prNewReader builds one PacketReader and fails when no packet wrapper exists.
func prNewReader(tb testing.TB, conn net.Conn, override xnet.Destination, dialDest xnet.Destination) *PacketReader {
	tb.Helper()
	created := NewPacketReader(conn, override, dialDest)
	reader, ok := created.(*PacketReader)
	if !ok {
		tb.Fatalf("NewPacketReader returned %T, want *PacketReader", created)
	}
	return reader
}

// prNewFixture builds one native-socket receive fixture. The dial destination
// is the sender, so the received source address matches InitChangedAddr and
// the fixture restores the dial destination address like production does.
func prNewFixture(tb testing.TB, network string, override xnet.Destination) *prReaderFixture {
	return prNewFixtureWith(tb, network, override, false)
}

// prNewMaskedFixture builds one wrapper fallback fixture. The mask hides the
// concrete socket from NewPacketReader.
func prNewMaskedFixture(tb testing.TB, network string, override xnet.Destination) *prReaderFixture {
	return prNewFixtureWith(tb, network, override, true)
}

func prNewFixtureWith(tb testing.TB, network string, override xnet.Destination, mask bool) *prReaderFixture {
	tb.Helper()
	sender := pwTestUDPConn(tb, network)
	conn := pwTestUDPConn(tb, network)
	var packetConn net.PacketConn = conn
	if mask {
		packetConn = &pwMaskPacketConn{PacketConn: conn}
	}
	reader := prNewReader(tb, pwTestWrapper(packetConn, sender.LocalAddr()), override, prDestination(sender))
	counter := &pwTestCounter{}
	reader.Counter = counter
	return &prReaderFixture{
		tb:      tb,
		network: network,
		sender:  sender,
		conn:    conn,
		reader:  reader,
		counter: counter,
	}
}

// readerAddrPort returns the value address of the reader socket.
func (f *prReaderFixture) readerAddrPort() netip.AddrPort {
	return f.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// send transmits one datagram to the reader socket.
func (f *prReaderFixture) send(payload []byte) {
	f.tb.Helper()
	if _, err := f.sender.WriteToUDPAddrPort(payload, f.readerAddrPort()); err != nil {
		f.tb.Fatalf("send %d bytes: %v", len(payload), err)
	}
}

// source returns the destination metadata that a datagram from the sender
// must carry.
func (f *prReaderFixture) source() xnet.Destination {
	return prDestination(f.sender)
}

// prCheckCounters fails when one test counter does not match the expected
// calls and total.
func prCheckCounters(tb testing.TB, counter *pwTestCounter, wantAdds int, wantTotal int) {
	tb.Helper()
	if counter.adds != wantAdds || counter.total != int64(wantTotal) {
		tb.Fatalf("counter adds=%d total=%d, want adds=%d total=%d",
			counter.adds, counter.total, wantAdds, wantTotal)
	}
}

// prReadDatagram reads one datagram with read, checks the payload and the
// optional source metadata, and releases the output.
func prReadDatagram(tb testing.TB, read prReadFunc, reader *PacketReader, wantPayload []byte, wantSource *xnet.Destination) {
	tb.Helper()
	mb, err := read(reader)
	if err != nil {
		tb.Fatalf("read: %v", err)
	}
	if len(mb) != 1 {
		buf.ReleaseMulti(mb)
		tb.Fatalf("multi buffer length = %d, want 1", len(mb))
	}
	b := mb[0]
	if !bytes.Equal(b.Bytes(), wantPayload) {
		buf.ReleaseMulti(mb)
		tb.Fatalf("payload has %d bytes, want %d matching bytes", b.Len(), len(wantPayload))
	}
	if wantSource == nil {
		if b.UDP != nil {
			buf.ReleaseMulti(mb)
			tb.Fatalf("UDP metadata = %v, want none", b.UDP)
		}
	} else {
		if b.UDP == nil {
			buf.ReleaseMulti(mb)
			tb.Fatal("UDP source metadata missing")
		}
		if b.UDP.Network != wantSource.Network || b.UDP.Port != wantSource.Port || b.UDP.Address != wantSource.Address {
			buf.ReleaseMulti(mb)
			tb.Fatalf("UDP source = %v:%v, want %v:%v",
				b.UDP.Address, b.UDP.Port, wantSource.Address, wantSource.Port)
		}
	}
	buf.ReleaseMulti(mb)
}

func TestPacketReaderNativeCapabilitySelection(t *testing.T) {
	sender := pwTestUDPConn(t, "udp4")
	dialDest := prDestination(sender)
	t.Run("native-udp-socket", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		reader := prNewReader(t, pwTestWrapper(conn, sender.LocalAddr()), pwNoOverride, dialDest)
		if reader.nativeUDPConn != conn {
			t.Fatalf("nativeUDPConn = %v, want the concrete socket", reader.nativeUDPConn)
		}
	})
	t.Run("masked-packet-conn", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		reader := prNewReader(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: conn}, sender.LocalAddr()), pwNoOverride, dialDest)
		if reader.nativeUDPConn != nil {
			t.Fatal("native capability leaked through a custom wrapper")
		}
	})
	t.Run("stats-wrapper", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		counter := &pwTestCounter{}
		wrapped := &stat.CounterConnection{Conn: pwTestWrapper(conn, sender.LocalAddr()), ReadCounter: counter}
		reader := prNewReader(t, wrapped, pwNoOverride, dialDest)
		if reader.nativeUDPConn != conn {
			t.Fatal("stats wrapper hid the native socket")
		}
		if reader.Counter != counter {
			t.Fatal("stats wrapper hid the read counter")
		}
	})
	t.Run("stats-wrapper-over-mask", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		wrapped := &stat.CounterConnection{Conn: pwTestWrapper(&pwMaskPacketConn{PacketConn: conn}, sender.LocalAddr())}
		reader := prNewReader(t, wrapped, pwNoOverride, dialDest)
		if reader.nativeUDPConn != nil {
			t.Fatal("native capability leaked through the stats wrapper and the mask")
		}
	})
	t.Run("non-wrapper-conn", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		created := NewPacketReader(client, pwNoOverride, dialDest)
		if _, ok := created.(*buf.PacketReader); !ok {
			t.Fatalf("NewPacketReader returned %T, want *buf.PacketReader", created)
		}
	})
}

func TestPacketReaderConstructorBaseline(t *testing.T) {
	legacySize := reflect.TypeOf(prLegacyPacketReader{}).Size()
	currentSize := reflect.TypeOf(PacketReader{}).Size()
	pointerSize := uintptr(strconv.IntSize / 8)
	t.Logf("HEAD reader size = %d bytes, current reader size = %d bytes, one pointer = %d bytes",
		legacySize, currentSize, pointerSize)
	if currentSize != legacySize+pointerSize {
		t.Fatalf("current reader size = %d, want the HEAD %d plus one pointer field (%d)",
			currentSize, legacySize, pointerSize)
	}

	sender := pwTestUDPConn(t, "udp4")
	dialDest := prDestination(sender)
	legacyConn := pwTestUDPConn(t, "udp4")
	created := prLegacyNewPacketReader(pwTestWrapper(legacyConn, sender.LocalAddr()), pwNoOverride, dialDest)
	headReader, ok := created.(*prLegacyPacketReader)
	if !ok {
		t.Fatalf("prLegacyNewPacketReader returned %T, want *prLegacyPacketReader", created)
	}
	counter := &pwTestCounter{}
	headReader.Counter = counter

	// One real read through the HEAD reader type on the same socket shape.
	payload := pwDatagram(512, 7)
	port := uint16(legacyConn.LocalAddr().(*net.UDPAddr).Port)
	if _, err := sender.WriteToUDPAddrPort(payload, netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port)); err != nil {
		t.Fatalf("send: %v", err)
	}
	mb, err := headReader.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("HEAD reader read: %v", err)
	}
	if len(mb) != 1 {
		buf.ReleaseMulti(mb)
		t.Fatalf("multi buffer length = %d, want 1", len(mb))
	}
	buffer := mb[0]
	if !bytes.Equal(buffer.Bytes(), payload) {
		buf.ReleaseMulti(mb)
		t.Fatal("HEAD reader payload mismatch")
	}
	if buffer.UDP == nil || buffer.UDP.Port != xnet.Port(sender.LocalAddr().(*net.UDPAddr).Port) {
		buf.ReleaseMulti(mb)
		t.Fatalf("HEAD reader source = %v, want the sender port %d", buffer.UDP, sender.LocalAddr().(*net.UDPAddr).Port)
	}
	buf.ReleaseMulti(mb)
	prCheckCounters(t, counter, 1, len(payload))

	// The constructor baseline escapes through buf.Reader like HEAD does, so
	// the allocation count stays comparable while the size class grows.
	benchSender := pwTestUDPConn(t, "udp4")
	wrapper := pwTestWrapper(pwTestUDPConn(t, "udp4"), benchSender.LocalAddr())
	benchDest := prDestination(benchSender)
	legacyAllocs := testing.AllocsPerRun(100, func() {
		prBenchmarkReaderSink = prLegacyNewPacketReader(wrapper, pwNoOverride, benchDest)
	})
	currentAllocs := testing.AllocsPerRun(100, func() {
		prBenchmarkReaderSink = NewPacketReader(wrapper, pwNoOverride, benchDest)
	})
	if legacyAllocs != currentAllocs {
		t.Fatalf("constructor allocations = HEAD %v, current %v, want equal counts", legacyAllocs, currentAllocs)
	}
	t.Logf("constructor allocations per reader: HEAD %v, current %v", legacyAllocs, currentAllocs)
}

func TestPacketReaderNativeMatchesLegacy(t *testing.T) {
	sizes := []int{1, 8, 128, 512, 1200, 1400, 4096, 8192}
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			native := prNewFixture(t, network, pwNoOverride)
			legacy := prNewFixture(t, network, pwNoOverride)
			if native.reader.nativeUDPConn == nil {
				t.Fatal("candidate reader did not select the native socket")
			}
			if legacy.reader.nativeUDPConn == nil {
				t.Fatal("legacy reader did not hold the same concrete socket type")
			}
			wantTotal := 0
			for _, size := range sizes {
				payload := pwDatagram(size, byte(size))
				native.send(payload)
				legacy.send(payload)
				nativeSource := native.source()
				legacySource := legacy.source()
				prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, &nativeSource)
				prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, &legacySource)
				wantTotal += size
			}
			prCheckCounters(t, native.counter, len(sizes), wantTotal)
			prCheckCounters(t, legacy.counter, len(sizes), wantTotal)
		})
	}
}

func TestPacketReaderFallbackMatchesLegacy(t *testing.T) {
	sizes := []int{8, 1200, 8192}
	native := prNewMaskedFixture(t, "udp4", pwNoOverride)
	legacy := prNewMaskedFixture(t, "udp4", pwNoOverride)
	if native.reader.nativeUDPConn != nil || legacy.reader.nativeUDPConn != nil {
		t.Fatal("masked wrapper selected the native path")
	}
	wantTotal := 0
	for _, size := range sizes {
		payload := pwDatagram(size, byte(size))
		native.send(payload)
		legacy.send(payload)
		nativeSource := native.source()
		legacySource := legacy.source()
		prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, &nativeSource)
		prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, &legacySource)
		wantTotal += size
	}
	prCheckCounters(t, native.counter, len(sizes), wantTotal)
	prCheckCounters(t, legacy.counter, len(sizes), wantTotal)
}

func TestPacketReaderMappedIPv4SourceMatchesLegacy(t *testing.T) {
	// A dual-stack reader bound to the unspecified address receives IPv4
	// sources as mapped IPv6 values, like a production wildcard bind.
	sender := pwTestUDPConn(t, "udp4")
	senderAddr := sender.LocalAddr().(*net.UDPAddr)
	dialDest := prDestination(sender)
	newDualStack := func() *net.UDPConn {
		t.Helper()
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6unspecified})
		if err != nil {
			t.Fatalf("open dual-stack socket: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	candidateConn := newDualStack()
	legacyConn := newDualStack()
	candidate := prNewReader(t, pwTestWrapper(candidateConn, sender.LocalAddr()), pwNoOverride, dialDest)
	candidate.Counter = &pwTestCounter{}
	legacy := prNewReader(t, pwTestWrapper(legacyConn, sender.LocalAddr()), pwNoOverride, dialDest)
	legacy.Counter = &pwTestCounter{}
	if candidate.nativeUDPConn == nil {
		t.Fatal("candidate reader did not select the dual-stack socket")
	}

	checkMapped := func(t *testing.T, read prReadFunc, reader *PacketReader, payload []byte) {
		t.Helper()
		mb, err := read(reader)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		defer buf.ReleaseMulti(mb)
		if len(mb) != 1 {
			t.Fatalf("multi buffer length = %d, want 1", len(mb))
		}
		b := mb[0]
		if !bytes.Equal(b.Bytes(), payload) {
			t.Fatal("mapped datagram payload mismatch")
		}
		if b.UDP == nil {
			t.Fatal("mapped datagram has no source metadata")
		}
		if !b.UDP.Address.Family().IsIPv4() {
			t.Fatalf("source family = %v, want IPv4 after the mapped normalization", b.UDP.Address.Family())
		}
		if got := b.UDP.Address.String(); got != "127.0.0.1" {
			t.Fatalf("source address = %s, want 127.0.0.1", got)
		}
		if b.UDP.Port != xnet.Port(senderAddr.Port) {
			t.Fatalf("source port = %d, want %d", b.UDP.Port, senderAddr.Port)
		}
	}

	candidatePayload := pwDatagram(64, 3)
	legacyPayload := pwDatagram(64, 4)
	v4 := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	for _, send := range []struct {
		payload []byte
		target  *net.UDPConn
	}{
		{candidatePayload, candidateConn},
		{legacyPayload, legacyConn},
	} {
		port := uint16(send.target.LocalAddr().(*net.UDPAddr).Port)
		if _, err := sender.WriteToUDPAddrPort(send.payload, netip.AddrPortFrom(v4, port)); err != nil {
			t.Fatalf("send to dual-stack socket: %v", err)
		}
	}
	checkMapped(t, (*PacketReader).ReadMultiBuffer, candidate, candidatePayload)
	checkMapped(t, prLegacyReadMultiBuffer, legacy, legacyPayload)
}

func TestPacketReaderOverrides(t *testing.T) {
	testCases := []struct {
		name     string
		override xnet.Destination
	}{
		{name: "address-only", override: xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 2}), 0)},
		{name: "port-only", override: xnet.UDPDestination(nil, 5399)},
		{name: "combined", override: xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 2}), 5399)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			native := prNewFixture(t, "udp4", testCase.override)
			legacy := prNewFixture(t, "udp4", testCase.override)
			if !native.reader.IsOverridden || !legacy.reader.IsOverridden {
				t.Fatal("override did not set IsOverridden")
			}
			payload := pwDatagram(1200, 17)
			native.send(payload)
			legacy.send(payload)
			// An override suppresses every source metadata field.
			prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, nil)
			prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, nil)
			prCheckCounters(t, native.counter, 1, len(payload))
			prCheckCounters(t, legacy.counter, 1, len(payload))
		})
	}
}

func TestPacketReaderInitChangedAddr(t *testing.T) {
	t.Run("restored-domain", func(t *testing.T) {
		native := prNewFixture(t, "udp4", pwNoOverride)
		legacy := prNewFixture(t, "udp4", pwNoOverride)
		domain := xnet.DomainAddress("fixture.example")
		native.reader.InitUnchangedAddr = domain
		legacy.reader.InitUnchangedAddr = domain
		payload := pwDatagram(512, 19)
		native.send(payload)
		legacy.send(payload)
		nativeSource := xnet.UDPDestination(domain, xnet.Port(native.sender.LocalAddr().(*net.UDPAddr).Port))
		legacySource := xnet.UDPDestination(domain, xnet.Port(legacy.sender.LocalAddr().(*net.UDPAddr).Port))
		prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, &nativeSource)
		prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, &legacySource)
	})
	t.Run("unrelated-source", func(t *testing.T) {
		native := prNewFixture(t, "udp4", pwNoOverride)
		legacy := prNewFixture(t, "udp4", pwNoOverride)
		// The dial target does not match the sender, so the real source stays.
		native.reader.InitChangedAddr = xnet.IPAddress([]byte{10, 88, 7, 6})
		legacy.reader.InitChangedAddr = xnet.IPAddress([]byte{10, 88, 7, 6})
		payload := pwDatagram(512, 20)
		native.send(payload)
		legacy.send(payload)
		nativeSource := native.source()
		legacySource := legacy.source()
		prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, &nativeSource)
		prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, &legacySource)
	})
}

func TestPacketReaderEmptyDatagram(t *testing.T) {
	native := prNewFixture(t, "udp4", pwNoOverride)
	legacy := prNewFixture(t, "udp4", pwNoOverride)
	native.send(nil)
	legacy.send(nil)
	testCases := []struct {
		name    string
		read    prReadFunc
		reader  *PacketReader
		counter *pwTestCounter
		source  xnet.Destination
	}{
		{name: "native", read: (*PacketReader).ReadMultiBuffer, reader: native.reader, counter: native.counter, source: native.source()},
		{name: "legacy", read: prLegacyReadMultiBuffer, reader: legacy.reader, counter: legacy.counter, source: legacy.source()},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			mb, err := testCase.read(testCase.reader)
			if err != nil {
				t.Fatalf("read empty datagram: %v", err)
			}
			if len(mb) != 1 {
				buf.ReleaseMulti(mb)
				t.Fatalf("multi buffer length = %d, want 1", len(mb))
			}
			b := mb[0]
			if b.Len() != 0 {
				buf.ReleaseMulti(mb)
				t.Fatalf("empty datagram length = %d, want 0", b.Len())
			}
			if b.Cap() != buf.Size {
				buf.ReleaseMulti(mb)
				t.Fatalf("empty datagram buffer capacity = %d, want %d", b.Cap(), buf.Size)
			}
			if b.UDP == nil || b.UDP.Address != testCase.source.Address || b.UDP.Port != testCase.source.Port {
				buf.ReleaseMulti(mb)
				t.Fatalf("empty datagram source = %v, want %v", b.UDP, testCase.source)
			}
			buf.ReleaseMulti(mb)
			prCheckCounters(t, testCase.counter, 1, 0)
		})
	}
}

func TestPacketReaderDatagramIntegrity(t *testing.T) {
	native := prNewFixture(t, "udp4", pwNoOverride)
	legacy := prNewFixture(t, "udp4", pwNoOverride)
	sizes := []int{0, 1, 8, 128, 512, 1200, 1400, 4096, 8192}
	wantTotal := 0
	for _, size := range sizes {
		payload := pwDatagram(size, byte(size))
		native.send(payload)
		legacy.send(payload)
		nativeSource := native.source()
		legacySource := legacy.source()
		prReadDatagram(t, (*PacketReader).ReadMultiBuffer, native.reader, payload, &nativeSource)
		prReadDatagram(t, prLegacyReadMultiBuffer, legacy.reader, payload, &legacySource)
		wantTotal += size
	}
	prCheckCounters(t, native.counter, len(sizes), wantTotal)
	prCheckCounters(t, legacy.counter, len(sizes), wantTotal)

	// One oversized datagram truncates the same way on both paths.
	oversize := pwDatagram(buf.Size+64, 23)
	native.send(oversize)
	legacy.send(oversize)
	nativeLen, nativePayload := prReadTruncated(t, (*PacketReader).ReadMultiBuffer, native.reader)
	legacyLen, legacyPayload := prReadTruncated(t, prLegacyReadMultiBuffer, legacy.reader)
	if nativeLen != legacyLen {
		t.Fatalf("truncated lengths differ: native %d, legacy %d", nativeLen, legacyLen)
	}
	if nativeLen != buf.Size {
		t.Fatalf("truncated length = %d, want %d", nativeLen, buf.Size)
	}
	if !bytes.Equal(nativePayload, oversize[:nativeLen]) || !bytes.Equal(legacyPayload, oversize[:legacyLen]) {
		t.Fatal("truncated datagram prefix mismatch")
	}
	wantTotal += nativeLen
	prCheckCounters(t, native.counter, len(sizes)+1, wantTotal)
	prCheckCounters(t, legacy.counter, len(sizes)+1, wantTotal)
}

// prReadTruncated reads one oversized datagram and returns its received length
// and a copy of the received bytes.
func prReadTruncated(tb testing.TB, read prReadFunc, reader *PacketReader) (int, []byte) {
	tb.Helper()
	mb, err := read(reader)
	if err != nil {
		tb.Fatalf("read truncated datagram: %v", err)
	}
	defer buf.ReleaseMulti(mb)
	if len(mb) != 1 {
		tb.Fatalf("multi buffer length = %d, want 1", len(mb))
	}
	payload := append([]byte(nil), mb[0].Bytes()...)
	return len(payload), payload
}

func TestPacketReaderReadErrors(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		testCases := []struct {
			name string
			read prReadFunc
			f    *prReaderFixture
		}{
			{name: "native", read: (*PacketReader).ReadMultiBuffer, f: prNewFixture(t, "udp4", pwNoOverride)},
			{name: "fallback", read: (*PacketReader).ReadMultiBuffer, f: prNewMaskedFixture(t, "udp4", pwNoOverride)},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				if err := testCase.f.conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
					t.Fatalf("set read deadline: %v", err)
				}
				mb, err := testCase.read(testCase.f.reader)
				if mb != nil {
					buf.ReleaseMulti(mb)
					t.Fatalf("multi buffer = %v, want nil", mb)
				}
				if err == nil {
					t.Fatal("read returned no error")
				}
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("error = %v, want a timeout error", err)
				}
				prCheckCounters(t, testCase.f.counter, 0, 0)
			})
		}
	})
	t.Run("closed", func(t *testing.T) {
		testCases := []struct {
			name string
			read prReadFunc
			f    *prReaderFixture
		}{
			{name: "native", read: (*PacketReader).ReadMultiBuffer, f: prNewFixture(t, "udp4", pwNoOverride)},
			{name: "fallback", read: (*PacketReader).ReadMultiBuffer, f: prNewMaskedFixture(t, "udp4", pwNoOverride)},
			{name: "legacy-native", read: prLegacyReadMultiBuffer, f: prNewFixture(t, "udp4", pwNoOverride)},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				if err := testCase.f.conn.Close(); err != nil {
					t.Fatalf("close reader socket: %v", err)
				}
				mb, err := testCase.read(testCase.f.reader)
				if mb != nil {
					buf.ReleaseMulti(mb)
					t.Fatalf("multi buffer = %v, want nil", mb)
				}
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("error = %v, want net.ErrClosed", err)
				}
				prCheckCounters(t, testCase.f.counter, 0, 0)
			})
		}
	})
}

func TestPacketReaderOutputOwnership(t *testing.T) {
	fixture := prNewFixture(t, "udp4", pwNoOverride)
	first := pwDatagram(512, 41)
	second := pwDatagram(64, 42)

	fixture.send(first)
	firstMulti, err := fixture.reader.ReadMultiBuffer()
	if err != nil {
		t.Fatalf("read first datagram: %v", err)
	}
	if len(firstMulti) != 1 {
		buf.ReleaseMulti(firstMulti)
		t.Fatalf("first multi buffer length = %d, want 1", len(firstMulti))
	}
	firstBuffer := firstMulti[0]
	if firstBuffer.Cap() != buf.Size {
		buf.ReleaseMulti(firstMulti)
		t.Fatalf("first buffer capacity = %d, want the managed %d", firstBuffer.Cap(), buf.Size)
	}
	if int(firstBuffer.Len()) != len(first) {
		buf.ReleaseMulti(firstMulti)
		t.Fatalf("first buffer length = %d, want %d", firstBuffer.Len(), len(first))
	}

	// The first output stays owned while the reader receives again.
	fixture.send(second)
	secondMulti, err := fixture.reader.ReadMultiBuffer()
	if err != nil {
		buf.ReleaseMulti(firstMulti)
		t.Fatalf("read second datagram: %v", err)
	}
	if len(secondMulti) != 1 {
		buf.ReleaseMulti(firstMulti)
		buf.ReleaseMulti(secondMulti)
		t.Fatalf("second multi buffer length = %d, want 1", len(secondMulti))
	}
	secondBuffer := secondMulti[0]
	if firstBuffer == secondBuffer {
		buf.ReleaseMulti(firstMulti)
		buf.ReleaseMulti(secondMulti)
		t.Fatal("held output buffer was reused")
	}
	if !bytes.Equal(firstBuffer.Bytes(), first) || !bytes.Equal(secondBuffer.Bytes(), second) {
		buf.ReleaseMulti(firstMulti)
		buf.ReleaseMulti(secondMulti)
		t.Fatal("held outputs changed after a later receive")
	}
	if firstBuffer.UDP == nil || secondBuffer.UDP == nil {
		buf.ReleaseMulti(firstMulti)
		buf.ReleaseMulti(secondMulti)
		t.Fatal("output source metadata missing")
	}

	buf.ReleaseMulti(firstMulti)
	buf.ReleaseMulti(secondMulti)
	if firstBuffer.Cap() != 0 || secondBuffer.Cap() != 0 {
		t.Fatal("released outputs still own their storage")
	}
	prCheckCounters(t, fixture.counter, 2, len(first)+len(second))
}

func TestPacketReaderConcurrentNativeReaders(t *testing.T) {
	const readers = 4
	const packets = 16
	fixtures := make([]*prReaderFixture, readers)
	for i := range fixtures {
		fixtures[i] = prNewFixture(t, "udp4", pwNoOverride)
		if fixtures[i].reader.nativeUDPConn == nil {
			t.Fatal("native socket not selected")
		}
	}
	var wg sync.WaitGroup
	for i := range fixtures {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fixture := fixtures[i]
			for p := 0; p < packets; p++ {
				payload := pwDatagram(256, byte(10*i+p))
				if _, err := fixture.sender.WriteToUDPAddrPort(payload, fixture.readerAddrPort()); err != nil {
					t.Errorf("reader %d packet %d: send: %v", i, p, err)
					return
				}
				mb, err := fixture.reader.ReadMultiBuffer()
				if err != nil {
					t.Errorf("reader %d packet %d: read: %v", i, p, err)
					return
				}
				if len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), payload) {
					buf.ReleaseMulti(mb)
					t.Errorf("reader %d packet %d: datagram mismatch", i, p)
					return
				}
				buf.ReleaseMulti(mb)
			}
		}(i)
	}
	wg.Wait()
	for i := range fixtures {
		prCheckCounters(t, fixtures[i].counter, packets, packets*256)
	}
}

var (
	prBenchmarkAddressSink xnet.Address
	prBenchmarkReaderSink  buf.Reader
)

// prRunLoopbackReadBenchmark sends one datagram from the fixture sender and
// reads it with read. The fixture is identical for the legacy and candidate
// subcases.
func prRunLoopbackReadBenchmark(b *testing.B, read prReadFunc, fixture *prReaderFixture, payload []byte) {
	b.Helper()
	readerAddr := fixture.readerAddrPort()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fixture.sender.WriteToUDPAddrPort(payload, readerAddr); err != nil {
			b.Fatalf("send: %v", err)
		}
		mb, err := read(fixture.reader)
		if err != nil {
			b.Fatalf("read: %v", err)
		}
		if len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), payload) {
			buf.ReleaseMulti(mb)
			b.Fatal("datagram mismatch")
		}
		buf.ReleaseMulti(mb)
	}
	b.StopTimer()
}

func BenchmarkFreedomPacketReaderAddress(b *testing.B) {
	v4Slice := []byte{127, 0, 0, 1}
	mappedSlice := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 127, 0, 0, 1}
	v4 := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	mapped := netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 127, 0, 0, 1})

	b.Run("address-only/baseline-ipaddress-v4", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			prBenchmarkAddressSink = xnet.IPAddress(v4Slice)
		}
		if prBenchmarkAddressSink == nil {
			b.Fatal("baseline conversion returned nil")
		}
	})
	b.Run("address-only/candidate-ipaddressfromaddr-v4", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			prBenchmarkAddressSink = xnet.IPAddressFromAddr(v4)
		}
		if prBenchmarkAddressSink == nil {
			b.Fatal("candidate conversion returned nil")
		}
	})
	b.Run("address-only/baseline-ipaddress-mapped", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			prBenchmarkAddressSink = xnet.IPAddress(mappedSlice)
		}
		if prBenchmarkAddressSink == nil {
			b.Fatal("baseline conversion returned nil")
		}
	})
	b.Run("address-only/candidate-ipaddressfromaddr-mapped", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			prBenchmarkAddressSink = xnet.IPAddressFromAddr(mapped)
		}
		if prBenchmarkAddressSink == nil {
			b.Fatal("candidate conversion returned nil")
		}
	})

	for _, network := range []string{"udp4", "udp6"} {
		for _, size := range []int{64, 1400, 8192} {
			name := network + "-size" + strconv.Itoa(size)
			b.Run("loopback-"+name+"/baseline-legacy-readfrom", func(b *testing.B) {
				fixture := prNewFixture(b, network, pwNoOverride)
				if fixture.reader.nativeUDPConn == nil {
					b.Fatal("baseline fixture has no native socket")
				}
				prRunLoopbackReadBenchmark(b, prLegacyReadMultiBuffer, fixture, pwDatagram(size, byte(size)))
			})
			b.Run("loopback-"+name+"/candidate-native-readfromudpaddrport", func(b *testing.B) {
				fixture := prNewFixture(b, network, pwNoOverride)
				if fixture.reader.nativeUDPConn == nil {
					b.Fatal("candidate fixture did not select the native socket")
				}
				prRunLoopbackReadBenchmark(b, (*PacketReader).ReadMultiBuffer, fixture, pwDatagram(size, byte(size)))
			})
		}
	}

	b.Run("control-custom-conn-fallback/baseline-legacy-readfrom", func(b *testing.B) {
		fixture := prNewMaskedFixture(b, "udp4", pwNoOverride)
		if fixture.reader.nativeUDPConn != nil {
			b.Fatal("fallback baseline selected the native path")
		}
		prRunLoopbackReadBenchmark(b, prLegacyReadMultiBuffer, fixture, pwDatagram(1400, 31))
	})
	b.Run("control-custom-conn-fallback/candidate-generic-readfrom", func(b *testing.B) {
		fixture := prNewMaskedFixture(b, "udp4", pwNoOverride)
		if fixture.reader.nativeUDPConn != nil {
			b.Fatal("fallback candidate selected the native path")
		}
		prRunLoopbackReadBenchmark(b, (*PacketReader).ReadMultiBuffer, fixture, pwDatagram(1400, 31))
	})

	override := xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 2}), 5399)
	b.Run("control-override/baseline-legacy-readfrom", func(b *testing.B) {
		fixture := prNewFixture(b, "udp4", override)
		if !fixture.reader.IsOverridden {
			b.Fatal("override fixture is not overridden")
		}
		prRunLoopbackReadBenchmark(b, prLegacyReadMultiBuffer, fixture, pwDatagram(1400, 32))
	})
	b.Run("control-override/candidate-native-readfromudpaddrport", func(b *testing.B) {
		fixture := prNewFixture(b, "udp4", override)
		if fixture.reader.nativeUDPConn == nil || !fixture.reader.IsOverridden {
			b.Fatal("override fixture did not select the native path")
		}
		prRunLoopbackReadBenchmark(b, (*PacketReader).ReadMultiBuffer, fixture, pwDatagram(1400, 32))
	})

	b.Run("constructor-overhead/baseline-legacy-new-reader", func(b *testing.B) {
		sender := pwTestUDPConn(b, "udp4")
		wrapper := pwTestWrapper(pwTestUDPConn(b, "udp4"), sender.LocalAddr())
		dialDest := prDestination(sender)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			prBenchmarkReaderSink = prLegacyNewPacketReader(wrapper, pwNoOverride, dialDest)
		}
		b.StopTimer()
		if prBenchmarkReaderSink == nil {
			b.Fatal("legacy constructor returned nil")
		}
	})
	b.Run("constructor-overhead/candidate-new-reader", func(b *testing.B) {
		sender := pwTestUDPConn(b, "udp4")
		wrapper := pwTestWrapper(pwTestUDPConn(b, "udp4"), sender.LocalAddr())
		dialDest := prDestination(sender)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			prBenchmarkReaderSink = NewPacketReader(wrapper, pwNoOverride, dialDest)
		}
		b.StopTimer()
		if prBenchmarkReaderSink == nil {
			b.Fatal("candidate constructor returned nil")
		}
	})
}
