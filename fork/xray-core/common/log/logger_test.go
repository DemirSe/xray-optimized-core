package log_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	. "github.com/xtls/xray-core/common/log"
)

func TestFileLogger(t *testing.T) {
	f, err := os.CreateTemp("", "vtest")
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if err := common.Must(f.Close()); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	creator, err := CreateFileLogWriter(path)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	handler := NewLogger(creator)
	handler.Handle(&GeneralMessage{Content: "Test Log"})
	time.Sleep(2 * time.Second)

	if err := common.Must(common.Close(handler)); err != nil {
		t.Fatal(err)
	}

	f, err = os.Open(path)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	b, err := buf.ReadAllToBytes(f)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Test Log") {
		t.Fatal("Expect log text contains 'Test Log', but actually: ", string(b))
	}
}
