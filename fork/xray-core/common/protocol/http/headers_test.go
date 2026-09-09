package http_test

import (
	"testing"

	"github.com/xtls/xray-core/common/net"
	. "github.com/xtls/xray-core/common/protocol/http"
)

func TestParseHost(t *testing.T) {
	testCases := []struct {
		RawHost     string
		DefaultPort net.Port
		Destination net.Destination
		Error       bool
	}{
		{
			RawHost:     "example.com:80",
			DefaultPort: 443,
			Destination: net.TCPDestination(net.DomainAddress("example.com"), 80),
		},
		{
			RawHost:     "tls.example.com",
			DefaultPort: 443,
			Destination: net.TCPDestination(net.DomainAddress("tls.example.com"), 443),
		},
		{
			RawHost:     "[2401:1bc0:51f0:ec08::1]:80",
			DefaultPort: 443,
			Destination: net.TCPDestination(net.ParseAddress("[2401:1bc0:51f0:ec08::1]"), 80),
		},
	}

	for _, testCase := range testCases {
		dest, err := ParseHost(testCase.RawHost, testCase.DefaultPort)
		if testCase.Error {
			if err == nil {
				t.Error("for test case: ", testCase.RawHost, " expected error, but actually nil")
			}
		} else {
			if dest != testCase.Destination {
				t.Error("for test case: ", testCase.RawHost, " expected host: ", testCase.Destination.String(), " but got ", dest.String())
			}
		}
	}
}
