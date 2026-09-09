package crypto

import (
	"encoding/binary"
	"io"

	"github.com/xtls/xray-core/common/buf"
)

// ponytail: fixed 2-byte big-endian sizes; the 3 interfaces had one caller.

type ChunkStreamReader struct {
	reader *buf.BufferedReader

	buffer       []byte
	leftOverSize int32
	maxNumChunk  uint32
	numChunk     uint32
}

func NewChunkStreamReaderWithChunkCount(reader io.Reader, maxNumChunk uint32) *ChunkStreamReader {
	r := &ChunkStreamReader{
		buffer:      make([]byte, 2),
		maxNumChunk: maxNumChunk,
	}
	if breader, ok := reader.(*buf.BufferedReader); ok {
		r.reader = breader
	} else {
		r.reader = &buf.BufferedReader{Reader: buf.NewReader(reader)}
	}

	return r
}

func (r *ChunkStreamReader) readSize() (uint16, error) {
	if _, err := io.ReadFull(r.reader, r.buffer); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(r.buffer), nil
}

func (r *ChunkStreamReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	size := r.leftOverSize
	if size == 0 {
		r.numChunk++
		if r.maxNumChunk > 0 && r.numChunk > r.maxNumChunk {
			return nil, io.EOF
		}
		nextSize, err := r.readSize()
		if err != nil {
			return nil, err
		}
		if nextSize == 0 {
			return nil, io.EOF
		}
		size = int32(nextSize)
	}
	r.leftOverSize = size

	mb, err := r.reader.ReadAtMost(size)
	if !mb.IsEmpty() {
		r.leftOverSize -= mb.Len()
		return mb, nil
	}
	return nil, err
}
