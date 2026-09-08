//go:build linux
// +build linux

package tcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/testing/servers/tcp"
	"github.com/xtls/xray-core/transport/internet"
	. "github.com/xtls/xray-core/transport/internet/tcp"
)

func TestGetOriginalDestination(t *testing.T) {
	tcpServer := tcp.Server{}
	dest, err := tcpServer.Start()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	defer tcpServer.Close()

	config, err := internet.ToMemoryStreamConfig(nil)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	conn, err := Dial(context.Background(), dest, config)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	originalDest, err := GetOriginalDestination(conn)
	if !(dest == originalDest || strings.Contains(err.Error(), "failed to call getsockopt")) {
		t.Error("unexpected state")
	}
}
