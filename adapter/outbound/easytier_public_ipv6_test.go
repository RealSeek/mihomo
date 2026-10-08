//go:build !no_easytier

package outbound

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/metacubex/mihomo/component/easytier"
	D "github.com/miekg/dns"
)

func TestEasyTierPublicIPv6DNS(t *testing.T) {
	publicAddress := netip.MustParseAddr("2001:db8:144::2")
	for _, overlayAddress := range []netip.Addr{{}, netip.MustParseAddr("fd00:144::2")} {
		nodes := []easytier.Node{{Hostname: "phone", IPv6: overlayAddress, PublicIPv6: publicAddress}}
		query := new(D.Msg)
		query.SetQuestion("phone.et.net.", D.TypeAAAA)
		reply := easyTierDNSReply(query, "et.net", nodes)
		var got []string
		for _, answer := range reply.Answer {
			got = append(got, answer.(*D.AAAA).AAAA.String())
		}
		if reply.Rcode != D.RcodeSuccess || !slices.Contains(got, publicAddress.String()) {
			t.Fatalf("public IPv6 AAAA reply = %s", reply)
		}
		if overlayAddress.IsValid() && !slices.Contains(got, overlayAddress.String()) {
			t.Fatalf("static overlay IPv6 absent from reply = %s", reply)
		}
		reverse, err := D.ReverseAddr(publicAddress.String())
		if err != nil {
			t.Fatal(err)
		}
		query.SetQuestion(reverse, D.TypePTR)
		reply = easyTierDNSReply(query, "et.net", nodes)
		if reply.Rcode != D.RcodeSuccess || len(reply.Answer) != 1 || reply.Answer[0].(*D.PTR).Ptr != "phone.et.net." {
			t.Fatalf("public IPv6 PTR reply = %s", reply)
		}
	}
}
