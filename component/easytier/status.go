package easytier

import "context"

type StatusProvider interface {
	EasyTierSummary() Summary
	EasyTierStatus(context.Context) (Status, error)
}

type Summary struct {
	Name               string `json:"name"`
	State              string `json:"state"`
	InstanceID         string `json:"instance-id,omitempty"`
	PacketMode         bool   `json:"packet-mode"`
	DNSZone            string `json:"dns-zone"`
	WebManaged         bool   `json:"web-managed,omitempty"`
	WebClientConnected bool   `json:"webclient-connected,omitempty"`
}

type Provenance struct {
	Commit string `json:"commit"`
	SHA256 string `json:"sha256"`
}

type Status struct {
	Summary
	Node       *NodeStatus     `json:"node"`
	Peers      []PeerStatus    `json:"peers"`
	Routes     []RouteStatus   `json:"routes"`
	Provenance Provenance      `json:"provenance"`
	Networks   []NetworkStatus `json:"networks,omitempty"`
}

type NetworkStatus struct {
	InstanceID  string        `json:"instance-id"`
	NetworkName string        `json:"network-name"`
	State       string        `json:"state"`
	DNSZone     string        `json:"dns-zone"`
	Node        *NodeStatus   `json:"node,omitempty"`
	Peers       []PeerStatus  `json:"peers"`
	Routes      []RouteStatus `json:"routes"`
}

type NodeStatus struct {
	PeerID     uint32   `json:"peer-id"`
	Hostname   string   `json:"hostname"`
	IPv4       string   `json:"ipv4"`
	IPv6       string   `json:"ipv6,omitempty"`
	PublicIPv6 string   `json:"public-ipv6,omitempty"`
	InstanceID string   `json:"instance-id"`
	Version    string   `json:"version"`
	ProxyCIDRs []string `json:"proxy-cidrs"`
}

type PeerStatus struct {
	PeerID      uint32             `json:"peer-id"`
	Connections []ConnectionStatus `json:"connections"`
}

type ConnectionStatus struct {
	ID               string  `json:"id"`
	Transport        string  `json:"transport"`
	Client           bool    `json:"client"`
	Closed           bool    `json:"closed"`
	LatencyUS        uint64  `json:"latency-us"`
	LossRate         float32 `json:"loss-rate"`
	RXBytes          uint64  `json:"rx-bytes"`
	TXBytes          uint64  `json:"tx-bytes"`
	RXPackets        uint64  `json:"rx-packets"`
	TXPackets        uint64  `json:"tx-packets"`
	SecureAuthLevel  string  `json:"secure-auth-level"`
	PeerIdentityType string  `json:"peer-identity-type"`
}

type RouteStatus struct {
	PeerID                    uint32   `json:"peer-id"`
	Hostname                  string   `json:"hostname"`
	IPv4                      string   `json:"ipv4,omitempty"`
	IPv6                      string   `json:"ipv6,omitempty"`
	PublicIPv6                string   `json:"public-ipv6,omitempty"`
	NextHopPeerID             uint32   `json:"next-hop-peer-id"`
	Cost                      int32    `json:"cost"`
	PathLatency               int32    `json:"path-latency"`
	NextHopPeerIDLatencyFirst *uint32  `json:"next-hop-peer-id-latency-first,omitempty"`
	CostLatencyFirst          *int32   `json:"cost-latency-first,omitempty"`
	PathLatencyLatencyFirst   *int32   `json:"path-latency-latency-first,omitempty"`
	ProxyCIDRs                []string `json:"proxy-cidrs"`
	InstanceID                string   `json:"instance-id"`
	Version                   string   `json:"version"`
}
