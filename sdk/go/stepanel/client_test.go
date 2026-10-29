package stepanel

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRefuseInsecureRedirect(t *testing.T) {
	request := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	if err := refuseInsecureRedirect(request("http://panel.internal/api/sites"), nil); err == nil {
		t.Fatal("redirect to non-TLS remote host was allowed")
	}
	for _, allowed := range []string{"https://panel.internal/api/sites", "http://127.0.0.1:8080/api/sites", "http://localhost/api/sites"} {
		if err := refuseInsecureRedirect(request(allowed), nil); err != nil {
			t.Fatalf("%s: %v", allowed, err)
		}
	}
	if err := refuseInsecureRedirect(request("https://panel.internal/"), make([]*http.Request, 10)); err == nil {
		t.Fatal("redirect loop was not bounded")
	}
}
