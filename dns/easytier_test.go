package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/component/fakeip"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
)

type dynamicMagicDNSClient struct {
	networks []EasyTierNetwork
	clients  map[string]*magicDNSClient
}

func (c *dynamicMagicDNSClient) Address() string  { return "dynamic-magic-dns-test" }
func (c *dynamicMagicDNSClient) ResetConnection() {}
func (c *dynamicMagicDNSClient) ExchangeContext(context.Context, *D.Msg) (*D.Msg, error) {
	return nil, fmt.Errorf("dynamic client requires an instance ID")
}
func (c *dynamicMagicDNSClient) EasyTierDNSNetworks(context.Context) ([]EasyTierNetwork, error) {
	return c.networks, nil
}
func (c *dynamicMagicDNSClient) ExchangeEasyTierDNS(ctx context.Context, id string, request *D.Msg) (*D.Msg, error) {
	return c.clients[id].ExchangeContext(ctx, request)
}

func TestAutomaticEasyTierDynamicDNS(t *testing.T) {
	client := &dynamicMagicDNSClient{clients: make(map[string]*magicDNSClient)}
	t.Cleanup(RegisterEasyTierDnsClient("web-dns-test", client))
	upstream := &magicDNSClient{addresses: map[string]string{"desk.": "192.0.2.1"}}
	service := NewService(NewResolverFromClient(upstream), NewEnhancer(EnhancerConfig{
		EasyTier: []EasyTierNetwork{{Name: "web-dns-test", Dynamic: true}},
	}))
	query := func(name string) (*D.Msg, error) {
		request := new(D.Msg)
		request.SetQuestion(name, D.TypeA)
		return service.ServeMsg(context.Background(), request)
	}
	assertAddress := func(name, address string) {
		t.Helper()
		response, err := query(name)
		if err != nil || len(response.Answer) != 1 || response.Answer[0].(*D.A).A.String() != address {
			t.Fatalf("%s = %s, %v; want %s", name, response, err, address)
		}
	}
	assertAddress("desk.", "192.0.2.1")
	client.networks = []EasyTierNetwork{{Name: "first", Zone: "mesh.net"}}
	client.clients["first"] = &magicDNSClient{addresses: map[string]string{"desk.mesh.net.": "10.144.0.1", "desk.": "10.144.0.1"}}
	assertAddress("desk.mesh.net.", "10.144.0.1")
	assertAddress("desk.", "10.144.0.1")
	client.networks[0].Zone = "new.mesh.net"
	client.clients["first"] = &magicDNSClient{addresses: map[string]string{"desk.new.mesh.net.": "10.144.0.2", "desk.": "10.144.0.2"}}
	assertAddress("desk.new.mesh.net.", "10.144.0.2")
	response, err := query("desk.mesh.net.")
	if err != nil || response.Rcode != D.RcodeNameError {
		t.Fatalf("replaced zone still resolved: %s, %v", response, err)
	}
	client.networks = append(client.networks, EasyTierNetwork{Name: "second", Zone: "other.net"})
	client.clients["second"] = &magicDNSClient{addresses: map[string]string{"desk.other.net.": "10.145.0.1", "desk.": "10.145.0.1"}}
	assertAddress("desk.other.net.", "10.145.0.1")
	if _, err := query("desk."); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous dynamic short name was accepted: %v", err)
	}
	client.networks[1].Zone = "new.mesh.net"
	client.clients["second"] = &magicDNSClient{
		addresses:  map[string]string{"desk.new.mesh.net.": "10.145.0.1", "desk.": "10.145.0.1"},
		addresses6: map[string]string{"desk.new.mesh.net.": "fd00:145::1"},
	}
	for _, name := range []string{"desk.new.mesh.net.", "desk."} {
		response, err := query(name)
		if err != nil || response.Rcode != D.RcodeSuccess {
			t.Fatalf("shared-zone %s = %s, %v", name, response, err)
		}
		var addresses []string
		for _, record := range response.Answer {
			addresses = append(addresses, record.(*D.A).A.String())
		}
		slices.Sort(addresses)
		if !slices.Equal(addresses, []string{"10.144.0.2", "10.145.0.1"}) {
			t.Fatalf("shared-zone %s RRset = %v", name, addresses)
		}
	}
	request := new(D.Msg)
	request.SetQuestion("desk.new.mesh.net.", D.TypeAAAA)
	response, err = service.ServeMsg(context.Background(), request)
	if err != nil || response.Rcode != D.RcodeSuccess || len(response.Answer) != 1 || response.Answer[0].(*D.AAAA).AAAA.String() != "fd00:145::1" {
		t.Fatalf("shared-zone positive plus NODATA = %s, %v", response, err)
	}
	for _, qtype := range []uint16{D.TypeA, D.TypeTXT} {
		request.SetQuestion("missing.new.mesh.net.", qtype)
		response, err = service.ServeMsg(context.Background(), request)
		if err != nil || response.Rcode != D.RcodeNameError {
			t.Fatalf("missing shared-zone name (%d) = %s, %v", qtype, response, err)
		}
	}
	client.clients["second"].addresses["desk.new.mesh.net."] = "10.144.0.2"
	assertAddress("desk.new.mesh.net.", "10.144.0.2")
	client.networks = nil
	assertAddress("desk.", "192.0.2.1")
}

