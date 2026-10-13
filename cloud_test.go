package stepanel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
		_, _ = fmt.Fprintf(response, `{"page":%d,"pages":3,"results":1,"total_results":3,"data":[{"id":%d}]}`, id, id)
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
			_, _ = response.Write([]byte(`{"page":1,"pages":1001,"total_results":1001,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "refusing incomplete inventory") {
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
			_, _ = response.Write([]byte(`{"page":1,"pages":2,"results":1,"total_results":2,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "empty response for page 2") {
			t.Fatalf("request error = %v, want explicit missing-page failure", err)
		}
	})

	t.Run("advertised total mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"page":1,"pages":1,"results":1,"total_results":2,"data":[{"id":1}]}`))
		}))
		defer server.Close()
		_, err := linodeAPIRequestWithClient(context.Background(), http.MethodGet, "/domains/123/records", nil, "test-token", linodeTestClient(server))
		if err == nil || !strings.Contains(err.Error(), "advertised 2") {
			t.Fatalf("request error = %v, want total-results mismatch", err)
		}
	})
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
	valid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "192.0.2.10", TTL: 300}, {Type: "AAAA", Name: "www", Target: "2001:db8::1", TTL: 300}, {Type: "MX", Name: "@", Target: "10 mail.example.com", TTL: 300}, {Type: "SRV", Name: "_https._tcp", Target: "10 5 443 service.example.com", TTL: 300}}
	for _, record := range valid {
		if !cloudDNSRecordValid(record) {
			t.Errorf("valid record rejected: %#v", record)
		}
	}
	invalid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "example.com", TTL: 300}, {Type: "A", Name: "@", Target: "192.0.2.1", TTL: 10}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300}, {Type: "MX", Name: "@", Target: "70000 mail.example.com", TTL: 300}, {Type: "SRV", Name: "_https._tcp", Target: "10 5 70000 service.example.com", TTL: 300}, {Type: "TXT", Name: "@", Target: "safe\nvalue", TTL: 300}}
	for _, record := range invalid {
		if cloudDNSRecordValid(record) {
			t.Errorf("invalid record accepted: %#v", record)
		}
	}
}

func TestDNSRecordExists(t *testing.T) {
	value := map[string]any{"data": []any{map[string]any{"type": "A", "name": "www.example.com.", "target": "192.0.2.10"}}}
	if !dnsRecordExists(value, cloudDNSRequest{Type: "a", Name: "www.example.com", Target: "192.0.2.10"}) {
		t.Fatal("identical DNS record was not detected")
	}
}
