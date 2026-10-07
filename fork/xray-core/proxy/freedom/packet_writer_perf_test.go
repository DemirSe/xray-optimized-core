package freedom

// C3: native UDP writes use the concrete socket with a value address. This
// file compares the native and generic paths on equivalent loopback sockets,
// and covers the fallback, override, cache, error, counter, release, and
// nil-metadata behavior. BenchmarkFreedomPacketWriterAddress measures the
// address conversion and the representative loopback write.

import (
	"bytes"
	"errors"
	mathrand "math/rand"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// pwTestCounter is one stats.Counter for the packet writer tests.
type pwTestCounter struct {
	adds  int
	total int64
}

func (c *pwTestCounter) Add(delta int64) int64 {
	c.adds++
	c.total += delta
	return c.total - delta
}

func (c *pwTestCounter) Set(value int64) int64 {
	previous := c.total
	c.total = value
	return previous
}

func (c *pwTestCounter) Value() int64 { return c.total }

// pwTestListener is one loopback UDP listener.
type pwTestListener struct {
	tb   testing.TB
	conn *net.UDPConn
}

// pwLoopbackIP returns the loopback address of one family.
func pwLoopbackIP(network string) net.IP {
	if network == "udp6" {
		return net.IPv6loopback
	}
	return net.IPv4(127, 0, 0, 1)
}

func pwListenAt(tb testing.TB, network string, ip net.IP, port int) *pwTestListener {
	tb.Helper()
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		tb.Fatalf("listen %s %s:%d: %v", network, ip, port, err)
	}
	tb.Cleanup(func() { conn.Close() })
	return &pwTestListener{tb: tb, conn: conn}
}

func pwListen(tb testing.TB, network string) *pwTestListener {
	return pwListenAt(tb, network, pwLoopbackIP(network), 0)
}

func (l *pwTestListener) port() int {
	return l.conn.LocalAddr().(*net.UDPAddr).Port
}

func (l *pwTestListener) destination() xnet.Destination {
	addr := l.conn.LocalAddr().(*net.UDPAddr)
	return xnet.UDPDestination(xnet.IPAddress(addr.IP), xnet.Port(addr.Port))
}

// readInto reads one datagram into buffer.
func (l *pwTestListener) readInto(buffer []byte, timeout time.Duration) ([]byte, error) {
	if err := l.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	n, _, err := l.conn.ReadFrom(buffer)
	if err != nil {
		return nil, err
	}
	return buffer[:n], nil
}

// read returns the next datagram or a timeout error.
func (l *pwTestListener) read(timeout time.Duration) ([]byte, error) {
	return l.readInto(make([]byte, 65536), timeout)
}

func (l *pwTestListener) receive(tb testing.TB) []byte {
	tb.Helper()
	payload, err := l.read(3 * time.Second)
	if err != nil {
		tb.Fatalf("read datagram: %v", err)
	}
	return payload
}

// receiveNothing fails when a datagram arrives within the timeout.
func (l *pwTestListener) receiveNothing(tb testing.TB, timeout time.Duration) {
	tb.Helper()
	payload, err := l.read(timeout)
	if err == nil {
		tb.Fatalf("received a %d byte datagram, want none", len(payload))
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		tb.Fatalf("read error = %v, want a timeout", err)
	}
}

// pwTestUDPConn returns one unconnected loopback UDP socket.
func pwTestUDPConn(tb testing.TB, network string) *net.UDPConn {
	tb.Helper()
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: pwLoopbackIP(network)})
	if err != nil {
		tb.Fatalf("open %s socket: %v", network, err)
	}
	tb.Cleanup(func() { conn.Close() })
	return conn
}

// pwDialUDP returns one connected loopback UDP socket.
func pwDialUDP(tb testing.TB, dest xnet.Destination) *net.UDPConn {
	tb.Helper()
	udpAddr, ok := dest.RawNetAddr().(*net.UDPAddr)
	if !ok {
		tb.Fatalf("destination %v has no UDP address", dest)
	}
	conn, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		tb.Fatalf("dial %v: %v", udpAddr, err)
	}
	tb.Cleanup(func() { conn.Close() })
	return conn
}

// pwTestWrapper wraps one packet connection. dest is the address used by the
// generic Write method, like the dial destination of a real PacketConnWrapper.
func pwTestWrapper(conn net.PacketConn, dest net.Addr) *internet.PacketConnWrapper {
	if dest == nil {
		dest = conn.LocalAddr()
	}
	return &internet.PacketConnWrapper{PacketConn: conn, Dest: dest}
}

