package inbound

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xerrors "github.com/xtls/xray-core/common/errors"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vless/encoding"
	"github.com/xtls/xray-core/transport"
	"google.golang.org/protobuf/proto"
)

var testConnAddr = &stdnet.TCPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 1}

// readStep is one scripted Read result.
type readStep struct {
	data []byte
	err  error
}

// scriptedConn is a deterministic net.Conn. Each Read uses one scripted step.
type scriptedConn struct {
	mu      sync.Mutex
	steps   []readStep
	written bytes.Buffer
	closed  bool
}

func newScriptedConn(steps ...readStep) *scriptedConn {
	return &scriptedConn{steps: steps}
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, stdnet.ErrClosed
	}
	if len(c.steps) == 0 {
		return 0, io.EOF
	}
	step := c.steps[0]
	n := copy(p, step.data)
	if n < len(step.data) {
		// Keep the rest of the step and its error for the next Read.
		c.steps[0] = readStep{data: step.data[n:], err: step.err}
		return n, nil
	}
	c.steps = c.steps[1:]
	return n, step.err
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, stdnet.ErrClosed
	}
	return c.written.Write(p)
}

func (c *scriptedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *scriptedConn) LocalAddr() stdnet.Addr           { return testConnAddr }
func (c *scriptedConn) RemoteAddr() stdnet.Addr          { return testConnAddr }
func (c *scriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedConn) SetWriteDeadline(time.Time) error { return nil }

// testDispatcher is a minimal routing.Dispatcher.
type testDispatcher struct {
	fn func(ctx context.Context, dest xnet.Destination, link *transport.Link) error
}

func (d *testDispatcher) Dispatch(context.Context, xnet.Destination) (*transport.Link, error) {
	return nil, errors.New("testDispatcher.Dispatch is not used")
}

func (d *testDispatcher) DispatchLink(ctx context.Context, dest xnet.Destination, link *transport.Link) error {
	if d.fn == nil {
		return nil
	}
	return d.fn(ctx, dest, link)
}

func (d *testDispatcher) Start() error      { return nil }
func (d *testDispatcher) Close() error      { return nil }
func (d *testDispatcher) Type() interface{} { return routing.DispatcherType() }

type testPolicyManager struct{}

func (testPolicyManager) ForLevel(uint32) policy.Session {
	return policy.Session{
		Timeouts: policy.Timeout{
			Handshake:      5 * time.Second,
			ConnectionIdle: 30 * time.Second,
			UplinkOnly:     5 * time.Second,
			DownlinkOnly:   5 * time.Second,
		},
	}
}

func (testPolicyManager) ForSystem() policy.System { return policy.System{} }
func (testPolicyManager) Start() error             { return nil }
func (testPolicyManager) Close() error             { return nil }
func (testPolicyManager) Type() interface{}        { return policy.ManagerType() }

type testUser struct {
	user *protocol.MemoryUser
}

func newTestUser(tb testing.TB, flow string) *testUser {
	tb.Helper()
	account := &vless.MemoryAccount{ID: protocol.NewID(uuid.New()), Flow: flow}
	return &testUser{user: &protocol.MemoryUser{Account: account}}
}

func newTestHandler(tb testing.TB, users ...*testUser) *Handler {
	tb.Helper()
	validator := new(vless.MemoryValidator)
	for _, u := range users {
		if err := validator.Add(u.user); err != nil {
			tb.Fatalf("failed to add the test user: %v", err)
		}
	}
	return &Handler{
		policyManager: testPolicyManager{},
		validator:     validator,
		ctx:           context.Background(),
	}
}

func testContext() context.Context {
	return session.ContextWithInbound(context.Background(), &session.Inbound{})
}

func encodeTestRequest(tb testing.TB, user *testUser, command protocol.RequestCommand, address xnet.Address, port xnet.Port, addons *encoding.Addons) []byte {
	tb.Helper()
	var out bytes.Buffer
	request := &protocol.RequestHeader{
		Version: encoding.Version,
		Command: command,
		Address: address,
		Port:    port,
		User:    user.user,
	}
	if err := encoding.EncodeRequestHeader(&out, request, addons); err != nil {
		tb.Fatalf("failed to encode the test request: %v", err)
	}
	return out.Bytes()
}

