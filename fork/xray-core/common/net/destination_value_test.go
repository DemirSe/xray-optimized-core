package net_test

// C3: Destination.RawNetAddrPort is the value conversion of RawNetAddr for
// native UDP writes. These checks cover the supported built-in addresses, the
// unsupported cases that keep the generic path, and the allocation behavior.

import (
	"net"
	"net/netip"
	"testing"

	. "github.com/xtls/xray-core/common/net"
)

// customValueAddress is an Address implementation outside this package. The
// native conversion must reject it and keep the generic path.
type customValueAddress struct{}

func (customValueAddress) IP() net.IP            { return net.IP{10, 0, 0, 1} }
func (customValueAddress) Domain() string        { return "10.0.0.1" }
func (customValueAddress) Family() AddressFamily { return AddressFamilyIPv4 }
func (customValueAddress) String() string        { return "10.0.0.1" }

var rawNetAddrPortSink netip.AddrPort

func TestDestinationRawNetAddrPort(t *testing.T) {
	v4Bytes := [4]byte{127, 0, 0, 1}
	v6Bytes := [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	testCases := []struct {
		name string
		dest Destination
		want netip.AddrPort
		ok   bool
	}{
		{
			name: "udp-v4",
			dest: UDPDestination(IPAddress(v4Bytes[:]), 53),
			want: netip.AddrPortFrom(netip.AddrFrom4(v4Bytes), 53),
			ok:   true,
		},
		{
			name: "udp-v4-port-zero",
			dest: UDPDestination(IPAddress(v4Bytes[:]), 0),
			want: netip.AddrPortFrom(netip.AddrFrom4(v4Bytes), 0),
			ok:   true,
		},
		{
			name: "udp-v4-port-max",
			dest: UDPDestination(IPAddress(v4Bytes[:]), 65535),
			want: netip.AddrPortFrom(netip.AddrFrom4(v4Bytes), 65535),
			ok:   true,
		},
		{
			name: "udp-v6",
			dest: UDPDestination(IPAddress(v6Bytes[:]), 443),
			want: netip.AddrPortFrom(netip.AddrFrom16(v6Bytes), 443),
			ok:   true,
		},
		{
			name: "udp-v4-mapped-v6",
			dest: UDPDestination(IPAddress(net.ParseIP("::ffff:10.1.2.3")), 9),
			want: netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 1, 2, 3}), 9),
			ok:   true,
		},
		{
			name: "tcp-ip",
			dest: TCPDestination(IPAddress(v4Bytes[:]), 80),
			ok:   false,
		},
		{
			name: "udp-domain",
			dest: UDPDestination(DomainAddress("fixture.example"), 53),
			ok:   false,
		},
		{
			name: "udp-nil-address",
			dest: Destination{Network: Network_UDP, Port: 53},
			ok:   false,
		},
		{
			name: "unknown-network",
			dest: Destination{Address: IPAddress(v4Bytes[:]), Port: 53},
			ok:   false,
		},
		{
			name: "custom-address-implementation",
			dest: UDPDestination(customValueAddress{}, 53),
			ok:   false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := testCase.dest.RawNetAddrPort()
			if ok != testCase.ok {
				t.Fatalf("RawNetAddrPort ok = %v, want %v", ok, testCase.ok)
			}
			if !ok {
				return
			}
			if got != testCase.want {
				t.Fatalf("RawNetAddrPort = %v, want %v", got, testCase.want)
			}
			raw := testCase.dest.RawNetAddr()
			udpAddr, isUDP := raw.(*net.UDPAddr)
			if !isUDP {
				t.Fatalf("RawNetAddr returned %T, want *net.UDPAddr for a supported destination", raw)
			}
			if udpAddr.AddrPort() != got {
				t.Fatalf("RawNetAddrPort = %v, RawNetAddr = %v", got, udpAddr.AddrPort())
			}
		})
	}
}

func TestDestinationRawNetAddrPortDoesNotAllocate(t *testing.T) {
	dest := UDPDestination(IPAddress([]byte{127, 0, 0, 1}), 53)
	allocs := testing.AllocsPerRun(1000, func() {
		rawNetAddrPortSink, _ = dest.RawNetAddrPort()
	})
	if allocs != 0 {
		t.Fatalf("RawNetAddrPort allocates %.1f objects per call, want 0", allocs)
	}
	rawNetAddrAllocs := testing.AllocsPerRun(1000, func() {
		_ = dest.RawNetAddr()
	})
	if rawNetAddrAllocs < 1 {
		t.Fatalf("RawNetAddr allocates %.1f objects per call, expected the generic path to allocate", rawNetAddrAllocs)
	}
}
