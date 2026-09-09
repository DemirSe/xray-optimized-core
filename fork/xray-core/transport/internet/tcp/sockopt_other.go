//go:build !linux && !freebsd && !darwin
// +build !linux,!freebsd,!darwin

package tcp

import (
	"github.com/xtls/xray-core/common/net"
)

func GetOriginalDestination(conn net.Conn) (net.Destination, error) {
	return net.Destination{}, nil
}