// readPayload copies the next MultiBuffer from the reader and releases it.
func readPayload(tb testing.TB, reader buf.Reader) ([]byte, error) {
	tb.Helper()
	mb, err := reader.ReadMultiBuffer()
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, b := range mb {
		out = append(out, b.Bytes()...)
	}
	buf.ReleaseMulti(mb)
	return out, nil
}

func TestProcessInitialReadFailure(t *testing.T) {
	readErr := errors.New("scripted read failure")

	t.Run("clean failure", func(t *testing.T) {
		handler := newTestHandler(t)
		conn := newScriptedConn(readStep{err: readErr})
		err := handler.Process(testContext(), xnet.Network_TCP, conn, &testDispatcher{})
		if err != readErr {
			t.Fatalf("Process error = %v, want %v", err, readErr)
		}
	})

	t.Run("partial read then failure", func(t *testing.T) {
		handler := newTestHandler(t)
		conn := newScriptedConn(readStep{data: []byte{0x00, 0x01, 0x02}, err: readErr})
		err := handler.Process(testContext(), xnet.Network_TCP, conn, &testDispatcher{})
		if err != readErr {
			t.Fatalf("Process error = %v, want %v", err, readErr)
		}
	})
}

func TestProcessHeaderAndPayloadInOneRead(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	payload := []byte("payload that arrives with the request header")
	fixture := append(append([]byte{}, header...), payload...)

	var held *transport.Link
	dispatcher := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		held = link
		return nil
	}}

	if err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: fixture}), dispatcher); err != nil {
		t.Fatalf("Process failed: %v", err)
	}
	if held == nil {
		t.Fatal("the dispatcher did not receive a link")
	}
	bufferedReader, ok := held.Reader.(*buf.BufferedReader)
	if !ok {
		t.Fatalf("the consumer reader type = %T, want *buf.BufferedReader", held.Reader)
	}
	if len(bufferedReader.Buffer) == 0 {
		t.Fatal("the reader cache is empty before the consumer read")
	}
	cached := bufferedReader.Buffer[0]

	// The consumer reads the cached bytes after Process returned.
	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the consumer read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the consumer read %q, want %q", got, payload)
	}

	// The consumer read and released the cache. The original initial buffer is
	// empty, and its storage is back in the pool.
	if cached.Len() != 0 || cached.Cap() != 0 {
		t.Fatalf("the consumed cache buffer = len %d, cap %d, want len 0, cap 0 after release", cached.Len(), cached.Cap())
	}

	if _, err := held.Reader.ReadMultiBuffer(); xerrors.Cause(err) != io.EOF {
		t.Fatalf("the second consumer read = %v, want EOF", err)
	}
}

func TestProcessHeaderSplitAcrossReads(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	payload := []byte("split-header payload")

	steps := make([]readStep, 0, len(header)+1)
	for i := range header {
		steps = append(steps, readStep{data: header[i : i+1]})
	}
	steps = append(steps, readStep{data: payload})

	var held *transport.Link
	dispatcher := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		held = link
		return nil
	}}

	if err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(steps...), dispatcher); err != nil {
		t.Fatalf("Process failed: %v", err)
	}
	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the consumer read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the consumer read %q, want %q", got, payload)
	}
}

func TestProcessRejectsInvalidVersion(t *testing.T) {
	user := newTestUser(t, "")
	raw := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	raw[0] = 0x05

	err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
	if err == nil || !strings.Contains(err.Error(), "invalid request version") {
		t.Fatalf("Process error = %v, want an invalid version error", err)
	}
}

func TestProcessRejectsInvalidUUID(t *testing.T) {
	user := newTestUser(t, "")
	raw := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	raw[1] ^= 0xff

	err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
	if err == nil || !strings.Contains(err.Error(), "invalid request user id") {
		t.Fatalf("Process error = %v, want an invalid user id error", err)
	}
}