// pwMaskPacketConn hides the concrete UDP socket, like a packet mask or any
// other custom wrapper. NewPacketWriter must keep the generic path for it.
type pwMaskPacketConn struct {
	net.PacketConn
}

// pwShortWritePacketConn reports one byte fewer than it forwarded.
type pwShortWritePacketConn struct {
	net.PacketConn
}

func (c *pwShortWritePacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	if err == nil && n > 0 {
		n--
	}
	return n, err
}

func pwTestHandler() *Handler {
	return &Handler{config: &Config{}}
}

func pwNewPacketWriter(tb testing.TB, conn net.Conn, override xnet.Destination, dialDest xnet.Destination) *PacketWriter {
	tb.Helper()
	writer := NewPacketWriter(conn, pwTestHandler(), override, dialDest)
	packetWriter, ok := writer.(*PacketWriter)
	if !ok {
		tb.Fatalf("NewPacketWriter returned %T, want *PacketWriter", writer)
	}
	return packetWriter
}

// pwDatagram returns deterministic datagram bytes.
func pwDatagram(size int, seed byte) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(int(seed) + i)
	}
	return payload
}

// pwUDPBuffer wraps one datagram with destination metadata.
func pwUDPBuffer(payload []byte, dest xnet.Destination) *buf.Buffer {
	b := buf.New()
	if _, err := b.Write(payload); err != nil {
		panic("packet writer fixture payload is too large: " + err.Error())
	}
	b.UDP = &dest
	return b
}

var pwNoOverride = xnet.UDPDestination(nil, 0)

func TestPacketWriterNativeCapabilitySelection(t *testing.T) {
	dest := xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 1}), 53)
	t.Run("native-udp-socket", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		writer := pwNewPacketWriter(t, pwTestWrapper(conn, nil), pwNoOverride, dest)
		if writer.nativeUDPConn != conn {
			t.Fatalf("nativeUDPConn = %v, want the concrete socket", writer.nativeUDPConn)
		}
	})
	t.Run("masked-packet-conn", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		writer := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: conn}, nil), pwNoOverride, dest)
		if writer.nativeUDPConn != nil {
			t.Fatal("native capability leaked through a custom wrapper")
		}
	})
	t.Run("stats-wrapper", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		counter := &pwTestCounter{}
		wrapped := &stat.CounterConnection{Conn: pwTestWrapper(conn, nil), WriteCounter: counter}
		writer := pwNewPacketWriter(t, wrapped, pwNoOverride, dest)
		if writer.nativeUDPConn != conn {
			t.Fatal("stats wrapper hid the native socket")
		}
		if writer.Counter != counter {
			t.Fatal("stats wrapper hid the write counter")
		}
	})
	t.Run("dial-dest-domain-cache-seed", func(t *testing.T) {
		conn := pwTestUDPConn(t, "udp4")
		dialDest := xnet.UDPDestination(xnet.DomainAddress("fixture.example"), 53)
		writer := pwNewPacketWriter(t, pwTestWrapper(conn, nil), pwNoOverride, dialDest)
		if writer.nativeUDPConn != conn {
			t.Fatal("native socket was not selected for a domain dial destination")
		}
		value, ok := writer.ResolvedUDPAddr.Load("fixture.example")
		if !ok || value == nil {
			t.Fatal("dial destination domain was not cached from the remote address")
		}
		if got := value.(xnet.Address).String(); got != "127.0.0.1" {
			t.Fatalf("cached dial address = %s, want 127.0.0.1", got)
		}
	})
	t.Run("non-wrapper-conn", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		writer := NewPacketWriter(client, pwTestHandler(), pwNoOverride, dest)
		if _, ok := writer.(*buf.SequentialWriter); !ok {
			t.Fatalf("NewPacketWriter returned %T, want *buf.SequentialWriter", writer)
		}
	})
}

