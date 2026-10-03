package safehttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":                  true,
		"1.1.1.1":                  true,
		"2606:4700:4700::1111":     true,
		"127.0.0.1":                false,
		"10.0.0.10":                false,
		"172.16.4.4":               false,
		"172.31.255.255":           false,
		"192.168.1.1":              false,
		"169.254.169.254":          false,
		"100.64.0.1":               false,
		"198.18.0.1":               false,
		"0.0.0.0":                  false,
		"255.255.255.255":          false,
		"240.0.0.1":                false,
		"224.0.0.1":                false,
		"203.0.113.9":              false,
		"::1":                      false,
		"::":                       false,
		"::ffff:127.0.0.1":         false,
		"::ffff:10.0.0.1":          false,
		"64:ff9b::a00:1":           false, // NAT64 embedding 10.0.0.1
		"2002:a00:1::1":            false, // 6to4 embedding 10.0.0.1
		"2001:0:4136:e378::1":      false, // Teredo
		"fd00::1":                  false,
		"fe80::1":                  false,
		"2001:db8::1":              false,
		"ff02::1":                  false,
		"fd00:ec2::254":            false, // AWS IPv6 metadata
		"::ffff:8.8.8.8":           true,
		"2a00:1450:4001:80b::200e": true,
	}
	for raw, want := range cases {
		if got := IsPublic(netip.MustParseAddr(raw)); got != want {
			t.Errorf("IsPublic(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestValidateURL(t *testing.T) {
	var p Policy
	allowed := []string{
		"https://hooks.example.com/task",
		"https://8.8.8.8/hook",
		"https://hooks.example.com:8443/task?x=1",
	}
	for _, raw := range allowed {
		if err := p.ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
	rejected := []string{
		"http://hooks.example.com/",
		"https://user:pass@hooks.example.com/",
		"https://hooks.example.com/#frag",
		"https:///nohost",
		"https://localhost/",
		"https://api.localhost/",
		"https://127.0.0.1/",
		"https://10.0.0.10/",
		"https://172.16.0.1/",
		"https://192.168.0.1/",
		"https://[::1]/",
		"https://[::ffff:127.0.0.1]/",
		"https://169.254.169.254/latest/meta-data/",
		"https://100.64.0.1/",
		"https://" + strings.Repeat("a", 2050) + ".com/",
	}
	for _, raw := range rejected {
		if err := p.ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want rejection", raw)
		}
	}
}

func TestPolicyAllowPrefixes(t *testing.T) {
	p := Policy{AllowPrefixes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	if !p.Allows(netip.MustParseAddr("10.20.1.1")) {
		t.Fatal("explicitly allowed private prefix rejected")
	}
	if p.Allows(netip.MustParseAddr("10.21.1.1")) {
		t.Fatal("private address outside the allowlist accepted")
	}
	if err := p.ValidateURL("https://10.20.1.1/hook"); err != nil {
		t.Fatalf("allowlisted literal rejected: %v", err)
	}
}

// loopbackClient returns a policy client that trusts server's certificate, so
// only the address policy decides whether the request succeeds.
func loopbackClient(t *testing.T, p Policy, server *httptest.Server, maxRedirects int) *http.Client {
	t.Helper()
	client := p.Client(5*time.Second, maxRedirects, TransportOptions{DialTimeout: 2 * time.Second})
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return client
}

func TestClientRefusesLoopbackAtConnectTime(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := loopbackClient(t, Policy{}, server, 0)
	_, err := client.Get(server.URL)
	if !errors.Is(err, ErrDisallowedDestination) {
		t.Fatalf("loopback request error = %v, want ErrDisallowedDestination", err)
	}

	// A hostname is resolved by the dialer and still checked on the final
	// address, which is what defeats DNS rebinding.
	u, _ := url.Parse(server.URL)
	_, err = client.Get("https://localhost:" + u.Port() + "/")
	if !errors.Is(err, ErrDisallowedDestination) {
		t.Fatalf("hostname resolving to loopback error = %v, want ErrDisallowedDestination", err)
	}

	allowed := loopbackClient(t, Policy{AllowPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, server, 0)
	resp, err := allowed.Get(server.URL)
	if err != nil {
		t.Fatalf("allowlisted loopback request: %v", err)
	}
	resp.Body.Close()
}

func TestClientRevalidatesRedirects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	// Allow the first hop so only the redirect is under test.
	p := Policy{AllowPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	_, err := loopbackClient(t, p, server, 5).Get(server.URL + "/start")
	if err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("redirect to metadata error = %v, want redirect rejection", err)
	}
}

func TestClientIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	transport := Policy{}.Transport(TransportOptions{})
	if transport.Proxy != nil {
		t.Fatal("transport honours environment proxies")
	}
}