func TestProcessRejectsInvalidAddons(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})

	// The version byte and the user ID use the first 17 bytes.
	// The next byte is the addons length. A length of 255 needs more data.
	raw := append([]byte{}, header[:18]...)
	raw[17] = 0xff

	err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
	if err == nil || !strings.Contains(err.Error(), "failed to read addons protobuf value") {
		t.Fatalf("Process error = %v, want an addons read error", err)
	}
}

func TestProcessRejectsUnknownFlow(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	addonsBytes, err := proto.Marshal(&encoding.Addons{Flow: "unknown-flow"})
	if err != nil {
		t.Fatalf("failed to marshal the test addons: %v", err)
	}

	// Splice the addons value between the user ID and the command.
	raw := append([]byte{}, header[:17]...)
	raw = append(raw, byte(len(addonsBytes)))
	raw = append(raw, addonsBytes...)
	raw = append(raw, header[18:]...)

	err = newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
	if err == nil || !strings.Contains(err.Error(), "unknown request flow") {
		t.Fatalf("Process error = %v, want an unknown flow error", err)
	}
}

func TestProcessRejectsFlowMismatch(t *testing.T) {
	address := xnet.ParseAddress("127.0.0.1")
	port := xnet.Port(443)

	t.Run("account flow without request flow", func(t *testing.T) {
		user := newTestUser(t, vless.XRV)
		raw := encodeTestRequest(t, user, protocol.RequestCommandTCP, address, port, &encoding.Addons{})
		err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
		if err == nil || !strings.Contains(err.Error(), "client flow is empty") {
			t.Fatalf("Process error = %v, want an empty client flow error", err)
		}
	})

	t.Run("request flow without account flow", func(t *testing.T) {
		user := newTestUser(t, "")
		raw := encodeTestRequest(t, user, protocol.RequestCommandTCP, address, port, &encoding.Addons{Flow: vless.XRV})
		err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
		if err == nil || !strings.Contains(err.Error(), "is not able to use the flow") {
			t.Fatalf("Process error = %v, want a flow rejection error", err)
		}
	})
}

// TestProcessMuxLateFirstInspection covers isMuxAndNotXUDP, which inspects the
// first buffer after request decoding consumed part of it.
func TestProcessMuxLateFirstInspection(t *testing.T) {
	// An XUDP frame with the network type 2 at byte 6.
	xudpFrame := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x02, 0x0f}

	t.Run("header fully consumed is rejected", func(t *testing.T) {
		user := newTestUser(t, vless.XRV)
		header := encodeTestRequest(t, user, protocol.RequestCommandMux, nil, 0, &encoding.Addons{})
		err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: header}), &testDispatcher{})
		if err == nil || !strings.Contains(err.Error(), "client flow is empty") {
			t.Fatalf("Process error = %v, want an empty client flow error", err)
		}
	})

	t.Run("short frame is rejected", func(t *testing.T) {
		user := newTestUser(t, vless.XRV)
		header := encodeTestRequest(t, user, protocol.RequestCommandMux, nil, 0, &encoding.Addons{})
		raw := append(append([]byte{}, header...), xudpFrame[:4]...)
		err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), &testDispatcher{})
		if err == nil || !strings.Contains(err.Error(), "client flow is empty") {
			t.Fatalf("Process error = %v, want an empty client flow error", err)
		}
	})

	t.Run("xudp frame is dispatched", func(t *testing.T) {
		user := newTestUser(t, vless.XRV)
		header := encodeTestRequest(t, user, protocol.RequestCommandMux, nil, 0, &encoding.Addons{})
		raw := append(append([]byte{}, header...), xudpFrame...)

		var held *transport.Link
		dispatcher := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
			held = link
			return nil
		}}
		if err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: raw}), dispatcher); err != nil {
			t.Fatalf("Process failed: %v", err)
		}
		got, err := readPayload(t, held.Reader)
		if err != nil {
			t.Fatalf("the consumer read failed: %v", err)
		}
		if !bytes.Equal(got, xudpFrame) {
			t.Fatalf("the consumer read %q, want %q", got, xudpFrame)
		}
	})
}