func TestPacketWriterNativeLoopbackWrites(t *testing.T) {
	sizes := []int{8, 128, 512, 1200, 1400, 2048, 4096, 8192}
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			listener := pwListen(t, network)
			conn := pwTestUDPConn(t, network)
			dest := listener.destination()
			writer := pwNewPacketWriter(t, pwTestWrapper(conn, listener.conn.LocalAddr()), pwNoOverride, dest)
			if writer.nativeUDPConn != conn {
				t.Fatal("native UDP capability not selected")
			}
			counter := &pwTestCounter{}
			writer.Counter = counter

			wantTotal := 0
			for _, size := range sizes {
				payload := pwDatagram(size, byte(size))
				if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, dest)}); err != nil {
					t.Fatalf("size %d: WriteMultiBuffer: %v", size, err)
				}
				if got := listener.receive(t); !bytes.Equal(got, payload) {
					t.Fatalf("size %d: datagram has %d bytes, want the input bytes", size, len(got))
				}
				wantTotal += size
			}
			if counter.total != int64(wantTotal) {
				t.Fatalf("counter = %d, want %d", counter.total, wantTotal)
			}
			if counter.adds != len(sizes) {
				t.Fatalf("counter adds = %d, want %d", counter.adds, len(sizes))
			}
		})
	}
}

func TestPacketWriterNativeMatchesGeneric(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			nativeListener := pwListen(t, network)
			genericListener := pwListen(t, network)
			nativeDest := nativeListener.destination()
			genericDest := genericListener.destination()
			native := pwNewPacketWriter(t, pwTestWrapper(pwTestUDPConn(t, network), nativeListener.conn.LocalAddr()), pwNoOverride, nativeDest)
			generic := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: pwTestUDPConn(t, network)}, genericListener.conn.LocalAddr()), pwNoOverride, genericDest)
			if native.nativeUDPConn == nil {
				t.Fatal("native writer has no native socket")
			}
			if generic.nativeUDPConn != nil {
				t.Fatal("generic writer selected a native socket through a custom wrapper")
			}
			nativeCounter := &pwTestCounter{}
			native.Counter = nativeCounter
			genericCounter := &pwTestCounter{}
			generic.Counter = genericCounter

			for _, size := range []int{8, 512, 1200, 1400, 8192} {
				payload := pwDatagram(size, byte(size))
				if err := native.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, nativeDest)}); err != nil {
					t.Fatalf("native size %d: %v", size, err)
				}
				if err := generic.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, genericDest)}); err != nil {
					t.Fatalf("generic size %d: %v", size, err)
				}
				gotNative := nativeListener.receive(t)
				gotGeneric := genericListener.receive(t)
				if !bytes.Equal(gotNative, payload) || !bytes.Equal(gotGeneric, payload) {
					t.Fatalf("size %d: datagram mismatch", size)
				}
			}
			if nativeCounter.total != genericCounter.total || nativeCounter.adds != genericCounter.adds {
				t.Fatalf("counters differ: native %d/%d, generic %d/%d",
					nativeCounter.total, nativeCounter.adds, genericCounter.total, genericCounter.adds)
			}
		})
	}
}

func TestPacketWriterCustomPacketConnFallback(t *testing.T) {
	listener := pwListen(t, "udp4")
	conn := pwTestUDPConn(t, "udp4")
	dest := listener.destination()
	writer := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: conn}, listener.conn.LocalAddr()), pwNoOverride, dest)
	if writer.nativeUDPConn != nil {
		t.Fatal("custom wrapper selected the native path")
	}
	counter := &pwTestCounter{}
	writer.Counter = counter
	payload := pwDatagram(1200, 3)
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, dest)}); err != nil {
		t.Fatalf("WriteMultiBuffer: %v", err)
	}
	if got := listener.receive(t); !bytes.Equal(got, payload) {
		t.Fatal("fallback datagram mismatch")
	}
	if counter.total != int64(len(payload)) {
		t.Fatalf("counter = %d, want %d", counter.total, len(payload))
	}
}

