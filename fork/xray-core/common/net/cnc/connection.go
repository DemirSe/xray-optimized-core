package cnc

import (
	"context"
	"io"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
)

type ConnectionOption func(*Connection)

func ConnectionLocalAddr(a net.Addr) ConnectionOption {
	return func(c *Connection) {
		c.local = a
	}
}

func ConnectionRemoteAddr(a net.Addr) ConnectionOption {
	return func(c *Connection) {
		c.remote = a
	}
}

func ConnectionInput(writer io.Writer) ConnectionOption {
	return func(c *Connection) {
		c.writer = buf.NewWriter(writer)
	}
}

func ConnectionInputMulti(writer buf.Writer) ConnectionOption {
	return func(c *Connection) {
		c.writer = writer
	}
}

func ConnectionOutput(reader io.Reader) ConnectionOption {
	return func(c *Connection) {
		c.reader = &buf.BufferedReader{Reader: buf.NewReader(reader)}
	}
}

func ConnectionOutputMulti(reader buf.Reader) ConnectionOption {
	return func(c *Connection) {
		c.reader = &buf.BufferedReader{Reader: reader}
	}
}

func ConnectionOutputMultiUDP(reader buf.Reader) ConnectionOption {
	return func(c *Connection) {
		c.reader = &buf.BufferedReader{
			Reader:   reader,
			Splitter: buf.SplitFirstBytes,
		}
	}
}

func ConnectionOnClose(n io.Closer) ConnectionOption {
	return func(c *Connection) {
		c.onClose = n
	}
}

func NewConnection(opts ...ConnectionOption) net.Conn {
	doneCtx, doneCancel := context.WithCancel(context.Background())
	c := &Connection{
		doneCtx:    doneCtx,
		doneCancel: doneCancel,
		local: &net.TCPAddr{
			IP:   []byte{0, 0, 0, 0},
			Port: 0,
		},
		remote: &net.TCPAddr{
			IP:   []byte{0, 0, 0, 0},
			Port: 0,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

type Connection struct {
	reader     *buf.BufferedReader
	writer     buf.Writer
	doneCtx    context.Context
	doneCancel context.CancelFunc
	onClose    io.Closer
	local      net.Addr
	remote     net.Addr
}

func (c *Connection) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

// ReadMultiBuffer implements buf.Reader.
func (c *Connection) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return c.reader.ReadMultiBuffer()
}

// Write implements net.Conn.Write().
func (c *Connection) Write(b []byte) (int, error) {
	if c.doneCtx.Err() != nil {
		return 0, io.ErrClosedPipe
	}

	l := len(b)
	mb := make(buf.MultiBuffer, 0, l/buf.Size+1)
	mb = buf.MergeBytes(mb, b)
	return l, c.writer.WriteMultiBuffer(mb)
}

func (c *Connection) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if c.doneCtx.Err() != nil {
		buf.ReleaseMulti(mb)
		return io.ErrClosedPipe
	}

	return c.writer.WriteMultiBuffer(mb)
}

// Close implements net.Conn.Close().
func (c *Connection) Close() error {
	c.doneCancel()
	common.Interrupt(c.reader)
	common.Close(c.writer)
	if c.onClose != nil {
		return c.onClose.Close()
	}

	return nil
}

// LocalAddr implements net.Conn.LocalAddr().
func (c *Connection) LocalAddr() net.Addr {
	return c.local
}

// RemoteAddr implements net.Conn.RemoteAddr().
func (c *Connection) RemoteAddr() net.Addr {
	return c.remote
}

// SetDeadline implements net.Conn.SetDeadline().
func (c *Connection) SetDeadline(t time.Time) error {
	return nil
}

// SetReadDeadline implements net.Conn.SetReadDeadline().
func (c *Connection) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline implements net.Conn.SetWriteDeadline().
func (c *Connection) SetWriteDeadline(t time.Time) error {
	return nil
}
