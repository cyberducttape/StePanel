// Package safehttp is the single outbound HTTP policy for StePanel-originated
// requests (archive imports, task webhooks). By default only public internet
// destinations are reachable:
//
//   - The address check runs in the dialer's Control hook, on the exact IP the
//     socket is about to connect to. DNS answers, rebinding between lookups,
//     and resolver environment variables cannot bypass it.
//   - Every redirect is revalidated and re-dialed through the same hook.
//   - Proxies from the environment are ignored, so HTTPS_PROXY cannot route
//     around the policy.
//
// Operators may allow specific private prefixes explicitly via Policy.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrDisallowedDestination reports a destination outside the policy.
var ErrDisallowedDestination = errors.New("destination is not a public internet address")

// nonPublicPrefixes are IANA special-purpose and non-routable ranges. IPv4
// mapped IPv6 addresses are unmapped before checking. Translation prefixes
// (NAT64, 6to4, Teredo) are rejected because they can embed private IPv4
// destinations.
var nonPublicPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this" network
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // carrier-grade NAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, cloud metadata
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay anycast
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, broadcast
	"::/128",          // unspecified
	"::1/128",         // loopback
	"64:ff9b::/96",    // NAT64
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard-only
	"2001::/23",       // IETF protocol assignments, including Teredo
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4
	"3fff::/20",       // documentation
	"fc00::/7",        // unique local
	"fe80::/10",       // link-local
	"fec0::/10",       // deprecated site-local
	"ff00::/8",        // multicast
)

// globalUnicastIPv6 is the only IPv6 space currently allocated for global
// unicast; anything outside it is not a public destination.
var globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}

// IsPublic reports whether addr is a public internet unicast address.
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	if addr.Is6() && !globalUnicastIPv6.Contains(addr) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Policy decides which destinations outbound requests may reach.
type Policy struct {
	// AllowPrefixes lists non-public ranges an administrator has explicitly
	// approved (for example an internal webhook receiver). Empty by default.
	AllowPrefixes []netip.Prefix
}

// Allows reports whether the policy permits connecting to addr.
func (p Policy) Allows(addr netip.Addr) bool {
	addr = addr.Unmap()
	if IsPublic(addr) {
		return true
	}
	for _, prefix := range p.AllowPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ValidateURL checks a destination URL before it is stored or requested:
// HTTPS only, no credentials or fragment, and a literal IP host must be
// allowed. Hostnames are checked again at connect time, so passing here is
// necessary but not sufficient.
func (p Policy) ValidateURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("URL must use https")
	}
	if u.User != nil {
		return errors.New("URL must not contain credentials")
	}
	if u.Fragment != "" || u.Opaque != "" {
		return errors.New("URL must not contain a fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return errors.New("URL must include a host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: %s", ErrDisallowedDestination, host)
	}
	if addr, err := netip.ParseAddr(host); err == nil && !p.Allows(addr) {
		return fmt.Errorf("%w: %s", ErrDisallowedDestination, host)
	}
	return nil
}

// control rejects a connection whose resolved peer address is not allowed.
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrDisallowedDestination, address)
	}
	if !p.Allows(addrPort.Addr()) {
		return fmt.Errorf("%w: %s", ErrDisallowedDestination, addrPort.Addr())
	}
	return nil
}

// Dialer returns a dialer that enforces the policy on every connection
// attempt, including each address tried during happy-eyeballs fallback.
func (p Policy) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: p.control}
}

// TransportOptions tunes the transport timeouts.
type TransportOptions struct {
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

// Transport returns an HTTP transport that only reaches allowed destinations.
func (p Policy) Transport(opts TransportOptions) *http.Transport {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	if opts.TLSHandshakeTimeout <= 0 {
		opts.TLSHandshakeTimeout = 15 * time.Second
	}
	if opts.ResponseHeaderTimeout <= 0 {
		opts.ResponseHeaderTimeout = 30 * time.Second
	}
	dialer := p.Dialer(opts.DialTimeout)
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
}

// CheckRedirect returns a redirect policy that revalidates every hop and
// stops after maxRedirects. maxRedirects of 0 refuses all redirects.
func (p Policy) CheckRedirect(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("redirect limit exceeded (%d)", maxRedirects)
		}
		if err := p.ValidateURL(req.URL.String()); err != nil {
			return fmt.Errorf("redirect rejected: %w", err)
		}
		return nil
	}
}

// Client returns an HTTP client bound to the policy.
func (p Policy) Client(timeout time.Duration, maxRedirects int, opts TransportOptions) *http.Client {
	return &http.Client{
		Transport:     p.Transport(opts),
		Timeout:       timeout,
		CheckRedirect: p.CheckRedirect(maxRedirects),
	}
}