func TestPacketWriterUDPOverride(t *testing.T) {
	t.Run("address", func(t *testing.T) {
		original := pwListen(t, "udp4")
		overridden := pwListenAt(t, "udp4", net.IPv4(127, 0, 0, 2), original.port())
		originalDest := original.destination()
		conn := pwTestUDPConn(t, "udp4")
		override := xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 2}), 0)
		writer := pwNewPacketWriter(t, pwTestWrapper(conn, original.conn.LocalAddr()), override, originalDest)
		if writer.nativeUDPConn != conn {
			t.Fatal("native UDP capability not selected")
		}
		payload := pwDatagram(256, 5)
		if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, originalDest)}); err != nil {
			t.Fatalf("WriteMultiBuffer: %v", err)
		}
		if got := overridden.receive(t); !bytes.Equal(got, payload) {
			t.Fatal("override destination received the wrong datagram")
		}
		original.receiveNothing(t, 250*time.Millisecond)
	})
	t.Run("port", func(t *testing.T) {
		original := pwListen(t, "udp4")
		overridden := pwListen(t, "udp4")
		originalDest := original.destination()
		conn := pwTestUDPConn(t, "udp4")
		override := xnet.UDPDestination(nil, xnet.Port(overridden.port()))
		writer := pwNewPacketWriter(t, pwTestWrapper(conn, original.conn.LocalAddr()), override, originalDest)
		payload := pwDatagram(256, 6)
		if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, originalDest)}); err != nil {
			t.Fatalf("WriteMultiBuffer: %v", err)
		}
		if got := overridden.receive(t); !bytes.Equal(got, payload) {
			t.Fatal("override port received the wrong datagram")
		}
		original.receiveNothing(t, 250*time.Millisecond)
	})
}

func TestPacketWriterCachedDomainDestination(t *testing.T) {
	for _, native := range []bool{true, false} {
		name := "native"
		if !native {
			name = "generic"
		}
		t.Run(name, func(t *testing.T) {
			listener := pwListen(t, "udp4")
			listenerDest := listener.destination()
			var packetConn net.PacketConn = pwTestUDPConn(t, "udp4")
			if !native {
				packetConn = &pwMaskPacketConn{PacketConn: packetConn}
			}
			writer := pwNewPacketWriter(t, pwTestWrapper(packetConn, listener.conn.LocalAddr()), pwNoOverride, listenerDest)
			if (writer.nativeUDPConn != nil) != native {
				t.Fatalf("nativeUDPConn = %v, want native=%v", writer.nativeUDPConn, native)
			}
			writer.ResolvedUDPAddr.Store("fixture.example", listenerDest.Address)
			domainDest := xnet.UDPDestination(xnet.DomainAddress("fixture.example"), listenerDest.Port)
			counter := &pwTestCounter{}
			writer.Counter = counter
			payload := pwDatagram(512, 9)
			if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, domainDest)}); err != nil {
				t.Fatalf("WriteMultiBuffer: %v", err)
			}
			if got := listener.receive(t); !bytes.Equal(got, payload) {
				t.Fatal("cached domain datagram mismatch")
			}
			if counter.total != int64(len(payload)) {
				t.Fatalf("counter = %d, want %d", counter.total, len(payload))
			}
		})
	}
}

func TestPacketWriterUnresolvedDomainIsDropped(t *testing.T) {
	listener := pwListen(t, "udp4")
	conn := pwTestUDPConn(t, "udp4")
	writer := pwNewPacketWriter(t, pwTestWrapper(conn, listener.conn.LocalAddr()), pwNoOverride, listener.destination())
	writer.ResolvedUDPAddr.Store("fixture.example", nil)
	domainDest := xnet.UDPDestination(xnet.DomainAddress("fixture.example"), 5399)
	counter := &pwTestCounter{}
	writer.Counter = counter
	packet := pwUDPBuffer(pwDatagram(64, 11), domainDest)
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil {
		t.Fatalf("WriteMultiBuffer: %v", err)
	}
	if packet.Cap() != 0 {
		t.Error("dropped packet buffer was not released")
	}
	if counter.total != 0 {
		t.Fatalf("counter = %d, want 0", counter.total)
	}
	listener.receiveNothing(t, 250*time.Millisecond)
}

