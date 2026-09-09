package internet

import (
	"context"
	"net"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
)

type PacketHeader interface {
	Size() int32
	Serialize([]byte)
}

type ConnectionAuthenticator interface {
	Client(net.Conn) net.Conn
	Server(net.Conn) net.Conn
}

// ponytail: one helper (was 2 identical funcs); CreatePacketHeader had zero callers.
func createHeaderAs[T any](config interface{}, notHeader string) (T, error) {
	var zero T
	header, err := common.CreateObject(context.Background(), config)
	if err != nil {
		return zero, err
	}
	if h, ok := header.(T); ok {
		return h, nil
	}
	return zero, errors.New(notHeader)
}

func CreatePacketHeader(config interface{}) (PacketHeader, error) {
	return createHeaderAs[PacketHeader](config, "not a packet header")
}

func CreateConnectionAuthenticator(config interface{}) (ConnectionAuthenticator, error) {
	return createHeaderAs[ConnectionAuthenticator](config, "not a ConnectionAuthenticator")
}
