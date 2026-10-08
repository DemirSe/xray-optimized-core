package net_test

// N2: IPAddressFromAddr converts a netip.Addr value directly to the built-in
// Address values. These checks compare it with the existing slice conversion
// IPAddress on every address shape that a native UDP receive can produce.
// Addr.AsSlice() is the input that UDPConn.ReadFrom hands to IPAddress today.

import (
	"bytes"
	"net/netip"
	"testing"

	. "github.com/xtls/xray-core/common/net"
)

var ipAddressFromAddrSink Address

func TestIPAddressFromAddrMatchesIPAddress(t *testing.T) {
	v4Bytes := [4]byte{127, 0, 0, 1}
	v6Bytes := [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	mappedBytes := [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 127, 0, 0, 1}

	testCases := []struct {
		name string
		addr netip.Addr
	}{
		{name: "ipv4", addr: netip.AddrFrom4(v4Bytes)},
		{name: "ipv4-unspecified", addr: netip.AddrFrom4([4]byte{})},
		{name: "ipv6", addr: netip.AddrFrom16(v6Bytes)},
		{name: "ipv6-loopback", addr: netip.IPv6Loopback()},
		{name: "ipv6-unspecified", addr: netip.IPv6Unspecified()},
		{name: "mapped-ipv4", addr: netip.AddrFrom16(mappedBytes)},
		{
			name: "mapped-ipv4-unspecified",
			addr: netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}),
		},
		{name: "zoned-ipv6", addr: netip.AddrFrom16(v6Bytes).WithZone("fixture0")},
		{name: "zoned-mapped-ipv4", addr: netip.AddrFrom16(mappedBytes).WithZone("fixture0")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := IPAddressFromAddr(testCase.addr)
			// The baseline conversion receives the address as a net.IP slice.
			want := IPAddress(testCase.addr.AsSlice())
			if got != want {
				t.Fatalf("IPAddressFromAddr(%v) = %v, want %v", testCase.addr, got, want)
			}
			if got.Family() != want.Family() {
				t.Fatalf("family = %v, want %v", got.Family(), want.Family())
			}
			if !bytes.Equal(got.IP(), want.IP()) {
				t.Fatalf("IP = %v, want %v", got.IP(), want.IP())
			}
			if got.String() != want.String() {
				t.Fatalf("String = %q, want %q", got.String(), want.String())
			}
			if testCase.addr.Zone() != "" && bytes.Contains([]byte(got.String()), []byte("%")) {
				t.Fatalf("String = %q, want no zone like the baseline conversion", got.String())
			}
		})
	}
}

func TestIPAddressFromAddrInvalid(t *testing.T) {
	if got := IPAddressFromAddr(netip.Addr{}); got != nil {
		t.Fatalf("IPAddressFromAddr(invalid) = %v, want nil", got)
	}
}

func TestIPAddressFromAddrDoesNotAddAllocations(t *testing.T) {
	mapped := netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 10, 1, 2, 3})
	mappedSlice := mapped.AsSlice()
	candidateAllocs := testing.AllocsPerRun(1000, func() {
		ipAddressFromAddrSink = IPAddressFromAddr(mapped)
	})
	baselineAllocs := testing.AllocsPerRun(1000, func() {
		ipAddressFromAddrSink = IPAddress(mappedSlice)
	})
	if candidateAllocs > baselineAllocs {
		t.Fatalf("IPAddressFromAddr allocations = %v, want at most the slice conversion %v", candidateAllocs, baselineAllocs)
	}
	t.Logf("conversion allocations: value %v, slice %v", candidateAllocs, baselineAllocs)
	if ipAddressFromAddrSink == nil || ipAddressFromAddrSink.Family() != AddressFamilyIPv4 {
		t.Fatalf("mapped conversion = %v, want an IPv4 address", ipAddressFromAddrSink)
	}
}