func TestPacketWriterNilMetadataUnchanged(t *testing.T) {
	listener := pwListen(t, "udp4")
	listenerDest := listener.destination()
	nativeConn := pwTestUDPConn(t, "udp4")
	native := pwNewPacketWriter(t, pwTestWrapper(nativeConn, listener.conn.LocalAddr()), pwNoOverride, listenerDest)
	genericConn := pwTestUDPConn(t, "udp4")
	generic := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: genericConn}, listener.conn.LocalAddr()), pwNoOverride, listenerDest)
	if native.nativeUDPConn == nil || generic.nativeUDPConn != nil {
		t.Fatal("unexpected native capability selection")
	}
	nativeCounter := &pwTestCounter{}
	native.Counter = nativeCounter
	genericCounter := &pwTestCounter{}
	generic.Counter = genericCounter

	payloads := [][]byte{pwDatagram(128, 13), pwDatagram(8192, 14)}
	for i, payload := range payloads {
		buffer := buf.New()
		if _, err := buffer.Write(payload); err != nil {
			t.Fatalf("fill buffer: %v", err)
		}
		if buffer.UDP != nil {
			t.Fatal("fixture buffer unexpectedly has UDP metadata")
		}
		writer := native
		if i == 1 {
			writer = generic
		}
		if err := writer.WriteMultiBuffer(buf.MultiBuffer{buffer}); err != nil {
			t.Fatalf("WriteMultiBuffer: %v", err)
		}
		if got := listener.receive(t); !bytes.Equal(got, payload) {
			t.Fatalf("payload %d: datagram mismatch", i)
		}
	}
	if nativeCounter.total != int64(len(payloads[0])) {
		t.Fatalf("native counter = %d, want %d", nativeCounter.total, len(payloads[0]))
	}
	if genericCounter.total != int64(len(payloads[1])) {
		t.Fatalf("generic counter = %d, want %d", genericCounter.total, len(payloads[1]))
	}
}

func TestPacketWriterWriteFailureReleasesRemaining(t *testing.T) {
	listener := pwListen(t, "udp4")
	dest := listener.destination()
	nativeConn := pwDialUDP(t, dest)
	genericConn := pwDialUDP(t, dest)
	native := pwNewPacketWriter(t, pwTestWrapper(nativeConn, listener.conn.LocalAddr()), pwNoOverride, dest)
	generic := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: genericConn}, listener.conn.LocalAddr()), pwNoOverride, dest)
	if native.nativeUDPConn == nil || generic.nativeUDPConn != nil {
		t.Fatal("unexpected native capability selection")
	}
	nativeCounter := &pwTestCounter{}
	native.Counter = nativeCounter
	genericCounter := &pwTestCounter{}
	generic.Counter = genericCounter

	nativeFirst := pwUDPBuffer(pwDatagram(64, 21), dest)
	nativeSecond := pwUDPBuffer(pwDatagram(64, 22), dest)
	genericFirst := pwUDPBuffer(pwDatagram(64, 23), dest)
	genericSecond := pwUDPBuffer(pwDatagram(64, 24), dest)

	nativeErr := native.WriteMultiBuffer(buf.MultiBuffer{nativeFirst, nativeSecond})
	genericErr := generic.WriteMultiBuffer(buf.MultiBuffer{genericFirst, genericSecond})
	if !errors.Is(nativeErr, net.ErrWriteToConnected) {
		t.Fatalf("native error = %v, want ErrWriteToConnected", nativeErr)
	}
	if !errors.Is(genericErr, net.ErrWriteToConnected) {
		t.Fatalf("generic error = %v, want ErrWriteToConnected", genericErr)
	}
	var nativeOpErr, genericOpErr *net.OpError
	if !errors.As(nativeErr, &nativeOpErr) || !errors.As(genericErr, &genericOpErr) {
		t.Fatalf("errors are not OpError values: %v, %v", nativeErr, genericErr)
	}
	if nativeOpErr.Err != genericOpErr.Err {
		t.Fatalf("error causes differ: %v, %v", nativeOpErr.Err, genericOpErr.Err)
	}
	for i, packet := range []*buf.Buffer{nativeFirst, nativeSecond, genericFirst, genericSecond} {
		if packet.Cap() != 0 {
			t.Errorf("buffer %d was not released", i)
		}
	}
	if nativeCounter.total != 0 || genericCounter.total != 0 {
		t.Fatalf("failed writes counted bytes: %d, %d", nativeCounter.total, genericCounter.total)
	}
	listener.receiveNothing(t, 250*time.Millisecond)
}

func TestPacketWriterFallbackShortWriteCountsReturnedBytes(t *testing.T) {
	listener := pwListen(t, "udp4")
	conn := pwTestUDPConn(t, "udp4")
	dest := listener.destination()
	writer := pwNewPacketWriter(t, pwTestWrapper(&pwShortWritePacketConn{PacketConn: conn}, listener.conn.LocalAddr()), pwNoOverride, dest)
	if writer.nativeUDPConn != nil {
		t.Fatal("short write fixture selected the native path")
	}
	counter := &pwTestCounter{}
	writer.Counter = counter
	payload := pwDatagram(512, 25)
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, dest)}); err != nil {
		t.Fatalf("WriteMultiBuffer: %v", err)
	}
	if got := listener.receive(t); !bytes.Equal(got, payload) {
		t.Fatal("short write fixture datagram mismatch")
	}
	if counter.total != int64(len(payload)-1) {
		t.Fatalf("counter = %d, want the reported %d bytes", counter.total, len(payload)-1)
	}
}

