package auth

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

func mustCIDRs(t *testing.T, raw string) TrustedProxies {
	t.Helper()
	out, err := ParseTrustedProxyCIDRs(raw)
	if err != nil {
		t.Fatalf("ParseTrustedProxyCIDRs(%q): %v", raw, err)
	}
	return out
}

func TestLimiterWindowAndReset(t *testing.T) {
	limiter := NewLimiter()
	for i := 0; i < 5; i++ {
		if !limiter.Allow("192.0.2.10") {
			t.Fatalf("attempt %d unexpectedly rejected", i+1)
		}
	}
	if limiter.Allow("192.0.2.10") {
		t.Fatal("sixth attempt should be rejected")
	}
	limiter.Reset("192.0.2.10")
	if !limiter.Allow("192.0.2.10") {
		t.Fatal("reset limiter still rejected login")
	}
}

func TestLimiterBoundsDistinctClients(t *testing.T) {
	limiter := NewLimiter()
	for i := 0; i < maxKeys; i++ {
		if !limiter.Allow(fmt.Sprintf("192.0.2.%d", i)) {
			t.Fatalf("client %d was unexpectedly rejected", i)
		}
	}
	if limiter.Allow("198.51.100.1") {
		t.Fatal("limiter accepted state beyond its configured bound")
	}
}

func TestClientIPDoesNotTrustForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.20")
	if got := ClientIP(req); got != "192.0.2.10" {
		t.Fatalf("client IP = %q, want peer address", got)
	}
}

// TestUntrustedPeerCannotSpoofClientIP is the direct regression test for
// the bool-TrustProxy vulnerability. A request from an arbitrary internet
// client, TLS-terminated somewhere but with no trusted-proxy CIDRs, must
// NOT be able to spoof its identity by setting X-Forwarded-For itself.
func TestUntrustedPeerCannotSpoofClientIP(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "203.0.113.99:1234"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("X-Real-IP", "10.0.0.2")
	// Empty trusted-proxy list: no forwarded header is ever consulted.
	if got := ClientIPWithTrustedProxies(req, nil); got != "203.0.113.99" {
		t.Fatalf("empty CIDRs must not trust forwarded headers, got %q", got)
	}
	// Loopback trusted, but the peer is NOT loopback → same result.
	if got := ClientIPWithTrustedProxies(req, mustCIDRs(t, "127.0.0.1/32,::1/128")); got != "203.0.113.99" {
		t.Fatalf("non-loopback peer with loopback trust list should not honor XFF, got %q", got)
	}
}

// TestTrustedProxyReturnsAttestedClient — a single trusted proxy in
// front, XFF has one entry (the client), that's the answer.
func TestTrustedProxyReturnsAttestedClient(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	trusted := mustCIDRs(t, "127.0.0.1/32")
	if got := ClientIPWithTrustedProxies(req, trusted); got != "203.0.113.7" {
		t.Fatalf("trusted proxy attested client not returned, got %q", got)
	}
}

// TestClientCannotForgeXFFThroughTrustedProxy — the whole point of the
// right-to-left walk. Client sends XFF: victim, then the trusted proxy
// appends the real client IP. The result must be the real IP (the
// rightmost entry after skipping trusted hops), NOT the victim IP the
// client injected at the leftmost position.
func TestClientCannotForgeXFFThroughTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	// The trusted local proxy is 127.0.0.1; it appended the real client
	// (203.0.113.42) after receiving the request from that client, which
	// had already forged its own XFF as "victim-ip".
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "10.9.9.9, 203.0.113.42")
	trusted := mustCIDRs(t, "127.0.0.1/32")
	got := ClientIPWithTrustedProxies(req, trusted)
	if got != "203.0.113.42" {
		t.Fatalf("XFF forgery not defeated: got %q, want 203.0.113.42 (the proxy-attested value)", got)
	}
	if got == "10.9.9.9" {
		t.Fatal("test regressed to the old ips[0] behavior — client-forged victim IP was trusted")
	}
}

// TestMultiHopTrustedChain — two trusted hops in front. Each hop appended
// the identity of the previous hop, so XFF is
// "real-client, hop1, hop2" and the peer is hop2. Walking right-to-left,
// hop2 is trusted (skip), hop1 is trusted (skip), real-client is not
// trusted → return real-client.
func TestMultiHopTrustedChain(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "10.0.0.3:8080"
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1, 10.0.0.2")
	trusted := mustCIDRs(t, "10.0.0.0/24")
	if got := ClientIPWithTrustedProxies(req, trusted); got != "198.51.100.9" {
		t.Fatalf("multi-hop chain resolution wrong, got %q", got)
	}
}

// TestFallbackToXRealIP — X-Forwarded-For absent, X-Real-IP is used but
// only when the peer is a trusted proxy.
func TestFallbackToXRealIP(t *testing.T) {
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Real-IP", "203.0.113.55")
	trusted := mustCIDRs(t, "127.0.0.1/32")
	if got := ClientIPWithTrustedProxies(req, trusted); got != "203.0.113.55" {
		t.Errorf("X-Real-IP not honored from trusted peer, got %q", got)
	}
	// Same header but untrusted peer → ignored.
	req.RemoteAddr = "203.0.113.99:1234"
	if got := ClientIPWithTrustedProxies(req, trusted); got != "203.0.113.99" {
		t.Errorf("X-Real-IP incorrectly honored from untrusted peer, got %q", got)
	}
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	// Empty is empty.
	if got, err := ParseTrustedProxyCIDRs(""); err != nil || len(got) != 0 {
		t.Errorf("empty input: got %v, err %v", got, err)
	}
	// Valid parses.
	if _, err := ParseTrustedProxyCIDRs("127.0.0.1/32, ::1/128, 10.0.20.0/24"); err != nil {
		t.Errorf("valid input failed: %v", err)
	}
	// Invalid rejected.
	for _, bad := range []string{"not-a-cidr", "127.0.0.1", "10.0.0.0/33", "10.0.0.0/-1"} {
		if _, err := ParseTrustedProxyCIDRs(bad); err == nil {
			t.Errorf("ParseTrustedProxyCIDRs(%q) should have failed", bad)
		}
	}
}
