package rootbroker

import (
	"testing"
	"time"
)

func TestRequestTimeoutBoundsCertificateIssuance(t *testing.T) {
	if got := RequestTimeout(nil); got != defaultRequestTimeout {
		t.Fatalf("nil request timeout = %s", got)
	}
	if got := RequestTimeout(&Request{RequestType: "site"}); got != defaultRequestTimeout {
		t.Fatalf("default request timeout = %s", got)
	}
	request := &Request{RequestType: "certificate", Certificate: &CertificateRequest{Action: "issue"}}
	if got, want := RequestTimeout(request), 15*time.Minute; got != want {
		t.Fatalf("certificate timeout = %s, want %s", got, want)
	}
	request.Certificate.Action = "unsupported"
	if got := RequestTimeout(request); got != defaultRequestTimeout {
		t.Fatalf("unsupported certificate timeout = %s", got)
	}
}

func TestRequestTimeoutCoversPanelTimeoutClasses(t *testing.T) {
	cases := []struct {
		name string
		req  *Request
		want time.Duration
	}{
		{"app apply", &Request{RequestType: "app", App: &AppRequest{Action: "apply"}}, 60 * time.Second},
		{"task apply", &Request{RequestType: "task", Task: &TaskRequest{Action: "apply"}}, 60 * time.Second},
		{"site delete", &Request{RequestType: "site", Site: &SiteRequest{Action: "delete"}}, 60 * time.Second},
		{"git delete", &Request{RequestType: "git", Git: &GitRequest{Action: "delete"}}, 60 * time.Second},
		{"composer install", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "composer-install"}}, 15 * time.Minute},
		{"python apply", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "python-apply"}}, 15 * time.Minute},
		{"node tool", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "node-tool"}}, 15 * time.Minute},
		{"worker restart", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "worker-restart"}}, 60 * time.Second},
		{"sitectl delete", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "sitectl", Action: "delete"}}, 60 * time.Second},
		{"proxy apply", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "proxyctl", Action: "apply"}}, defaultRequestTimeout},
		{"nil helper", &Request{RequestType: "helper"}, defaultRequestTimeout},
	}
	for _, tc := range cases {
		if got := RequestTimeout(tc.req); got != tc.want {
			t.Errorf("%s timeout = %s, want %s", tc.name, got, tc.want)
		}
	}
}