type magicDNSClient struct {
	addresses  map[string]string
	addresses6 map[string]string
	reverse    map[string]string
	calls      int
}

func (c *magicDNSClient) Address() string  { return "magic-dns-test" }
func (c *magicDNSClient) ResetConnection() {}
func (c *magicDNSClient) ExchangeContext(ctx context.Context, request *D.Msg) (*D.Msg, error) {
	c.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply := new(D.Msg)
	reply.SetReply(request)
	question := request.Question[0]
	header := D.RR_Header{Name: question.Name, Rrtype: question.Qtype, Class: D.ClassINET, Ttl: 60}
	switch question.Qtype {
	case D.TypeA:
		if ip := c.addresses[strings.ToLower(question.Name)]; ip != "" {
			reply.Answer = []D.RR{&D.A{Hdr: header, A: net.ParseIP(ip).To4()}}
		} else if c.addresses6[strings.ToLower(question.Name)] == "" {
			reply.Rcode = D.RcodeNameError
		}
	case D.TypePTR:
		if name := c.reverse[question.Name]; name != "" {
			reply.Answer = []D.RR{&D.PTR{Hdr: header, Ptr: name}}
		} else {
			reply.Rcode = D.RcodeNameError
		}
	case D.TypeAAAA:
		if ip := c.addresses6[strings.ToLower(question.Name)]; ip != "" {
			reply.Answer = []D.RR{&D.AAAA{Hdr: header, AAAA: net.ParseIP(ip)}}
		} else if c.addresses[strings.ToLower(question.Name)] == "" {
			reply.Rcode = D.RcodeNameError
		}
	default:
		if c.addresses[strings.ToLower(question.Name)] == "" && c.addresses6[strings.ToLower(question.Name)] == "" {
			reply.Rcode = D.RcodeNameError
		}
	}
	return reply, nil
}

func TestAutomaticEasyTierIPv6DNS(t *testing.T) {
	reverse, err := D.ReverseAddr("fd00:144::2")
	if err != nil {
		t.Fatal(err)
	}
	client := &magicDNSClient{
		addresses6: map[string]string{"desk.mesh.": "fd00:144::2", "desk.": "fd00:144::2"},
		reverse:    map[string]string{reverse: "desk.mesh."},
	}
	t.Cleanup(RegisterEasyTierDnsClient("ipv6-mesh", client))
	service := NewService(NewResolverFromClient(&magicDNSClient{}), NewEnhancer(EnhancerConfig{
		EasyTier: []EasyTierNetwork{{Name: "ipv6-mesh", Zone: "mesh"}},
	}))
	for _, name := range []string{"desk.mesh.", "desk."} {
		request := new(D.Msg)
		request.SetQuestion(name, D.TypeAAAA)
		response, err := service.ServeMsg(context.Background(), request)
		if err != nil || len(response.Answer) != 1 || response.Answer[0].(*D.AAAA).AAAA.String() != "fd00:144::2" {
			t.Fatalf("%s IPv6 Magic DNS: %s, %v", name, response, err)
		}
	}
	request := new(D.Msg)
	request.SetQuestion(reverse, D.TypePTR)
	response, err := service.ServeMsg(context.Background(), request)
	if err != nil || len(response.Answer) != 1 || response.Answer[0].(*D.PTR).Ptr != "desk.mesh." {
		t.Fatalf("IPv6 reverse Magic DNS: %s, %v", response, err)
	}
}

