package dns

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/component/resolver"
	icontext "github.com/metacubex/mihomo/context"

	D "github.com/miekg/dns"
)

type easyTierDNSClient struct {
	name      string
	networkID string
}

type EasyTierNetwork struct {
	Name    string
	Zone    string
	Dynamic bool
}

type easyTierDNSNetworksProvider interface {
	EasyTierDNSNetworks(context.Context) ([]EasyTierNetwork, error)
	ExchangeEasyTierDNS(context.Context, string, *D.Msg) (*D.Msg, error)
}

type easyTierDNSNetwork struct {
	EasyTierNetwork
	client *easyTierDNSClient
}

type easyTierResolverEntry struct {
	id     uint64
	client dnsClient
}

var (
	easyTierResolverID atomic.Uint64
	easyTierResolverMu sync.RWMutex
	easyTierResolvers  = map[string]easyTierResolverEntry{}
)

var _ dnsClient = (*easyTierDNSClient)(nil)

func RegisterEasyTierDnsClient(name string, client dnsClient) func() {
	id := easyTierResolverID.Add(1)
	easyTierResolverMu.Lock()
	easyTierResolvers[name] = easyTierResolverEntry{
		id:     id,
		client: client,
	}
	easyTierResolverMu.Unlock()

	return func() {
		easyTierResolverMu.Lock()
		if entry, ok := easyTierResolvers[name]; ok && entry.id == id {
			delete(easyTierResolvers, name)
		}
		easyTierResolverMu.Unlock()
	}
}

func newEasyTierClient(name string) *easyTierDNSClient {
	return &easyTierDNSClient{name: name}
}

func (c *easyTierDNSClient) Address() string {
	return "easytier://" + c.name
}

func (c *easyTierDNSClient) ExchangeContext(ctx context.Context, m *D.Msg) (*D.Msg, error) {
	easyTierResolverMu.RLock()
	entry, ok := easyTierResolvers[c.name]
	easyTierResolverMu.RUnlock()
	if !ok || entry.client == nil {
		return nil, fmt.Errorf("proxy %q does not provide EasyTier DNS", c.name)
	}
	if c.networkID != "" {
		provider, ok := entry.client.(easyTierDNSNetworksProvider)
		if !ok {
			return nil, fmt.Errorf("proxy %q no longer provides configuration-center DNS", c.name)
		}
		return provider.ExchangeEasyTierDNS(ctx, c.networkID, m)
	}
	return entry.client.ExchangeContext(ctx, m)
}

func (c *easyTierDNSClient) ResetConnection() {}

func easyTierDNSNetworks(ctx context.Context, configured []EasyTierNetwork) ([]easyTierDNSNetwork, error) {
	var networks []easyTierDNSNetwork
	for _, network := range configured {
		if !network.Dynamic {
			networks = append(networks, easyTierDNSNetwork{network, newEasyTierClient(network.Name)})
			continue
		}
		easyTierResolverMu.RLock()
		entry, exists := easyTierResolvers[network.Name]
		easyTierResolverMu.RUnlock()
		if !exists {
			return nil, fmt.Errorf("proxy %q does not provide EasyTier DNS", network.Name)
		}
		provider, ok := entry.client.(easyTierDNSNetworksProvider)
		if !ok {
			return nil, fmt.Errorf("proxy %q does not provide configuration-center DNS", network.Name)
		}
		current, err := provider.EasyTierDNSNetworks(ctx)
		if err != nil {
			return nil, fmt.Errorf("EasyTier Magic DNS %q: %w", network.Name, err)
		}
		for _, instance := range current {
			client := &easyTierDNSClient{name: network.Name, networkID: instance.Name}
			instance.Name = network.Name + "/" + instance.Name
			networks = append(networks, easyTierDNSNetwork{instance, client})
		}
	}
	return networks, nil
}

func withEasyTier(networks []EasyTierNetwork) middleware {
	return func(next handler) handler {
		return func(ctx *icontext.DNSContext, request *D.Msg) (*D.Msg, error) {
			question := request.Question[0]
			if question.Qclass != D.ClassINET {
				return next(ctx, request)
			}
			queryCtx, cancel := context.WithTimeout(ctx, resolver.DefaultDNSTimeout)
			defer cancel()
			current, err := easyTierDNSNetworks(queryCtx, networks)
			if err != nil {
				return nil, err
			}
			host := easytier.NormalizeDNSName(question.Name)
			qualifiedZone := ""
			for _, network := range current {
				if easytier.IsMagicDNS(host, network.Zone) && len(network.Zone) > len(qualifiedZone) {
					qualifiedZone = network.Zone
				}
			}
			shortHost := host != "" && !strings.Contains(host, ".")
			_, reversePTR := easytier.ParsePTR(host)
			probeAddress := qualifiedZone == "" && shortHost && (question.Qtype == D.TypeHTTPS || question.Qtype == D.TypeSVCB)
			if qualifiedZone == "" && !(shortHost && (question.Qtype == D.TypeA || question.Qtype == D.TypeAAAA || probeAddress)) && !(question.Qtype == D.TypePTR && reversePTR) {
				return next(ctx, request)
			}
			query := request
			if probeAddress {
				query = request.Copy()
				query.Question[0].Qtype = D.TypeA
			}
			var answer *D.Msg
			var missing *D.Msg
			var answerNetwork, answerZone string
			for _, network := range current {
				if qualifiedZone != "" && network.Zone != qualifiedZone {
					continue
				}
				response, err := network.client.ExchangeContext(queryCtx, query)
				if err != nil {
					return nil, fmt.Errorf("EasyTier Magic DNS %q: %w", network.Name, err)
				}
				if probeAddress && response.Rcode == D.RcodeSuccess && len(response.Answer) == 0 {
					ipv6Query := query.Copy()
					ipv6Query.Question[0].Qtype = D.TypeAAAA
					response, err = network.client.ExchangeContext(queryCtx, ipv6Query)
					if err != nil {
						return nil, fmt.Errorf("EasyTier Magic DNS %q: %w", network.Name, err)
					}
				}
				if response.Rcode != D.RcodeSuccess {
					if qualifiedZone != "" && missing == nil {
						missing = response
					}
					continue
				}
				if qualifiedZone == "" && (question.Qtype == D.TypePTR || probeAddress) && len(response.Answer) == 0 {
					continue
				}
				if answer != nil && answerZone != network.Zone {
					return nil, fmt.Errorf("EasyTier Magic DNS name %q is ambiguous between %q and %q; use a qualified hostname", question.Name, answerNetwork, network.Name)
				}
				if answer == nil {
					answer, answerNetwork, answerZone = response, network.Name, network.Zone
				} else {
					answer.Answer = append(answer.Answer, response.Answer...)
				}
			}
			if answer == nil {
				if qualifiedZone == "" {
					return next(ctx, request)
				}
				answer = missing
			}
			if probeAddress {
				answer = handleMsgWithEmptyAnswer(request)
			} else {
				answer.Answer = D.Dedup(answer.Answer, nil)
			}
			ctx.SetType(icontext.DNSTypeRaw)
			return answer, nil
		}
	}
}
