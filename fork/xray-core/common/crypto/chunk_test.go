package crypto_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	. "github.com/xtls/xray-core/common/crypto"
)

func TestChunkStreamIO(t *testing.T) {
	cache := bytes.NewBuffer(make([]byte, 0, 8192))

	writer := NewChunkStreamWriter(PlainChunkSizeParser{}, cache)
	reader := NewChunkStreamReader(PlainChunkSizeParser{}, cache)

	b := buf.New()
	b.WriteString("abcd")
	if err := common.Must(writer.WriteMultiBuffer(buf.MultiBuffer{b})); err != nil {
		t.Fatal(err)
	}

	b = buf.New()
	b.WriteString("efg")
	if err := common.Must(writer.WriteMultiBuffer(buf.MultiBuffer{b})); err != nil {
		t.Fatal(err)
	}

	if err := common.Must(writer.WriteMultiBuffer(buf.MultiBuffer{})); err != nil {
		t.Fatal(err)
	}

	if cache.Len() != 13 {
		t.Fatalf("Cache length is %d, want 13", cache.Len())
	}

	mb, err := reader.ReadMultiBuffer()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	if s := mb.String(); s != "abcd" {
		t.Error("content: ", s)
	}

	mb, err = reader.ReadMultiBuffer()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	if s := mb.String(); s != "efg" {
		t.Error("content: ", s)
	}

	_, err = reader.ReadMultiBuffer()
	if err != io.EOF {
		t.Error("error: ", err)
	}
}