func TestAutomaticEasyTierMagicDNS(t *testing.T) {
	mesh := &magicDNSClient{
		addresses: map[string]string{"desk.mesh.": "10.144.0.2", "desk.": "10.144.0.2"},
		reverse:   map[string]string{"2.0.144.10.in-addr.arpa.": "desk.mesh."},
	}
	dev := &magicDNSClient{addresses: map[string]string{"build.dev.mesh.": "10.145.0.2", "build.": "10.145.0.2"}}
	idle := &magicDNSClient{}
	t.Cleanup(RegisterEasyTierDnsClient("mesh-dns-test", mesh))
	t.Cleanup(RegisterEasyTierDnsClient("dev-dns-test", dev))
	t.Cleanup(RegisterEasyTierDnsClient("idle-dns-test", idle))
	networks := []EasyTierNetwork{{Name: "mesh-dns-test", Zone: "mesh"}, {Name: "dev-dns-test", Zone: "dev.mesh"}}
	for _, mode := range []C.DNSMode{C.DNSMapping, C.DNSFakeIP} {
		t.Run(mode.String(), func(t *testing.T) {
			pool, err := fakeip.New(fakeip.Options{IPNet: netip.MustParsePrefix("198.18.0.1/16"), Size: 16})
			if err != nil {
				t.Fatal(err)
			}
			upstream := &magicDNSClient{
				addresses: map[string]string{"example.com.": "192.0.2.1", "printer.": "192.0.2.2"},
				reverse:   map[string]string{"1.2.0.192.in-addr.arpa.": "example.com."},
			}
			service := NewService(NewResolverFromClient(upstream), NewEnhancer(EnhancerConfig{
				EnhancedMode: mode, FakeIPPool: pool, FakeIPSkipper: &fakeip.Skipper{}, EasyTier: networks,
			}))
			query := func(name string, qtype uint16) *D.Msg {
				t.Helper()
				request := new(D.Msg)
				request.SetQuestion(name, qtype)
				response, err := service.ServeMsg(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			for _, name := range []string{"Desk.mesh.", "desk."} {
				response := query(name, D.TypeA)
				if len(response.Answer) != 1 || response.Answer[0].(*D.A).A.String() != "10.144.0.2" {
					t.Fatalf("%s Magic DNS answer = %s", name, response)
				}
			}
			if response := query("build.dev.mesh.", D.TypeA); response.Answer[0].(*D.A).A.String() != "10.145.0.2" {
				t.Fatalf("nested zone chose the wrong network: %s", response)
			}
			if response := query("missing.mesh.", D.TypeA); response.Rcode != D.RcodeNameError {
				t.Fatalf("missing Magic DNS hostname escaped its zone: %s", response)
			}
			if response := query("mesh.", D.TypeHTTPS); response.Rcode != D.RcodeNameError {
				t.Fatalf("missing zone apex HTTPS answer = %s", response)
			}
			if response := query("desk.", D.TypeAAAA); response.Rcode != D.RcodeSuccess || len(response.Answer) != 0 {
				t.Fatalf("short overlay AAAA answer = %s", response)
			}
			if response := query("2.0.144.10.in-addr.arpa.", D.TypePTR); len(response.Answer) != 1 || response.Answer[0].(*D.PTR).Ptr != "desk.mesh." {
				t.Fatalf("overlay reverse answer = %s", response)
			}
			before := mesh.calls + dev.calls
			response := query("example.com.", D.TypeA)
			ip, _ := netip.AddrFromSlice(response.Answer[0].(*D.A).A)
			if mode == C.DNSFakeIP && !pool.IPNet().Contains(ip.Unmap()) {
				t.Fatalf("ordinary hostname lost fake-ip behavior: %s", response)
			}
			if mode == C.DNSMapping && ip.Unmap().String() != "192.0.2.1" {
				t.Fatalf("ordinary hostname did not reach the upstream: %s", response)
			}
			if mesh.calls+dev.calls != before {
				t.Fatal("ordinary qualified hostname queried an EasyTier client")
			}
			if response := query("printer.", D.TypeA); response.Rcode != D.RcodeSuccess || len(response.Answer) == 0 {
				t.Fatalf("unknown short name did not fall through: %s", response)
			}
			if response := query("1.2.0.192.in-addr.arpa.", D.TypePTR); len(response.Answer) != 1 || response.Answer[0].(*D.PTR).Ptr != "example.com." {
				t.Fatalf("ordinary reverse did not fall through: %s", response)
			}
		})
	}
	if idle.calls != 0 {
		t.Fatal("automatic Magic DNS queried an unattached instance")
	}
	dev.addresses["desk."] = "10.145.0.3"
	request := new(D.Msg)
	request.SetQuestion("desk.", D.TypeA)
	service := NewService(NewResolverFromClient(&magicDNSClient{}), NewEnhancer(EnhancerConfig{EasyTier: networks}))
	if _, err := service.ServeMsg(context.Background(), request); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("overlapping short hostname was not rejected: %v", err)
	}
}
