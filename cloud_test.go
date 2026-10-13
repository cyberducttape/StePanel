package stepanel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type cloudRoundTripper func(*http.Request) (*http.Response, error)

func (f cloudRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func linodeTestClient(server *httptest.Server) *http.Client {
	target, _ := url.Parse(server.URL)
	return &http.Client{Transport: cloudRoundTripper(func(request *http.Request) (*http.Response, error) {
		request = request.Clone(request.Context())
		requestURL := *request.URL
		requestURL.Scheme = target.Scheme
		requestURL.Host = target.Host
		request.URL = &requestURL
		request.Host = target.Host
		return http.DefaultTransport.RoundTrip(request)
	})}
}

func TestLinodeAPIRequestAggregatesEveryPage(t *testing.T) {
	requestedPages := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v4/domains/123/records" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		page := request.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		requestedPages = append(requestedPages, page)
		id := 1
		if _, err := fmt.Sscanf(page, "%d", &id); err != nil || id < 1 || id > 3 {
			t.Errorf("unexpected page query %q", page)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(response, `{"page":%d,"pages":3,"results":3,"data":[{"id":%d}]}`, id, id)
	}))
	defer server.Close()

	value, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("response type = %T, want map", value)
	}
	data, ok := root["data"].([]any)
	if !ok || len(data) != 3 {
		t.Fatalf("aggregated data = %#v, want three records", root["data"])
	}
	if got := root["results"]; got != float64(3) {
		t.Fatalf("aggregated results = %#v, want 3", got)
	}
	if strings.Join(requestedPages, ",") != "1,2,3" {
		t.Fatalf("requested pages = %v, want [1 2 3]", requestedPages)
	}
}

func TestLinodeAPIRequestRejectsIncompletePagination(t *testing.T) {
	t.Run("page limit", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			calls++
			_, _ = fmt.Fprintf(response, `{"page":1,"pages":%d,"results":%d,"data":[{"id":1}]}`, maxCloudPaginationPages+1, maxCloudPaginationPages+1)
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "page safety limit") {
			t.Fatalf("request error = %v, want explicit page-limit failure", err)
		}
		if calls != 1 {
			t.Fatalf("requests made = %d, want 1", calls)
		}
	})

	t.Run("missing page", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("page") == "2" {
				response.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = response.Write([]byte(`{"page":1,"pages":2,"results":2,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "empty response for page 2") {
			t.Fatalf("request error = %v, want explicit missing-page failure", err)
		}
	})

	t.Run("advertised total mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"page":1,"pages":1,"results":2,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "single-page result count") {
			t.Fatalf("request error = %v, want total-results mismatch", err)
		}
	})
	t.Run("aggregate response budget", func(t *testing.T) {
		calls := 0
		largeValue := strings.Repeat("x", 7<<20)
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			calls++
			page := request.URL.Query().Get("page")
			if page == "" {
				page = "1"
			}
			var number int
			if _, err := fmt.Sscanf(page, "%d", &number); err != nil {
				t.Errorf("parse page %q: %v", page, err)
			}
			_, _ = fmt.Fprintf(response, `{"page":%d,"pages":5,"results":5,"data":[{"large":"%s"}]}`, number, largeValue)
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "aggregate response limit") {
			t.Fatalf("request error = %v, want aggregate byte-budget failure", err)
		}
		if calls != 5 {
			t.Fatalf("provider pages fetched = %d, want stop at page 5 budget breach", calls)
		}
	})
	t.Run("item budget", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(response, `{"page":1,"pages":2,"results":%d,"data":[{"id":1}]}`, maxCloudPaginationItems+1)
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "item safety limit") {
			t.Fatalf("request error = %v, want item-budget failure", err)
		}
	})
	t.Run("time budget", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("page") == "2" {
				<-request.Context().Done()
				return
			}
			_, _ = response.Write([]byte(`{"page":1,"pages":2,"results":2,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		started := time.Now()
		_, err := linodeAPIRequestWithClientBudget(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server), 20*time.Millisecond)
		if err == nil {
			t.Fatal("pagination exceeded its time budget without failing")
		}
		if time.Since(started) > time.Second {
			t.Fatalf("time budget took too long to enforce: %s", time.Since(started))
		}
	})
}

func TestLinodeAPIRequestAcceptsInstanceBackupObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v4/linode/instances/123/backups" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"automatic":[],"snapshot":{"current":null,"in_progress":null}}`))
	}))
	defer server.Close()
	value, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/linode/instances/123/backups", nil, "test-token", linodeTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	root, ok := value.(map[string]any)
	if !ok || root["snapshot"] == nil {
		t.Fatalf("backup response = %#v, want unpaginated backup object", value)
	}
}

