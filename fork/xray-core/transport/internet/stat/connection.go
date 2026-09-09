package stat

import (
	"net"

	"github.com/xtls/xray-core/features/stats"
)

type CounterConnection struct {
	net.Conn
	ReadCounter  stats.Counter
	WriteCounter stats.Counter
}

func (c *CounterConnection) Read(b []byte) (int, error) {
	nBytes, err := c.Conn.Read(b)
	if c.ReadCounter != nil {
		c.ReadCounter.Add(int64(nBytes))
	}

	return nBytes, err
}

func (c *CounterConnection) Write(b []byte) (int, error) {
	nBytes, err := c.Conn.Write(b)
	if c.WriteCounter != nil {
		c.WriteCounter.Add(int64(nBytes))
	}
	return nBytes, err
}

func TryUnwrapStatsConn(conn net.Conn) net.Conn {
	if conn == nil {
		return conn
	}
	if conn, ok := conn.(*CounterConnection); ok {
		return conn.Conn
	}
	return conn
}
