package outboundgroup

import (
	"net/netip"
	"testing"

	"github.com/metacubex/mihomo/common/lru"
	"github.com/metacubex/mihomo/component/smart"
	C "github.com/metacubex/mihomo/constant"

	"github.com/stretchr/testify/require"
)

type priorityStubProxy struct {
	C.Proxy
	name  string
	kind  C.AdapterType
	alive bool
	udp   bool
}

func (p priorityStubProxy) Name() string                      { return p.name }
func (p priorityStubProxy) Type() C.AdapterType               { return p.kind }
func (p priorityStubProxy) AliveForTestUrl(string) bool       { return p.alive }
func (p priorityStubProxy) SupportUDP() bool                  { return p.udp }
func (p priorityStubProxy) LastDelayForTestUrl(string) uint16 { return 1 }

func TestTypePriorityParsing(t *testing.T) {
	s := &Smart{typePriority: make(map[string]float64)}
	s.applyTypePriority("ss:1.5; VLESS:1.2;vmess:0.8;hy2:oops;trojan:0;broken")

	require.Equal(t, map[string]float64{
		"shadowsocks": 1.5,
		"vless":       1.2,
		"vmess":       0.8,
	}, s.typePriority)
}

// The name weight says how good the line is, the type weight says how well the
// protocol survives the traffic; both multiply into the same factor.
func TestTypePriorityMultipliesWithPolicyPriority(t *testing.T) {
	s := &Smart{
		policyPriority: make([]priorityRule, 0),
		typePriority:   make(map[string]float64),
	}
	s.applyPolicyPriority(`优:2`)
	s.applyTypePriority("ss:1.5;vmess:0.8")

	require.Equal(t, 3.0, s.getPriorityFactor(priorityStubProxy{name: "香港 优 ss", kind: C.Shadowsocks}))
	require.Equal(t, 2.0, s.getPriorityFactor(priorityStubProxy{name: "香港 优 vless", kind: C.Vless}))
	require.Equal(t, 0.8, s.getPriorityFactor(priorityStubProxy{name: "香港 备 vmess", kind: C.Vmess}))
	require.Equal(t, 1.0, s.getPriorityFactor(priorityStubProxy{name: "香港 备 trojan", kind: C.Trojan}))
}

func TestTypePriorityCachesPerName(t *testing.T) {
	s := &Smart{
		policyPriority: make([]priorityRule, 0),
		typePriority:   make(map[string]float64),
	}
	s.applyTypePriority("ss:1.5")

	require.Equal(t, 1.5, s.getPriorityFactor(priorityStubProxy{name: "node", kind: C.Shadowsocks}))
	// a second call must come from the cache, not from the proxy object
	require.Equal(t, 1.5, s.getPriorityFactor(priorityStubProxy{name: "node", kind: C.Vmess}))
}

func TestStickyKeyUsesSourceAndEffectiveTarget(t *testing.T) {
	source := &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.10")}
	otherSource := &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.11")}

	require.Equal(t, smartStickyKey(source, "example.com"), smartStickyKey(source, "example.com"))
	require.NotEqual(t, smartStickyKey(source, "example.com"), smartStickyKey(otherSource, "example.com"))
	require.NotEqual(t, smartStickyKey(source, "example.com"), smartStickyKey(source, "example.net"))
}

func TestStickyProxyReusesHealthyNode(t *testing.T) {
	s := newStickyTestGroup(t)
	metadata := stickyTestMetadata()
	s.stickyCache.Set(smartStickyKey(metadata, "example.com"), "proxy-b")

	selected := s.stickyProxy(metadata, "example.com", []C.Proxy{
		priorityStubProxy{name: "proxy-a", alive: true, udp: true},
		priorityStubProxy{name: "proxy-b", alive: true, udp: true},
	}, false)
	require.NotNil(t, selected)
	require.Equal(t, "proxy-b", selected.Name())
}