func TestLinodeSnapshotRequestMatchesProviderContract(t *testing.T) {
	method, path, payload, err := linodeInstanceActionRequest("snapshot", "123", "before-maintenance", true)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/linode/instances/123/backups" {
		t.Fatalf("snapshot request = %s %s", method, path)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v4/linode/instances/123/backups" {
			t.Errorf("provider request = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode snapshot body: %v", err)
		}
		if body["label"] != "before-maintenance" {
			t.Errorf("snapshot body = %#v", body)
		}
		_, _ = response.Write([]byte(`{"id":44,"type":"snapshot"}`))
	}))
	defer server.Close()
	if _, err := linodeAPIRequestWithClient(context.Background(), method, path, payload, "test-token", linodeTestClient(server)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := linodeInstanceActionRequest("snapshot", "123", "", true); err == nil {
		t.Fatal("snapshot without a required provider label was accepted")
	}
	if _, _, _, err := linodeInstanceActionRequest("snapshot", "123", "backup", false); err == nil {
		t.Fatal("snapshot without explicit replacement confirmation was accepted")
	}
}

func TestLinodeInstanceActionsWaitForRemoteConvergence(t *testing.T) {
	t.Run("start waits through transitional state", func(t *testing.T) {
		statuses := []string{"offline", "booting", "running"}
		calls, boots := 0, 0
		request := func(_ context.Context, method, path string, _ any) (any, error) {
			if method == http.MethodPost {
				if path != "/linode/instances/123/boot" {
					t.Fatalf("mutation path = %q", path)
				}
				boots++
				return nil, nil
			}
			status := statuses[min(calls, len(statuses)-1)]
			calls++
			return map[string]any{"status": status}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executeLinodeInstanceAction(ctx, "start", "123", "", false, request, 0); err != nil {
			t.Fatal(err)
		}
		if boots != 1 || calls != 3 {
			t.Fatalf("boot dispatches=%d state reads=%d; want 1 and 3", boots, calls)
		}
	})
	t.Run("already running start is a no-op", func(t *testing.T) {
		posts := 0
		request := func(_ context.Context, method, _ string, _ any) (any, error) {
			if method == http.MethodPost {
				posts++
			}
			return map[string]any{"status": "running"}, nil
		}
		if err := executeLinodeInstanceAction(context.Background(), "start", "123", "", false, request, 0); err != nil {
			t.Fatal(err)
		}
		if posts != 0 {
			t.Fatalf("already-running start dispatched %d mutations", posts)
		}
	})
	t.Run("reboot requires observed transition", func(t *testing.T) {
		statuses := []string{"running", "rebooting", "running"}
		calls, posts := 0, 0
		request := func(_ context.Context, method, _ string, _ any) (any, error) {
			if method == http.MethodPost {
				posts++
				return nil, nil
			}
			status := statuses[min(calls, len(statuses)-1)]
			calls++
			return map[string]any{"status": status}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executeLinodeInstanceAction(ctx, "reboot", "123", "", false, request, 0); err != nil {
			t.Fatal(err)
		}
		if posts != 1 || calls != 3 {
			t.Fatalf("reboot dispatches=%d state reads=%d; want 1 and 3", posts, calls)
		}
	})
}

func TestOpenStackWaitRequiresDesiredState(t *testing.T) {
	statuses := []string{"BUILD", "ACTIVE"}
	reads := 0
	err := waitOpenStackServerState(context.Background(), "start", "server-id", 0, func(_ context.Context, id string) (string, error) {
		if id != "server-id" {
			t.Fatalf("server ID = %q", id)
		}
		status := statuses[min(reads, len(statuses)-1)]
		reads++
		return status, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("status reads = %d, want 2", reads)
	}
}

func TestLinodeDNSRecordPayloadUsesTypedMXAndSRVFields(t *testing.T) {
	mx := cloudDNSRequest{Type: "mx", Name: "@", Target: "mail.example.com", TTL: 2419200, Priority: 10}
	if !cloudDNSRecordValid(mx) {
		t.Fatal("valid Linode MX record rejected")
	}
	if got := linodeDNSRecordPayload(mx); got["target"] != "mail.example.com" || got["priority"] != 10 || got["ttl_sec"] != 2419200 {
		t.Fatalf("MX payload = %#v", got)
	}
	srv := cloudDNSRequest{Type: "SRV", Service: "https", Protocol: "tcp", Target: "service.example.com", TTL: 3600, Priority: 5, Weight: 20, Port: 443}
	if !cloudDNSRecordValid(srv) {
		t.Fatal("valid Linode SRV record rejected")
	}
	got := linodeDNSRecordPayload(srv)
	if got["target"] != "service.example.com" || got["service"] != "https" || got["protocol"] != "tcp" || got["priority"] != 5 || got["weight"] != 20 || got["port"] != 443 {
		t.Fatalf("SRV payload = %#v", got)
	}
	if _, exists := got["name"]; exists {
		t.Fatalf("SRV payload must use service/protocol, not name: %#v", got)
	}
}

func TestLinodeBackupsPathRequiresInstanceID(t *testing.T) {
	if got, err := linodeBackupsPath("123"); err != nil || got != "/linode/instances/123/backups" {
		t.Fatalf("backups path = %q, %v", got, err)
	}
	if _, err := linodeBackupsPath("../../account"); err == nil {
		t.Fatal("invalid Linode ID accepted")
	}
}

func TestCloudOperationKeysAreOptionalAndTimeBounded(t *testing.T) {
	request := durableCloudRequest{Operation: "instance", Action: "stop"}
	first := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if cloudOperationKey(request, "", first) != "" {
		t.Fatal("missing client idempotency key unexpectedly generated a persistent identity")
	}
	key := cloudOperationKey(request, "request-123", first)
	if got := cloudOperationKey(request, "request-123", first.Add(time.Minute)); got != key {
		t.Fatalf("same-day retry key changed: %q != %q", got, key)
	}
	if got := cloudOperationKey(request, "request-123", first.Add(12*time.Hour)); got == key {
		t.Fatal("client key was not expired at the bounded UTC-day boundary")
	}
}

func TestLinodeNodePayloadCombinesAddressAndPort(t *testing.T) {
	for _, test := range []struct {
		address string
		port    int
		want    string
	}{
		{"192.0.2.10", 8080, "192.0.2.10:8080"},
		{"2001:db8::1", 443, "[2001:db8::1]:443"},
	} {
		payload, err := linodeNodePayload(cloudLBRequest{Address: test.address, Port: test.port, Label: "backend-1", Weight: 255})
		if err != nil {
			t.Fatalf("payload for %s: %v", test.address, err)
		}
		if payload["address"] != test.want || payload["label"] != "backend-1" || payload["weight"] != 255 {
			t.Errorf("node payload = %#v, want address %q, label, and weight", payload, test.want)
		}
		if _, exists := payload["port"]; exists {
			t.Errorf("node payload has unsupported separate port field: %#v", payload)
		}
	}
	if _, err := linodeNodePayload(cloudLBRequest{Address: "192.0.2.10", Port: 80, Label: "x", Weight: 1}); err == nil {
		t.Fatal("invalid short node label accepted")
	}
}

func TestCloudCommandEnvKeepsRcloneConfigButFiltersPanelSecrets(t *testing.T) {
	t.Setenv("RCLONE_CONFIG", "/run/secrets/rclone.conf")
	t.Setenv("STEPANEL_RCLONE_CONFIG", "/wrong/path")
	t.Setenv("STEPANEL_SESSION_SECRET", "must-not-leak")

	env := cloudCommandEnv()
	var hasRcloneConfig, hasPanelSecrets bool
	for _, item := range env {
		if item == "RCLONE_CONFIG=/run/secrets/rclone.conf" {
			hasRcloneConfig = true
		}
		if strings.HasPrefix(item, "STEPANEL_") || strings.Contains(item, "must-not-leak") {
			hasPanelSecrets = true
		}
	}
	if !hasRcloneConfig {
		t.Fatal("cloud command environment omitted RCLONE_CONFIG")
	}
	if hasPanelSecrets {
		t.Fatal("cloud command environment leaked a STEPANEL variable or panel secret")
	}
}

func TestAWSActionArgsUseTypedResourceFlags(t *testing.T) {
	instanceCases := []struct {
		action string
		want   []string
	}{
		{"start", []string{"ec2", "start-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
		{"stop", []string{"ec2", "stop-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
		{"reboot", []string{"ec2", "reboot-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
	}
	for _, test := range instanceCases {
		got, err := awsInstanceActionArgs(test.action, AWSInstanceAction{InstanceID: "i-0123456789abcdef0"})
		if err != nil {
			t.Fatalf("%s args: %v", test.action, err)
		}
		if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
			t.Errorf("%s args = %v, want %v", test.action, got, test.want)
		}
	}
	got, err := awsVolumeSnapshotArgs(AWSVolumeSnapshot{VolumeID: "vol-0123456789abcdef0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ec2", "create-snapshot", "--volume-id", "vol-0123456789abcdef0", "--output", "json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("snapshot args = %v, want %v", got, want)
	}
}

func TestCloudDNSRecordValidation(t *testing.T) {
	valid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "192.0.2.10", TTL: 300}, {Type: "AAAA", Name: "www", Target: "2001:db8::1", TTL: 300}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300, Priority: 10}, {Type: "SRV", Service: "https", Protocol: "tcp", Target: "service.example.com", TTL: 300, Priority: 10, Weight: 5, Port: 443}}
	for _, record := range valid {
		if !cloudDNSRecordValid(record) {
			t.Errorf("valid record rejected: %#v", record)
		}
	}
	invalid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "example.com", TTL: 300}, {Type: "A", Name: "@", Target: "192.0.2.1", TTL: 10}, {Type: "MX", Name: "@", Target: "10 mail.example.com", TTL: 300}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300, Priority: 256}, {Type: "SRV", Service: "_https", Protocol: "tcp", Target: "service.example.com", TTL: 300, Port: 70000}, {Type: "TXT", Name: "@", Target: "safe\nvalue", TTL: 300}}
	for _, record := range invalid {
		if cloudDNSRecordValid(record) {
			t.Errorf("invalid record accepted: %#v", record)
		}
	}
}

func TestDNSRecordExists(t *testing.T) {
	value := map[string]any{"data": []any{map[string]any{"type": "A", "name": "www.example.com.", "target": "192.0.2.10", "ttl_sec": float64(300)}}}
	if !dnsRecordExists(value, cloudDNSRequest{Type: "a", Name: "www.example.com", Target: "192.0.2.10", TTL: 300}) {
		t.Fatal("identical DNS record was not detected")
	}
	if dnsRecordExists(value, cloudDNSRequest{Type: "a", Name: "www.example.com", Target: "192.0.2.10", TTL: 3600}) {
		t.Fatal("same DNS record with a different TTL was incorrectly treated as already configured")
	}
}
