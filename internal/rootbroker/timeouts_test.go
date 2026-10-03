package rootbroker

import (
	"os"
	"regexp"
	"strings"
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
		{"site delete", &Request{RequestType: "site", Site: &SiteRequest{Action: "delete"}}, 120 * time.Minute},
		{"site seal", &Request{RequestType: "site", Site: &SiteRequest{Action: "seal"}}, 120 * time.Minute},
		{"database dump", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "dump"}}, 120 * time.Minute},
		{"database restore", &Request{RequestType: "db", DB: &DBRequest{Action: "restore-dump"}}, 120 * time.Minute},
		{"database provision", &Request{RequestType: "db", DB: &DBRequest{Action: "provision"}}, 60 * time.Minute},
		{"git clone", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "gitctl", Action: "clone"}}, 30 * time.Minute},
		{"runner build", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "runnerctl", Action: "build"}}, 30 * time.Minute},
		{"git delete", &Request{RequestType: "git", Git: &GitRequest{Action: "delete"}}, 60 * time.Second},
		{"composer install", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "composer-install"}}, 15 * time.Minute},
		{"python apply", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "python-apply"}}, 15 * time.Minute},
		{"node tool", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "node-tool"}}, 15 * time.Minute},
		{"worker restart", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "worker-restart"}}, 60 * time.Second},
		{"sitectl delete", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "sitectl", Action: "delete"}}, 120 * time.Minute},
		{"proxy apply", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "proxyctl", Action: "apply"}}, defaultRequestTimeout},
		{"nil helper", &Request{RequestType: "helper"}, defaultRequestTimeout},
	}
	for _, tc := range cases {
		if got := RequestTimeout(tc.req); got != tc.want {
			t.Errorf("%s timeout = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Every action the broker accepts must have an explicit timeout class, so a
// new long-running action cannot silently inherit the 30-second default and
// be SIGKILLed mid-operation.
func TestEveryBrokerActionHasATimeoutClass(t *testing.T) {
	for name, actions := range helperSchemas {
		for action := range actions {
			if _, ok := helperTimeouts[name][action]; !ok {
				t.Errorf("helper %s/%s has no timeout class in helperTimeouts", name, action)
			}
		}
	}
	source, err := os.ReadFile("broker.go")
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string]string{
		"site": "handleSiteRequest", "app": "handleAppRequest", "db": "handleDBRequest",
		"vhost": "handleVhostRequest", "proxy": "handleProxyRequest", "git": "handleGitRequest", "task": "handleTaskRequest",
	}
	text := string(source)
	for requestType, handler := range handlers {
		start := strings.Index(text, "func (b *Broker) "+handler+"(")
		if start < 0 {
			t.Fatalf("%s not found in broker.go", handler)
		}
		body := text[start:]
		body = body[:strings.Index(body, "\n}\n")]
		for _, match := range regexp.MustCompile(`case ((?:"[a-z-]+"(?:, )?)+):`).FindAllStringSubmatch(body, -1) {
			for _, action := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(match[1], -1) {
				if _, ok := typedTimeouts[requestType][action[1]]; !ok {
					t.Errorf("typed %s/%s has no timeout class in typedTimeouts", requestType, action[1])
				}
			}
		}
	}
}