func TestPacketWriterNonUDPNetworkUsesGenericPath(t *testing.T) {
	listener := pwListen(t, "udp4")
	listenerAddr := listener.conn.LocalAddr().(*net.UDPAddr)
	tcpDest := xnet.TCPDestination(xnet.IPAddress(listenerAddr.IP), xnet.Port(listenerAddr.Port))

	nativeConn := pwTestUDPConn(t, "udp4")
	native := pwNewPacketWriter(t, pwTestWrapper(nativeConn, listener.conn.LocalAddr()), pwNoOverride, listener.destination())
	genericConn := pwTestUDPConn(t, "udp4")
	generic := pwNewPacketWriter(t, pwTestWrapper(&pwMaskPacketConn{PacketConn: genericConn}, listener.conn.LocalAddr()), pwNoOverride, listener.destination())
	if native.nativeUDPConn == nil || generic.nativeUDPConn != nil {
		t.Fatal("unexpected native capability selection")
	}

	nativeErr := native.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(pwDatagram(32, 31), tcpDest)})
	genericErr := generic.WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(pwDatagram(32, 32), tcpDest)})
	if nativeErr == nil || genericErr == nil {
		t.Fatalf("TCP network writes to a UDP socket returned %v and %v, want errors", nativeErr, genericErr)
	}
	if !errors.Is(nativeErr, syscall.EINVAL) || !errors.Is(genericErr, syscall.EINVAL) {
		t.Fatalf("error class differs: %v, %v", nativeErr, genericErr)
	}
	var opErr *net.OpError
	if !errors.As(genericErr, &opErr) {
		t.Fatalf("generic error %v is not an OpError", genericErr)
	}
	if _, ok := opErr.Addr.(*net.TCPAddr); !ok {
		t.Fatalf("generic error address = %T, want *net.TCPAddr", opErr.Addr)
	}
	listener.receiveNothing(t, 250*time.Millisecond)
}

