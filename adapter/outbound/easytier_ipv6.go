//go:build !no_easytier

package outbound

import (
	"net/netip"

	"github.com/easytier/easytier/easytier-go/proto/common"
	"github.com/metacubex/mihomo/component/easytier"
)

// easyTierIPv6Inet converts the protobuf IPv6 CIDR representation without
// accepting a malformed or IPv4-mapped address.
func easyTierIPv6Inet(value *common.Ipv6Inet) (netip.Prefix, bool) {
	if value == nil || value.GetAddress() == nil {
		return netip.Prefix{}, false
	}
	address := easytier.IPv6FromParts(
		value.GetAddress().GetPart1(),
		value.GetAddress().GetPart2(),
		value.GetAddress().GetPart3(),
		value.GetAddress().GetPart4(),
	)
	if !address.IsValid() || !address.Is6() || address.Is4In6() || address.IsUnspecified() {
		return netip.Prefix{}, false
	}
	length := int(value.GetNetworkLength())
	if length < 0 || length > address.BitLen() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(address, length), true
}

func appendEasyTierPrefixUnique(values *[]netip.Prefix, value netip.Prefix) {
	if !value.IsValid() {
		return
	}
	for _, current := range *values {
		if current == value {
			return
		}
	}
	*values = append(*values, value)
}
