package mux

import (
	"encoding/binary"
	"io"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/crypto"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
)

// PacketReader is an io.Reader that reads whole chunk of Mux frames every time.
type PacketReader struct {
	reader io.Reader
	eof    bool
	dest   *net.Destination
}

// NewPacketReader creates a new PacketReader.
func NewPacketReader(reader io.Reader, dest *net.Destination) *PacketReader {
	return &PacketReader{
		reader: reader,
		eof:    false,
		dest:   dest,
	}
}

// ReadMultiBuffer implements buf.Reader.
func (r *PacketReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.eof {
		return nil, io.EOF
	}

	var sizeBytes [2]byte
	if _, err := io.ReadFull(r.reader, sizeBytes[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint16(sizeBytes[:])

	if size > buf.Size {
		return nil, errors.New("packet size too large: ", size)
	}

	b := buf.New()
	if _, err := b.ReadFullFrom(r.reader, int32(size)); err != nil {
		b.Release()
		return nil, err
	}
	r.eof = true
	if r.dest != nil && r.dest.Network == net.Network_UDP {
		b.UDP = r.dest
	}
	return buf.MultiBuffer{b}, nil
}

// NewStreamReader creates a new StreamReader.
func NewStreamReader(reader *buf.BufferedReader) buf.Reader {
	return crypto.NewChunkStreamReaderWithChunkCount(reader, 1)
}