func TestProcessUDPPacketHandoff(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandUDP, xnet.ParseAddress("1.2.3.4"), xnet.Port(53), &encoding.Addons{})
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	fixture := append(append([]byte{}, header...), 0x00, byte(len(payload)))
	fixture = append(fixture, payload...)

	var held *transport.Link
	dispatcher := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		held = link
		return nil
	}}
	if err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: fixture}), dispatcher); err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// The length-packet reader consumes the cached first bytes through Read.
	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the consumer read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the consumer read %q, want %q", got, payload)
	}
}

func TestProcessDispatchFailureKeepsReaderCache(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	payload := []byte("payload held after a failed dispatch")
	fixture := append(append([]byte{}, header...), payload...)

	dispatchErr := errors.New("dispatch failed")
	var held *transport.Link
	dispatcher := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		held = link
		return dispatchErr
	}}

	err := newTestHandler(t, user).Process(testContext(), xnet.Network_TCP, newScriptedConn(readStep{data: fixture}), dispatcher)
	if err == nil || !strings.Contains(err.Error(), "failed to dispatch request") {
		t.Fatalf("Process error = %v, want a dispatch error", err)
	}

	// The consumer still owns the cache after Process returned an error.
	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the consumer read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the consumer read %q, want %q", got, payload)
	}
}

// TestProcessCanceledDispatchKeepsUnreadCache cancels the context inside
// DispatchLink. The consumer never reads the cached payload. The unread cache
// must survive the cancellation and later pool activity.
func TestProcessCanceledDispatchKeepsUnreadCache(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	payload := []byte("unread payload of a canceled dispatch")
	fixture := append(append([]byte{}, header...), payload...)
	handler := newTestHandler(t, user)

	ctx, cancel := context.WithCancel(testContext())
	defer cancel()

	var held *transport.Link
	dispatcher := &testDispatcher{fn: func(ctx context.Context, _ xnet.Destination, link *transport.Link) error {
		held = link
		cancel()
		return ctx.Err()
	}}

	err := handler.Process(ctx, xnet.Network_TCP, newScriptedConn(readStep{data: fixture}), dispatcher)
	if err == nil || !strings.Contains(err.Error(), "failed to dispatch request") {
		t.Fatalf("Process error = %v, want a dispatch error", err)
	}
	if cause := xerrors.Cause(err); cause != context.Canceled {
		t.Fatalf("Process error cause = %v, want context.Canceled", cause)
	}

	// Complete connections recycle their buffers before the held consumer reads.
	otherPayload := []byte("payload of a complete connection")
	consume := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		got, err := readPayload(t, link.Reader)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, otherPayload) {
			return errors.New("the other connection read the wrong payload")
		}
		return nil
	}}
	for i := 0; i < 8; i++ {
		if err := handler.Process(testContext(), xnet.Network_TCP,
			newScriptedConn(readStep{data: append(append([]byte{}, header...), otherPayload...)}), consume); err != nil {
			t.Fatalf("Process failed on connection %d: %v", i, err)
		}
	}

	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the held consumer read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the held consumer read %q, want %q", got, payload)
	}
}

func TestProcessHeldCacheSurvivesOtherConnections(t *testing.T) {
	user := newTestUser(t, "")
	header := encodeTestRequest(t, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	heldPayload := []byte("payload of the held connection")
	otherPayload := []byte("payload of a complete connection")
	handler := newTestHandler(t, user)

	var held *transport.Link
	if err := handler.Process(testContext(), xnet.Network_TCP,
		newScriptedConn(readStep{data: append(append([]byte{}, header...), heldPayload...)}),
		&testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
			held = link
			return nil
		}}); err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Complete connections recycle their buffers while the first connection
	// holds its cached payload.
	consume := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		got, err := readPayload(t, link.Reader)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, otherPayload) {
			return errors.New("the other connection read the wrong payload")
		}
		return nil
	}}
	for i := 0; i < 32; i++ {
		if err := handler.Process(testContext(), xnet.Network_TCP,
			newScriptedConn(readStep{data: append(append([]byte{}, header...), otherPayload...)}), consume); err != nil {
			t.Fatalf("Process failed on connection %d: %v", i, err)
		}
	}

	got, err := readPayload(t, held.Reader)
	if err != nil {
		t.Fatalf("the held consumer read failed: %v", err)
	}
	if !bytes.Equal(got, heldPayload) {
		t.Fatalf("the held consumer read %q, want %q", got, heldPayload)
	}
}