func TestPacketWriterConcurrentNativeWriters(t *testing.T) {
	const writers = 4
	const packets = 16
	listeners := make([]*pwTestListener, writers)
	packetWriters := make([]*PacketWriter, writers)
	for i := range listeners {
		listeners[i] = pwListen(t, "udp4")
		conn := pwTestUDPConn(t, "udp4")
		packetWriters[i] = pwNewPacketWriter(t, pwTestWrapper(conn, listeners[i].conn.LocalAddr()), pwNoOverride, listeners[i].destination())
		if packetWriters[i].nativeUDPConn != conn {
			t.Fatal("native UDP capability not selected")
		}
	}

	var wg sync.WaitGroup
	for i := range packetWriters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dest := listeners[i].destination()
			for p := 0; p < packets; p++ {
				payload := pwDatagram(256, byte(10*i+p))
				if err := packetWriters[i].WriteMultiBuffer(buf.MultiBuffer{pwUDPBuffer(payload, dest)}); err != nil {
					t.Errorf("writer %d packet %d: %v", i, p, err)
					return
				}
				got, err := listeners[i].read(3 * time.Second)
				if err != nil {
					t.Errorf("writer %d packet %d: %v", i, p, err)
					return
				}
				if !bytes.Equal(got, payload) {
					t.Errorf("writer %d packet %d: datagram mismatch", i, p)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

var (
	pwBenchmarkAddrSink     net.Addr
	pwBenchmarkAddrPortSink netip.AddrPort
)

// pwWriteFunc is one packet write loop under benchmark: the legacy HEAD
// reference or the current production method.
type pwWriteFunc func(w *PacketWriter, mb buf.MultiBuffer) error

// pwLegacyWriteMultiBuffer is the exact PacketWriter.WriteMultiBuffer body from
// HEAD cd03620eae9e75b146782bfdc229a2abff86f8c8, kept as the benchmark
// baseline. It ignores nativeUDPConn and always uses the generic wrapper, so
// BenchmarkFreedomPacketWriterAddress compares the old write loop against
// current production on equivalent sockets.
func pwLegacyWriteMultiBuffer(w *PacketWriter, mb buf.MultiBuffer) error {
	for {
		mb2, b := buf.SplitFirst(mb)
		mb = mb2
		if b == nil {
			break
		}
		var n int
		var err error
		if b.UDP != nil {
			if w.UDPOverride.Address != nil {
				b.UDP.Address = w.UDPOverride.Address
			}
			if w.UDPOverride.Port != 0 {
				b.UDP.Port = w.UDPOverride.Port
			}
			if b.UDP.Address.Family().IsDomain() {
				if v, ok := w.ResolvedUDPAddr.Load(b.UDP.Address.Domain()); ok {
					// ponytail: sync.Map; stored values are never nil (guard keeps old nil-slot behavior).
					if v != nil {
						b.UDP.Address = v.(xnet.Address)
					} else {
						b.UDP.Address = nil
					}
				} else {
					var ip xnet.Address
					ShouldUseSystemResolver := true
					if w.Handler.config.DomainStrategy.HasStrategy() {
						ips, err := internet.LookupForIP(b.UDP.Address.Domain(), w.Handler.config.DomainStrategy, w.LocalAddr)
						if err != nil {
							// drop packet if resolve failed when forceIP
							if w.Handler.config.DomainStrategy.ForceIP() {
								b.Release()
								continue
							}
						} else {
							ip = xnet.IPAddress(ips[mathrand.Intn(len(ips))])
							ShouldUseSystemResolver = false
						}
					}
					if ShouldUseSystemResolver {
						udpAddr, err := xnet.ResolveUDPAddr("udp", b.UDP.NetAddr())
						if err != nil {
							b.Release()
							continue
						} else {
							ip = xnet.IPAddress(udpAddr.IP)
						}
					}
					if ip != nil {
						if v, _ := w.ResolvedUDPAddr.LoadOrStore(b.UDP.Address.Domain(), ip); v != nil {
							b.UDP.Address = v.(xnet.Address)
						} else {
							b.UDP.Address = nil
						}
					}
				}
			}
			destAddr := b.UDP.RawNetAddr()
			if destAddr == nil {
				b.Release()
				continue
			}
			n, err = w.PacketConnWrapper.WriteTo(b.Bytes(), destAddr)
		} else {
			n, err = w.PacketConnWrapper.Write(b.Bytes())
		}
		b.Release()
		if err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
		if w.Counter != nil {
			w.Counter.Add(int64(n))
		}
	}
	return nil
}

// pwRunLoopbackBenchmark writes one 1200 byte datagram with write and drains
// it. The fixture is identical for the legacy and candidate subcases.
func pwRunLoopbackBenchmark(b *testing.B, writer *PacketWriter, listener *pwTestListener, dest xnet.Destination, write pwWriteFunc) {
	b.Helper()
	payload := pwDatagram(1200, 7)
	receiveBuffer := make([]byte, 65536)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := write(writer, buf.MultiBuffer{pwUDPBuffer(payload, dest)}); err != nil {
			b.Fatalf("WriteMultiBuffer: %v", err)
		}
		got, err := listener.readInto(receiveBuffer, 3*time.Second)
		if err != nil {
			b.Fatalf("read datagram: %v", err)
		}
		if !bytes.Equal(got, payload) {
			b.Fatal("datagram mismatch")
		}
	}
	b.StopTimer()
}

func BenchmarkFreedomPacketWriterAddress(b *testing.B) {
	dest := xnet.UDPDestination(xnet.IPAddress([]byte{127, 0, 0, 1}), 51335)

	b.Run("address-only/baseline-rawnetaddr", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			pwBenchmarkAddrSink = dest.RawNetAddr()
		}
		if pwBenchmarkAddrSink == nil {
			b.Fatal("RawNetAddr returned nil")
		}
	})
	b.Run("address-only/candidate-rawnetaddrport", func(b *testing.B) {
		var ok bool
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			pwBenchmarkAddrPortSink, ok = dest.RawNetAddrPort()
		}
		if !ok {
			b.Fatal("RawNetAddrPort rejected a UDP IPv4 destination")
		}
	})

	for _, network := range []string{"udp4", "udp6"} {
		b.Run("loopback-"+network+"/baseline-legacy-writeto", func(b *testing.B) {
			listener := pwListen(b, network)
			writer := pwNewPacketWriter(b, pwTestWrapper(pwTestUDPConn(b, network), listener.conn.LocalAddr()), pwNoOverride, listener.destination())
			if writer.nativeUDPConn == nil {
				b.Fatal("baseline writer did not hold the native socket")
			}
			writer.Counter = &pwTestCounter{}
			pwRunLoopbackBenchmark(b, writer, listener, listener.destination(), pwLegacyWriteMultiBuffer)
		})
		b.Run("loopback-"+network+"/candidate-native-writetoudpaddrport", func(b *testing.B) {
			listener := pwListen(b, network)
			writer := pwNewPacketWriter(b, pwTestWrapper(pwTestUDPConn(b, network), listener.conn.LocalAddr()), pwNoOverride, listener.destination())
			if writer.nativeUDPConn == nil {
				b.Fatal("candidate writer did not select the native path")
			}
			writer.Counter = &pwTestCounter{}
			pwRunLoopbackBenchmark(b, writer, listener, listener.destination(), (*PacketWriter).WriteMultiBuffer)
		})
	}

	b.Run("control-nil-metadata/baseline-legacy-write", func(b *testing.B) {
		listener := pwListen(b, "udp4")
		writer := pwNewPacketWriter(b, pwTestWrapper(pwTestUDPConn(b, "udp4"), listener.conn.LocalAddr()), pwNoOverride, listener.destination())
		if writer.nativeUDPConn == nil {
			b.Fatal("baseline writer did not hold the native socket")
		}
		writer.Counter = &pwTestCounter{}
		pwRunNilMetadataBenchmark(b, writer, listener, pwLegacyWriteMultiBuffer)
	})
	b.Run("control-nil-metadata/candidate-native-write", func(b *testing.B) {
		listener := pwListen(b, "udp4")
		writer := pwNewPacketWriter(b, pwTestWrapper(pwTestUDPConn(b, "udp4"), listener.conn.LocalAddr()), pwNoOverride, listener.destination())
		if writer.nativeUDPConn == nil {
			b.Fatal("candidate writer did not select the native path")
		}
		writer.Counter = &pwTestCounter{}
		pwRunNilMetadataBenchmark(b, writer, listener, (*PacketWriter).WriteMultiBuffer)
	})
	b.Run("control-custom-conn-fallback/baseline-legacy-writeto", func(b *testing.B) {
		listener := pwListen(b, "udp4")
		writer := pwNewPacketWriter(b, pwTestWrapper(&pwMaskPacketConn{PacketConn: pwTestUDPConn(b, "udp4")}, listener.conn.LocalAddr()), pwNoOverride, listener.destination())
		if writer.nativeUDPConn != nil {
			b.Fatal("fallback baseline selected the native path")
		}
		writer.Counter = &pwTestCounter{}
		pwRunLoopbackBenchmark(b, writer, listener, listener.destination(), pwLegacyWriteMultiBuffer)
	})
	b.Run("control-custom-conn-fallback/candidate-generic-writeto", func(b *testing.B) {
		listener := pwListen(b, "udp4")
		writer := pwNewPacketWriter(b, pwTestWrapper(&pwMaskPacketConn{PacketConn: pwTestUDPConn(b, "udp4")}, listener.conn.LocalAddr()), pwNoOverride, listener.destination())
		if writer.nativeUDPConn != nil {
			b.Fatal("fallback candidate selected the native path")
		}
		writer.Counter = &pwTestCounter{}
		pwRunLoopbackBenchmark(b, writer, listener, listener.destination(), (*PacketWriter).WriteMultiBuffer)
	})
}

// pwRunNilMetadataBenchmark writes one datagram without UDP metadata through
// the wrapper Write method and drains it.
func pwRunNilMetadataBenchmark(b *testing.B, writer *PacketWriter, listener *pwTestListener, write pwWriteFunc) {
	b.Helper()
	payload := pwDatagram(1200, 8)
	receiveBuffer := make([]byte, 65536)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer := buf.New()
		if _, err := buffer.Write(payload); err != nil {
			b.Fatalf("fill buffer: %v", err)
		}
		if err := write(writer, buf.MultiBuffer{buffer}); err != nil {
			b.Fatalf("WriteMultiBuffer: %v", err)
		}
		got, err := listener.readInto(receiveBuffer, 3*time.Second)
		if err != nil {
			b.Fatalf("read datagram: %v", err)
		}
		if !bytes.Equal(got, payload) {
			b.Fatal("datagram mismatch")
		}
	}
	b.StopTimer()
}
