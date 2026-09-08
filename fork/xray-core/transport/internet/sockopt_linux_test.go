package internet_test

import (
	"context"
	"syscall"
	"testing"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/testing/servers/tcp"
	. "github.com/xtls/xray-core/transport/internet"
)

func TestSockOptMark(t *testing.T) {
	t.Skip("requires CAP_NET_ADMIN")

	tcpServer := tcp.Server{
		MsgProcessor: func(b []byte) []byte {
			return b
		},
	}
	dest, err := tcpServer.Start()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	defer tcpServer.Close()

	const mark = 1
	dialer := DefaultSystemDialer{}
	conn, err := dialer.Dial(context.Background(), nil, dest, &SocketConfig{Mark: mark})
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rawConn, err := conn.(*net.TCPConn).SyscallConn()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	err = rawConn.Control(func(fd uintptr) {
		m, err := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK)
		if err := common.Must(err); err != nil {
			t.Fatal(err)
		}
		if mark != m {
			t.Fatal("unexpected connection mark", m, " want ", mark)
		}
	})
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
}