func newFallbackHandler(tb testing.TB, dest string) *Handler {
	tb.Helper()
	handler := newTestHandler(tb)
	handler.fallbacks = map[string]map[string]map[string]*Fallback{
		"": {"": {"": {Type: "tcp", Dest: dest}}},
	}
	return handler
}

func TestFallbackForwardsInitialBytes(t *testing.T) {
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open the fallback listener: %v", err)
	}
	defer listener.Close()

	payload := append([]byte{0x02}, bytes.Repeat([]byte("fallback-"), 6)...)
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		data := make([]byte, len(payload))
		_, err = io.ReadFull(conn, data)
		conn.Close()
		if err != nil {
			return
		}
		received <- data
	}()

	handler := newFallbackHandler(t, listener.Addr().String())
	client, server := stdnet.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		client.Write(payload)
		client.Close()
	}()

	processErr := handler.Process(testContext(), xnet.Network_TCP, server, &testDispatcher{})

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Fatalf("the fallback received %q, want %q", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout while waiting for the fallback bytes")
	}
	if processErr != nil && !strings.Contains(processErr.Error(), "fallback ends") {
		t.Fatalf("Process error = %v, want nil or a fallback end error", processErr)
	}
}

// TestFallbackCancelWhileReaderHeld cancels the connection while the fallback
// copy still holds the reader. The handler must not release the reader cache.
func TestFallbackCancelWhileReaderHeld(t *testing.T) {
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open the fallback listener: %v", err)
	}
	defer listener.Close()

	payload := append([]byte{0x02}, bytes.Repeat([]byte("held-"), 8)...)
	forwarded := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, data); err != nil {
			return
		}
		forwarded <- data
		<-done
	}()

	handler := newFallbackHandler(t, listener.Addr().String())
	ctx, cancel := context.WithCancel(testContext())
	defer cancel()

	client, server := stdnet.Pipe()
	defer client.Close()
	defer server.Close()
	defer close(done)
	go func() {
		client.Write(payload)
	}()

	result := make(chan error, 1)
	go func() {
		result <- handler.Process(ctx, xnet.Network_TCP, server, &testDispatcher{})
	}()

	select {
	case got := <-forwarded:
		if !bytes.Equal(got, payload) {
			t.Fatalf("the fallback received %q, want %q", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout while waiting for the fallback bytes")
	}
	cancel()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "fallback ends") {
			t.Fatalf("Process error = %v, want a fallback end error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout while waiting for cancellation")
	}
}

func BenchmarkProcessInitialReadHandoff(b *testing.B) {
	user := newTestUser(b, "")
	header := encodeTestRequest(b, user, protocol.RequestCommandTCP, xnet.ParseAddress("127.0.0.1"), xnet.Port(443), &encoding.Addons{})
	payload := bytes.Repeat([]byte("payload"), 16)
	fixture := append(append([]byte{}, header...), payload...)
	handler := newTestHandler(b, user)
	ctx := testContext()

	consume := &testDispatcher{fn: func(_ context.Context, _ xnet.Destination, link *transport.Link) error {
		mb, err := link.Reader.ReadMultiBuffer()
		if err != nil {
			return err
		}
		if mb.Len() != int32(len(payload)) {
			return errors.New("unexpected payload length")
		}
		buf.ReleaseMulti(mb)
		return nil
	}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := handler.Process(ctx, xnet.Network_TCP, newScriptedConn(readStep{data: fixture}), consume); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProcessInitialReadFailure(b *testing.B) {
	handler := newTestHandler(b)
	ctx := testContext()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// The first read fails. The handler releases the buffer before it
		// returns the error.
		conn := newScriptedConn(readStep{err: io.EOF})
		if err := handler.Process(ctx, xnet.Network_TCP, conn, &testDispatcher{}); err != io.EOF {
			b.Fatalf("Process error = %v, want EOF", err)
		}
	}
}
