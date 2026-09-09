// Package session provides functions for sessions of incoming requests.
package session // import "github.com/xtls/xray-core/common/session"

import (
	"math/rand"

	c "github.com/xtls/xray-core/common/ctx"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/signal"
)

// NewID generates a new ID. The generated ID is high likely to be unique, but not cryptographically secure.
// The generated ID will never be 0.
func NewID() c.ID {
	for {
		id := c.ID(rand.Uint32())
		if id != 0 {
			return id
		}
	}
}

// Inbound is the metadata of an inbound connection.
type Inbound struct {
	// Source address of the inbound connection.
	Source net.Destination
	// Local address of the inbound connection.
	Local net.Destination
	// Gateway address.
	Gateway net.Destination
	// Tag of the inbound proxy that handles the connection.
	Tag string
	// Name of the inbound proxy that handles the connection.
	Name string
	// User is the user that authenticates for the inbound. May be nil if the protocol allows anonymous traffic.
	User *protocol.MemoryUser
	// VlessRoute is the user-sent VLESS UUID's 7th<<8 | 8th bytes.
	VlessRoute net.Port
	// Used by splice copy. Conn is actually internet.Connection. May be nil.
	Conn net.Conn
	// Used by splice copy. Timer of the inbound buf copier. May be nil.
	Timer *signal.ActivityTimer
	// CanSpliceCopy is a property for this connection
	// 1 = can, 2 = after processing protocol info should be able to, 3 = cannot
	CanSpliceCopy int
}

// Outbound is the metadata of an outbound connection.
type Outbound struct {
	// Target address of the outbound connection.
	OriginalTarget net.Destination
	Target         net.Destination
	RouteTarget    net.Destination
	// Gateway address
	Gateway net.Address
	// Tag of the outbound proxy that handles the connection.
	Tag string
	// Name of the outbound proxy that handles the connection.
	Name string
	// Unused. Conn is actually internet.Connection. May be nil. It is currently nil for outbound with proxySettings
	Conn net.Conn
	// CanSpliceCopy is a property for this connection
	// 1 = can, 2 = after processing protocol info should be able to, 3 = cannot
	CanSpliceCopy int
}

// SniffingRequest controls the behavior of content sniffing. They are from inbound config. Read-only
type SniffingRequest struct {
	ExcludeForDomain               []string
	OverrideDestinationForProtocol []string
	Enabled                        bool
	MetadataOnly                   bool
	RouteOnly                      bool
}

// Content is the metadata of the connection content. Mainly used for routing.
type Content struct {
	// Protocol of current content.
	Protocol string

	SniffingRequest SniffingRequest

	// ForcedOutboundTag carries the platform-initialized detour tag.
	// ponytail: was map[string]string + 2 methods; HTTP-sniffed headers
	// were write-only (no repo-wide reader) and routing GetAttributes has
	// zero callers, so the map is dropped. Only forcedOutboundTag is read.
	ForcedOutboundTag string

	// SkipDNSResolve is set from DNS module. the DOH remote server maybe a domain name, this prevents cycle resolving dead loop
	SkipDNSResolve bool
}

// Sockopt is the settings for socket connection.
type Sockopt struct {
	// Mark of the socket connection.
	Mark int32
}
