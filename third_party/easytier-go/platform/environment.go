package platform

import (
	"context"
	"net"
	"net/netip"
)

type ConnectorEnvironment interface {
	LocalAddrForRemote(context.Context, *net.UDPAddr, SocketContext) (net.Addr, error)
}

// EnvironmentSnapshot is the host-observed network state supplied when an
// EasyTier instance or WebClient is created. Core remains responsible for all
// policy decisions made from these facts. Host.UpdateEnvironment can publish a
// complete replacement to long-lived instances after an underlay change.
type EnvironmentSnapshot struct {
	PublicIPv4           *netip.Addr
	InterfaceIPv4s       []netip.Addr
	PublicIPv6           *netip.Addr
	InterfaceIPv6s       []netip.Addr
	MappedListeners      []string
	LocalIPs             []netip.Addr
	ProtectedTCPPorts    []uint16
	PreferredIPv6Sources []PreferredIPv6Source
}

type PreferredIPv6Source struct {
	IP      netip.Addr
	IfIndex uint32
}

type Services struct {
	Sockets     SocketFactory
	DNS         DNSResolver
	Environment ConnectorEnvironment
	Snapshot    EnvironmentSnapshot
}
