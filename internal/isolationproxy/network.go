package isolationproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// PublicAddress rejects non-routable, private, shared, documentation, transition
// and metadata ranges. Reject every DNS result if any answer is not public.
func PublicAddress(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(cidr).Contains(addr) {
			return false
		}
	}
	// Currently assigned native IPv6 global unicast; fail closed on future types.
	return addr.Is4() || netip.MustParsePrefix("2000::/3").Contains(addr)
}

func publicTransport() *http.Transport {
	return &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, MaxIdleConns: 8, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true, DialContext: publicDial}
}

type lookupIPFunc func(context.Context, string) ([]net.IPAddr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	dialer.Timeout = 5 * time.Second
	dialer.KeepAlive = 15 * time.Second
	return dialPublic(ctx, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}
func dialPublic(ctx context.Context, address string, lookup lookupIPFunc, dial dialContextFunc) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || !validHost(host) {
		return nil, errors.New("upstream unavailable")
	}
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("upstream unavailable")
	}
	for _, ip := range ips {
		if ip.Zone != "" || !PublicAddress(ip.IP) {
			return nil, errors.New("upstream unavailable")
		}
	}
	for _, ip := range ips {
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("upstream unavailable")
}
