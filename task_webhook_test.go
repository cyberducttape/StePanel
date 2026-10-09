package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/safehttp"
)

var loopbackWebhookPolicy = safehttp.Policy{AllowPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}

// webhookClient trusts server's certificate and enforces policy.
func webhookClient(server *httptest.Server, policy safehttp.Policy) *http.Client {
	client := policy.Client(5*time.Second, 0, safehttp.TransportOptions{DialTimeout: 2 * time.Second})
	client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return client
}

func TestTaskWebhookDeliversCompletionPayload(t *testing.T) {
	received := make(chan taskWebhookPayload, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload taskWebhookPayload
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	args := []string{server.URL + "/hook", "demo", "nightly", "exit-code", "exited", "1"}
	if err := sendTaskWebhook(context.Background(), webhookClient(server, loopbackWebhookPolicy), loopbackWebhookPolicy, args); err != nil {
		t.Fatal(err)
	}
	want := taskWebhookPayload{Site: "demo", Task: "nightly", Result: "exit-code", ExitCode: "exited", ExitStatus: "1"}
	if got := <-received; got != want {
		t.Fatalf("payload = %+v, want %+v", got, want)
	}
}

func TestTaskWebhookRefusesPrivateDestinationsByDefault(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("private webhook destination was contacted")
	}))
	defer server.Close()
	for _, target := range []string{server.URL + "/hook", "https://169.254.169.254/latest/meta-data/", "https://10.0.0.10/hook", "https://[::1]/hook"} {
		err := sendTaskWebhook(context.Background(), webhookClient(server, safehttp.Policy{}), safehttp.Policy{}, []string{target, "demo", "nightly", "success", "exited", "0"})
		if err == nil {
			t.Errorf("webhook to %s delivered", target)
		}
	}
}

func TestTaskWebhookEnforcesPolicyAtConnectTime(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("loopback webhook destination was contacted")
	}))
	defer server.Close()
	// The URL passes save-time validation (allowlist), but the client enforces
	// the default policy, as it would if DNS changed after the URL was saved.
	err := sendTaskWebhook(context.Background(), webhookClient(server, safehttp.Policy{}), loopbackWebhookPolicy, []string{server.URL, "demo", "nightly", "success", "exited", "0"})
	if !errors.Is(err, safehttp.ErrDisallowedDestination) {
		t.Fatalf("connect-time error = %v, want ErrDisallowedDestination", err)
	}
}

func TestTaskWebhookDoesNotFollowRedirects(t *testing.T) {
	var hits int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	err := sendTaskWebhook(context.Background(), webhookClient(server, loopbackWebhookPolicy), loopbackWebhookPolicy, []string{server.URL, "demo", "nightly", "success", "exited", "0"})
	if err == nil || hits != 1 {
		t.Fatalf("redirect: err = %v, hits = %d; want refusal after one request", err, hits)
	}
}

func TestTaskWebhookReportsReceiverFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	err := sendTaskWebhook(context.Background(), webhookClient(server, loopbackWebhookPolicy), loopbackWebhookPolicy, []string{server.URL, "demo", "nightly", "success", "exited", "0"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("receiver failure error = %v", err)
	}
}

func TestTaskWebhookRequiresAllArguments(t *testing.T) {
	if err := runTaskWebhook(context.Background(), safehttp.Policy{}, []string{"https://hooks.example.com/"}, strings.NewReader("")); err == nil {
		t.Fatal("missing arguments accepted")
	}
}

func TestTaskWebhookReadsURLFromStdin(t *testing.T) {
	args, err := taskWebhookArgs([]string{"-", "demo", "nightly", "success", "exited", "0"}, strings.NewReader("https://hooks.example/abc?token=secret\n"))
	if err != nil || args[0] != "https://hooks.example/abc?token=secret" || len(args) != 6 {
		t.Fatalf("args = %q, %v", args, err)
	}
	for name, input := range map[string]string{"empty": "", "two lines": "https://a.example\nhttps://b.example\n", "too long": "https://a.example/" + strings.Repeat("a", maxTaskWebhookURL)} {
		if _, err := taskWebhookArgs([]string{"-", "demo", "nightly", "success", "exited", "0"}, strings.NewReader(input)); err == nil {
			t.Errorf("%s stdin URL was accepted", name)
		}
	}
	if args, err := taskWebhookArgs([]string{"https://a.example", "x"}, strings.NewReader("ignored")); err != nil || args[0] != "https://a.example" {
		t.Fatalf("explicit URL args = %q, %v", args, err)
	}
}

// A webhook URL can carry a credential. It must never be loaded into the
// tenant task's environment or passed on any command line.
func TestTaskUnitKeepsWebhookURLFromTenant(t *testing.T) {
	helper, err := os.ReadFile("deploy/integrations/stepanel-appctl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(helper)
	for _, forbidden := range []string{"EnvironmentFile=-$notify_env", "${STEPANEL_TASK_WEBHOOK_URL}"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("task unit still exposes the webhook URL: %s", forbidden)
		}
	}
	for _, required := range []string{
		"ExecStopPost=-+/usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin /usr/local/sbin/stepanel-appctl task-notify",
		`runuser -u stepanel -- env -i /opt/stepanel/stepanel task-webhook - "$site"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("task webhook delivery is missing %q", required)
		}
	}
}
