#!/system/bin/sh
# Read-only rooted-Android acceptance probe. It never starts/stops mihomo or
# changes addresses, routes, processes, firewall rules, or installed files.
set -eu

CONTROLLER=${CONTROLLER:-http://127.0.0.1:9090}
SECRET=${SECRET:-}
INSTANCE=${INSTANCE:-mesh}
EXPECTED_IPV4=${EXPECTED_IPV4:-}
EXPECTED_IPV6=${EXPECTED_IPV6:-}
EXPECTED_IPV4_ROUTE=${EXPECTED_IPV4_ROUTE:-}
EXPECTED_IPV6_ROUTE=${EXPECTED_IPV6_ROUTE:-}
REMOTE_IPV4=${REMOTE_IPV4:-}
REMOTE_IPV6=${REMOTE_IPV6:-}
TUN_INTERFACE=${TUN_INTERFACE:-}
MAGIC_DNS_NAMES=${MAGIC_DNS_NAMES:-}
DNS_SERVER=${DNS_SERVER:-127.0.0.1}
SKIP_PACKET_PROBE=${SKIP_PACKET_PROBE:-0}

expected_ipv4=${EXPECTED_IPV4%%/*}
expected_ipv6=${EXPECTED_IPV6%%/*}

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
pass() { echo "PASS: $*"; }
require_equal() { if [ "$1" = "$2" ]; then pass "$3"; else fail "$3"; fi; }
check_up() {
    if ip -o link show dev "$1" | grep -Eq '(<|,)UP(,|>)'; then pass "overlay interface $1 is administratively up"; else fail "overlay interface $1 is not administratively up"; fi
}
check_route() {
    [ -n "$2" ] || return 0
    if ip "-$1" route show table all | awk -v expected="$2" -v iface="$overlay_iface" '$1 == expected { for (i = 1; i < NF; i++) if ($i == "dev" && (iface == "" || $(i + 1) == iface)) found = 1 } END { exit !found }'; then
        pass "IPv$1 route $2 selects overlay interface $overlay_iface"
    else
        fail "IPv$1 route $2 does not select overlay interface $overlay_iface"
    fi
}

if [ "$(id -u)" != 0 ]; then
    echo 'Run this probe through su -c as root; it does not elevate itself.' >&2
    exit 2
fi

if command -v curl >/dev/null 2>&1; then
    http_get() {
        if [ -n "$SECRET" ]; then
            curl -fsS --connect-timeout 3 --max-time 8 -H "Authorization: Bearer $SECRET" "$1"
        else
            curl -fsS --connect-timeout 3 --max-time 8 "$1"
        fi
    }
elif command -v wget >/dev/null 2>&1; then
    http_get() {
        if [ -n "$SECRET" ]; then
            wget -qO- --timeout=8 --header="Authorization: Bearer $SECRET" "$1"
        else
            wget -qO- --timeout=8 "$1"
        fi
    }
else
    echo 'A pre-existing curl or wget is required for the controller check; no tool is installed by this script.' >&2
    exit 2
fi

url="$CONTROLLER/easytier/$INSTANCE"
json=$(http_get "$url") || { fail "GET $url failed"; json=; }
if [ -n "$json" ]; then
    printf '%s\n' "$json"
    case "$json" in *'"state":"running"'*) pass 'EasyTier instance state is running' ;; *) fail 'EasyTier instance is not reported as running' ;; esac
    case "$json" in *'"closed":false'*) pass 'at least one peer connection is live' ;; *) fail 'no live peer connection was found' ;; esac
    if [ -n "$EXPECTED_IPV4" ]; then
        case "$json" in *"\"ipv4\":\"$expected_ipv4\""*|*"\"ipv4\":\"$expected_ipv4/"*) pass "node IPv4 is $expected_ipv4" ;; *) fail "node IPv4 $expected_ipv4 was not found in status" ;; esac
    fi
    if [ -n "$EXPECTED_IPV6" ]; then
        if printf '%s' "$json" | grep -Fq "\"ipv6\":\"$EXPECTED_IPV6\"" ||
            printf '%s' "$json" | grep -Fq "\"ipv6\":\"$expected_ipv6\""; then
            pass "node IPv6 is $EXPECTED_IPV6"
        else
            fail "node IPv6 $expected_ipv6 was not found in status"
        fi
    fi
fi

ip_available=1
if ! command -v ip >/dev/null 2>&1; then
    fail 'ip command is required for Android address/route checks'
    ip_available=0
fi
if [ "$ip_available" = 1 ]; then
    overlay_iface=$TUN_INTERFACE
    if [ -n "$TUN_INTERFACE" ]; then
        if ip link show "$TUN_INTERFACE" >/dev/null 2>&1; then
            pass "named TUN interface $TUN_INTERFACE exists"
            check_up "$TUN_INTERFACE"
        else
            fail "named TUN interface $TUN_INTERFACE does not exist"
        fi
    fi
    if [ -n "$EXPECTED_IPV4" ]; then
        count=$(ip -o -4 addr show | awk -v expected="$expected_ipv4" 'split($4, address, "/") && address[1] == expected { print $2 }' | sort -u | wc -l | tr -d ' ')
        require_equal "$count" 1 "overlay IPv4 $expected_ipv4 is assigned on one interface"
        iface=$(ip -o -4 addr show | awk -v expected="$expected_ipv4" 'split($4, address, "/") && address[1] == expected { print $2; exit }')
        if [ -n "$iface" ]; then
            if [ -n "$overlay_iface" ]; then require_equal "$iface" "$overlay_iface" 'overlay IPv4 uses the shared TUN'; else overlay_iface=$iface; fi
            check_up "$iface"
        fi
    fi
    if [ -n "$EXPECTED_IPV6" ]; then
        count=$(ip -o -6 addr show | awk -v expected="$expected_ipv6" 'split($4, address, "/") && address[1] == expected { print $2 }' | sort -u | wc -l | tr -d ' ')
        require_equal "$count" 1 "overlay IPv6 $expected_ipv6 is assigned on one interface"
        iface=$(ip -o -6 addr show | awk -v expected="$expected_ipv6" 'split($4, address, "/") && address[1] == expected { print $2; exit }')
        if [ -n "$iface" ]; then
            if [ -n "$overlay_iface" ]; then require_equal "$iface" "$overlay_iface" 'overlay IPv6 uses the shared TUN'; else overlay_iface=$iface; fi
            check_up "$iface"
        fi
    fi
    check_route 4 "$EXPECTED_IPV4_ROUTE"
    check_route 6 "$EXPECTED_IPV6_ROUTE"
fi

if [ -n "$MAGIC_DNS_NAMES" ]; then
    if command -v nslookup >/dev/null 2>&1; then
        for name in $MAGIC_DNS_NAMES; do
            if nslookup "$name" "$DNS_SERVER" >/dev/null 2>&1; then pass "DNS returned a record for $name"; else fail "DNS query $name through $DNS_SERVER failed"; fi
        done
    else
        echo 'SKIP: nslookup is unavailable; query Magic DNS with an existing application or DNS client.'
    fi
fi

if [ "$SKIP_PACKET_PROBE" != 1 ]; then
    if [ -n "$REMOTE_IPV4" ]; then
        if ping -c 2 -W 2 "$REMOTE_IPV4" >/dev/null 2>&1; then pass "ICMP reaches remote overlay IPv4 $REMOTE_IPV4"; else fail "ICMP probe to $REMOTE_IPV4 failed"; fi
    fi
    if [ -n "$REMOTE_IPV6" ]; then
        if ping6 -c 2 -W 2 "$REMOTE_IPV6" >/dev/null 2>&1; then pass "ICMP reaches remote overlay IPv6 $REMOTE_IPV6"; else fail "ICMP probe to $REMOTE_IPV6 failed"; fi
    fi
fi

if [ "$failures" -ne 0 ]; then
    echo "$failures acceptance check(s) failed." >&2
    exit 1
fi
echo 'All requested read-only acceptance checks passed.'
