package pipe

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/features/policy"
)

// New creates a new Reader and Writer that connects to each other.
// limit is the maximum buffer size in bytes; -1 means no size limit.
// discard, when true, discards writes if the buffer is full instead of blocking.
func New(limit int32, discard bool) (*Reader, *Writer) {
	doneCtx, doneCancel := context.WithCancel(context.Background())
	p := &pipe{
		readSignal:  signal.NewNotifier(),
		writeSignal: signal.NewNotifier(),
		doneCtx:     doneCtx,
		doneCancel:  doneCancel,
		errChan:     make(chan error, 1),
		option: pipeOption{
			limit:           limit,
			discardOverflow: discard,
		},
	}

	return &Reader{
			pipe: p,
		}, &Writer{
			pipe: p,
		}
}

// NewFromContext creates a new Reader and Writer with the buffer limit from context policy.
func NewFromContext(ctx context.Context) (*Reader, *Writer) {
	limit := policy.BufferPolicyFromContext(ctx).PerConnection
	if limit < 0 {
		limit = -1
	}
	return New(limit, false)
}

type state byte

const (
	open state = iota
	closed
	errord
)

type pipeOption struct {
	limit           int32 // maximum buffer size in bytes
	discardOverflow bool
}

func (o *pipeOption) isFull(curSize int32) bool {
	return o.limit >= 0 && curSize > o.limit
}

type pipe struct {
	sync.Mutex
	data        buf.MultiBuffer
	readSignal  *signal.Notifier
	writeSignal *signal.Notifier
	doneCtx     context.Context
	doneCancel  context.CancelFunc
	errChan     chan error
	option      pipeOption
	state       state
}

var errBufferFull = errors.New("buffer full")

func (p *pipe) Len() int32 {
	data := p.data
	if data == nil {
		return 0
	}
	return data.Len()
}

func (p *pipe) getState(forRead bool) error {
	switch p.state {
	case open:
		if !forRead && p.option.isFull(p.data.Len()) {
			return errBufferFull
		}
		return nil
	case closed:
		if !forRead {
			return io.ErrClosedPipe
		}
		if !p.data.IsEmpty() {
			return nil
		}
		return io.EOF
	case errord:
		return io.ErrClosedPipe
	default:
		panic("impossible case")
	}
}

func (p *pipe) readMultiBufferInternal() (buf.MultiBuffer, error) {
	p.Lock()
	defer p.Unlock()

	if err := p.getState(true); err != nil {
		return nil, err
	}

	data := p.data
	p.data = nil
	return data, nil
}

func (p *pipe) ReadMultiBuffer() (buf.MultiBuffer, error) {
	for {
		data, err := p.readMultiBufferInternal()
		if data != nil || err != nil {
			p.writeSignal.Signal()
			return data, err
		}

		select {
		case <-p.readSignal.Wait():
		case <-p.doneCtx.Done():
		case err = <-p.errChan:
			return nil, err
		}
	}
}

func (p *pipe) ReadMultiBufferTimeout(d time.Duration) (buf.MultiBuffer, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	for {
		data, err := p.readMultiBufferInternal()
		if data != nil || err != nil {
			p.writeSignal.Signal()
			return data, err
		}

		select {
		case <-p.readSignal.Wait():
		case <-p.doneCtx.Done():
		case <-timer.C:
			return nil, buf.ErrReadTimeout
		}
	}
}

func (p *pipe) writeMultiBufferInternal(mb buf.MultiBuffer) error {
	p.Lock()
	defer p.Unlock()

	if err := p.getState(false); err != nil {
		return err
	}

	if p.data == nil {
		p.data = mb
	} else {
		p.data, _ = buf.MergeMulti(p.data, mb)
	}
	return nil
}

func (p *pipe) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if mb.IsEmpty() {
		return nil
	}

	for {
		err := p.writeMultiBufferInternal(mb)
		if err == nil {
			p.readSignal.Signal()
			return nil
		}

		if err == errBufferFull {
			if p.option.discardOverflow {
				buf.ReleaseMulti(mb)
				return nil
			}
			select {
			case <-p.writeSignal.Wait():
				continue
			case <-p.doneCtx.Done():
				buf.ReleaseMulti(mb)
				return io.ErrClosedPipe
			}
		}

		buf.ReleaseMulti(mb)
		p.readSignal.Signal()
		return err
	}
}

func (p *pipe) Close() error {
	p.Lock()
	defer p.Unlock()

	if p.state == closed || p.state == errord {
		return nil
	}

	p.state = closed
	p.doneCancel()
	return nil
}

// Interrupt implements common.Interruptible.
func (p *pipe) Interrupt() {
	p.Lock()
	defer p.Unlock()

	if !p.data.IsEmpty() {
		buf.ReleaseMulti(p.data)
		p.data = nil
		if p.state == closed {
			p.state = errord
		}
	}

	if p.state == closed || p.state == errord {
		return
	}

	p.state = errord

	p.doneCancel()
}

// Reader is a buf.Reader that reads content from a pipe.
type Reader struct {
	pipe *pipe
}

// ReadMultiBuffer implements buf.Reader.
func (r *Reader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return r.pipe.ReadMultiBuffer()
}

// ReadMultiBufferTimeout reads content from a pipe within the given duration, or returns buf.ErrTimeout otherwise.
func (r *Reader) ReadMultiBufferTimeout(d time.Duration) (buf.MultiBuffer, error) {
	return r.pipe.ReadMultiBufferTimeout(d)
}

// Interrupt implements common.Interruptible.
func (r *Reader) Interrupt() {
	r.pipe.Interrupt()
}

// ReturnAnError makes ReadMultiBuffer return an error, only once.
func (r *Reader) ReturnAnError(err error) {
	r.pipe.errChan <- err
}

// Recover catches an error set by ReturnAnError, if exists.
func (r *Reader) Recover() (err error) {
	select {
	case err = <-r.pipe.errChan:
	default:
	}
	return
}

// Writer is a buf.Writer that writes data into a pipe.
type Writer struct {
	pipe *pipe
}

// WriteMultiBuffer implements buf.Writer.
func (w *Writer) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.pipe.WriteMultiBuffer(mb)
}

// Close implements io.Closer. After the pipe is closed, writing to the pipe will return io.ErrClosedPipe, while reading will return io.EOF.
func (w *Writer) Close() error {
	return w.pipe.Close()
}

func (w *Writer) Len() int32 {
	return w.pipe.Len()
}

// Interrupt implements common.Interruptible.
func (w *Writer) Interrupt() {
	w.pipe.Interrupt()
}