func TestStickyProxyDropsUnavailableNode(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		pin     string
		proxies []C.Proxy
		udp     bool
	}{
		{name: "node left the group", pin: "proxy-b", proxies: []C.Proxy{priorityStubProxy{name: "proxy-a", alive: true, udp: true}}},
		{name: "node is dead", pin: "proxy-b", proxies: []C.Proxy{priorityStubProxy{name: "proxy-b", alive: false, udp: true}}},
		{name: "node cannot carry udp", pin: "proxy-b", proxies: []C.Proxy{priorityStubProxy{name: "proxy-b", alive: true, udp: false}}, udp: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := newStickyTestGroup(t)
			metadata := stickyTestMetadata()
			key := smartStickyKey(metadata, "example.com")
			s.stickyCache.Set(key, testCase.pin)

			require.Nil(t, s.stickyProxy(metadata, "example.com", testCase.proxies, testCase.udp))

			_, cached := s.stickyCache.Get(key)
			require.False(t, cached, "a dropped pin must not be reused")
		})
	}
}

func TestStickyStoreAndClear(t *testing.T) {
	s := newStickyTestGroup(t)
	metadata := stickyTestMetadata()
	key := smartStickyKey(metadata, "example.com")
	s.stickyCache.Set(key, "proxy-a")

	s.clearStickyProxy(metadata)
	_, cached := s.stickyCache.Get(key)
	require.False(t, cached)

	s.storeStickyProxy(metadata, priorityStubProxy{name: "proxy-b"})
	pin, cached := s.stickyCache.Get(key)
	require.True(t, cached)
	require.Equal(t, "proxy-b", pin)
}

// A manual member selection is the user's choice, not a session to keep.
func TestStickySkipsStoreWhileManuallySelected(t *testing.T) {
	s := newStickyTestGroup(t)
	s.selected = "proxy-a"
	metadata := stickyTestMetadata()

	s.storeStickyProxy(metadata, priorityStubProxy{name: "proxy-b"})

	_, cached := s.stickyCache.Get(smartStickyKey(metadata, "example.com"))
	require.False(t, cached)
}

func TestStickySelectProxiesReturnsThePinnedNode(t *testing.T) {
	s := newStickyTestGroup(t)
	metadata := &C.Metadata{
		NetWork: C.TCP,
		SrcIP:   netip.MustParseAddr("192.0.2.10"),
		DstIP:   netip.MustParseAddr("203.0.113.8"),
		Host:    "api.example.com",
	}
	proxies := []C.Proxy{
		priorityStubProxy{name: "proxy-a", alive: true, udp: true},
		priorityStubProxy{name: "proxy-b", alive: true, udp: true},
	}
	target := smart.GetEffectiveTarget(metadata.Host, "")
	s.stickyCache.Set(smartStickyKey(metadata, target), "proxy-b")

	selected, pinned, keepPin := s.selectProxies(metadata, proxies)

	require.Len(t, selected, 1)
	require.Equal(t, "proxy-b", selected[0].Name())
	require.True(t, pinned, "a sticky node must be dialed alone, not raced")
	require.False(t, keepPin)
}

func TestNewSmartRejectsUnknownStrategy(t *testing.T) {
	_, err := NewSmart(GroupCommonOption{Name: "smart"}, SmartOption{Strategy: "round-robin"}, nil, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported strategy")
}

func newStickyTestGroup(t *testing.T) *Smart {
	t.Helper()
	smart.InitCache()
	smart.InitQueue()

	s := sweepGroup(t.Name())
	s.configName = "config"
	s.testUrl = "https://example.com/generate_204"
	s.testUrlHost = "example.com"
	s.store = &smart.Store{}
	s.hostFailLimit.Store(100)
	s.strategy = smartStrategyStickySessions
	s.stickyCache = lru.New[uint64, string](
		lru.WithAge[uint64, string](int64(smartStickyCacheAge.Seconds())),
		lru.WithSize[uint64, string](smartStickyCacheSize),
	)
	return s
}

func stickyTestMetadata() *C.Metadata {
	return &C.Metadata{
		NetWork:        C.TCP,
		SrcIP:          netip.MustParseAddr("192.0.2.10"),
		DstIP:          netip.MustParseAddr("203.0.113.8"),
		Host:           "api.example.com",
		WildcardTarget: "example.com",
		SmartTarget:    "example.com",
	}
}
