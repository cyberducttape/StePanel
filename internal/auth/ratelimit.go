// Package auth contains authentication policy that is independent of the
// HTTP application assembly and platform helpers.
package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginAttempt struct {
	count int
	start time.Time
}

// Limiter bounds failed-login work per client and bounds the number of client
// keys retained in memory. It intentionally has no persistence or HTTP state.
type Limiter struct {
	mu      sync.Mutex
	attempt map[string]loginAttempt
	lastGC  time.Time
}

const maxKeys = 10_000

// NewLimiter creates a bounded login-attempt limiter.
func NewLimiter() *Limiter {
	return &Limiter{attempt: make(map[string]loginAttempt), lastGC: time.Now()}
}

// Allow admits one attempt when the client remains within the five-attempt,
// fifteen-minute window and the bounded key table has capacity.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) >= time.Minute {
		for candidate, attempt := range l.attempt {
			if now.Sub(attempt.start) >= 15*time.Minute {
				delete(l.attempt, candidate)
			}
		}
		l.lastGC = now
	}
	attempt := l.attempt[key]
	if attempt.start.IsZero() || now.Sub(attempt.start) >= 15*time.Minute {
		if len(l.attempt) >= maxKeys {
			return false
		}
		l.attempt[key] = loginAttempt{count: 1, start: now}
		return true
	}
	if attempt.count >= 5 {
		return false
	}
	attempt.count++
	l.attempt[key] = attempt
	return true
}

// Reset clears the attempt window for one client after successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.attempt, key)
	l.mu.Unlock()
}

// ClientIP extracts the peer address without trusting forwarded headers.
// Reverse-proxy deployments should pass the panel only a trusted transport
// boundary rather than allowing browsers to forge client identity headers.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}

// TrustedProxies is a set of CIDR ranges from which forwarded-identity
// headers (X-Forwarded-For, X-Real-IP) are trusted. Any request whose
// direct peer (RemoteAddr) falls outside every listed CIDR is treated as
// an untrusted direct client — no forwarded header is consulted, and the
// peer address is used as-is.
//
// The bool "TrustProxy" flag this replaces was structurally unsafe: it
// trusted forwarded headers unconditionally as long as the deployment was
// TLS-terminated somewhere, so any client that could reach the panel
// could spoof any client IP by setting X-Forwarded-For itself. Rate
// limiting keys and audit-log client identities were both forgeable.
type TrustedProxies []*net.IPNet

// ParseTrustedProxyCIDRs parses a comma-separated list of CIDRs (for
// example "127.0.0.1/32, ::1/128, 10.0.20.0/24"). Empty entries and
// surrounding whitespace are ignored. An empty input returns an empty
// TrustedProxies (which trusts nothing).
func ParseTrustedProxyCIDRs(raw string) (TrustedProxies, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out TrustedProxies
	for _, piece := range strings.Split(raw, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		_, network, err := net.ParseCIDR(piece)
		if err != nil {
			return nil, err
		}
		out = append(out, network)
	}
	return out, nil
}

// Contains reports whether ip is inside any of the trusted CIDRs.
func (tp TrustedProxies) Contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range tp {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIPWithTrustedProxies extracts the origin IP for a request,
// consulting forwarded-identity headers only when the direct peer
// (RemoteAddr) is inside one of the trusted-proxy CIDRs.
//
// XFF algorithm (RFC 7239 style):
//
//   - X-Forwarded-For is a comma-separated list appended left-to-right as
//     the request traverses hops: "originalClient, hop1, hop2". The
//     leftmost entry is what the client — or an attacker upstream of
//     every proxy — claims to be.
//   - The prior implementation returned ips[0] unconditionally, which
//     meant any HTTP client that could reach the panel could send
//     "X-Forwarded-For: victim-ip" and impersonate victim-ip in rate
//     limiting and audit records. The comment above that code claimed
//     "rightmost is the original client", which was also wrong: leftmost
//     is the original client per the standard.
//   - The safe algorithm walks the list right-to-left, skipping any hop
//     that is itself a trusted proxy, and returns the first non-trusted
//     IP encountered. That returned IP is the last hop before the trust
//     chain — the attested client IP.
//
// X-Real-IP is a single value; it is only consulted when RemoteAddr is
// trusted, and only when there is no X-Forwarded-For.
func ClientIPWithTrustedProxies(r *http.Request, trustedProxies TrustedProxies) string {
	peer := ClientIP(r)
	peerIP := net.ParseIP(peer)
	if peerIP == nil || !trustedProxies.Contains(peerIP) {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		for i := len(ips) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(ips[i])
			if candidate == "" {
				continue
			}
			parsed := net.ParseIP(candidate)
			if parsed == nil {
				continue
			}
			if !trustedProxies.Contains(parsed) {
				return candidate
			}
		}
		// The entire XFF chain is trusted proxies — treat the direct peer
		// as the effective client rather than returning "" or the raw
		// header, which would corrupt the rate-limiter key space.
		return peer
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}
	return peer
}
