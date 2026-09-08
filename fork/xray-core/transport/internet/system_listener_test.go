package internet_test

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/sagernet/sing/common/control"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/transport/internet"
)

func TestRegisterListenerController(t *testing.T) {
	var gotFd uintptr

	if err := common.Must(internet.RegisterListenerController(func(network, address string, conn syscall.RawConn) error {
		return control.Raw(conn, func(fd uintptr) error {
			gotFd = fd
			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	conn, err := internet.ListenSystemPacket(context.Background(), &net.UDPAddr{
		IP: net.IPv4zero,
	}, nil)
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	if err := common.Must(conn.Close()); err != nil {
		t.Fatal(err)
	}

	if gotFd == 0 {
		t.Error("expected none-zero fd, but actually 0")
	}
}
