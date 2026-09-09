package protocol

import (
	"runtime"

	"github.com/xtls/xray-core/common/net"
	"golang.org/x/sys/cpu"
)

// RequestCommand is a custom command in a proxy request.
type RequestCommand byte

const (
	RequestCommandTCP = RequestCommand(0x01)
	RequestCommandUDP = RequestCommand(0x02)
	RequestCommandMux = RequestCommand(0x03)
	RequestCommandRvs = RequestCommand(0x04)
)

func (c RequestCommand) TransferType() TransferType {
	switch c {
	case RequestCommandTCP, RequestCommandMux, RequestCommandRvs:
		return TransferTypeStream
	case RequestCommandUDP:
		return TransferTypePacket
	default:
		return TransferTypeStream
	}
}

type RequestHeader struct {
	Version  byte
	Command  RequestCommand
	Option   byte
	Security SecurityType
	Port     net.Port
	Address  net.Address
	User     *MemoryUser
}

func (h *RequestHeader) Destination() net.Destination {
	if h.Command == RequestCommandUDP {
		return net.UDPDestination(h.Address, h.Port)
	}
	return net.TCPDestination(h.Address, h.Port)
}

const (
	ResponseOptionConnectionReuse byte = 0x01
)

var (
	// Keep in sync with crypto/tls/cipher_suites.go.
	hasGCMAsmAMD64 = cpu.X86.HasAES && cpu.X86.HasPCLMULQDQ && cpu.X86.HasSSE41 && cpu.X86.HasSSSE3
	hasGCMAsmARM64 = (cpu.ARM64.HasAES && cpu.ARM64.HasPMULL) || (runtime.GOOS == "darwin" && runtime.GOARCH == "arm64")

	HasAESGCMHardwareSupport = hasGCMAsmAMD64 || hasGCMAsmARM64
)

func (sc *SecurityConfig) GetSecurityType() SecurityType {
	if sc == nil || sc.Type == SecurityType_AUTO {
		if HasAESGCMHardwareSupport {
			return SecurityType_AES128_GCM
		}
		return SecurityType_CHACHA20_POLY1305
	}
	return sc.Type
}

func isDomainTooLong(domain string) bool {
	return len(domain) > 256
}
