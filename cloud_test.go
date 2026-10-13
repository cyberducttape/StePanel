package stepanel

import (
	"context"
	"encoding/json"
	"errors"
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
	t.Run("reboot waits for its correlated provider event", func(t *testing.T) {
		calls, posts, stateReads, eventReads := 0, 0, 0, 0
		request := func(_ context.Context, method, path string, _ any) (any, error) {
			if method == http.MethodPost {
				if path != "/linode/instances/123/reboot" {
					t.Fatalf("reboot path = %q", path)
				}
				posts++
				return nil, nil
			}
			if path == "/linode/instances/123" {
				stateReads++
				return map[string]any{"status": "running"}, nil
			}
			if path != "/account/events" {
				t.Fatalf("reboot verification path = %q, want provider events", path)
			}
			calls++
			eventReads++
			if eventReads == 1 {
				return map[string]any{"data": []any{map[string]any{"id": float64(10), "action": "linode_reboot", "status": "finished", "entity": map[string]any{"id": float64(123), "type": "linode"}}}}, nil
			}
			if eventReads == 2 {
				return map[string]any{"data": []any{
					map[string]any{"id": float64(10), "action": "linode_reboot", "status": "finished", "entity": map[string]any{"id": float64(123), "type": "linode"}},
					map[string]any{"id": float64(11), "action": "linode_reboot", "status": "finished", "entity": map[string]any{"id": float64(999), "type": "linode"}},
					map[string]any{"id": float64(12), "action": "linode_reboot", "status": "started", "entity": map[string]any{"id": float64(123), "type": "linode"}},
				}}, nil
			}
			return map[string]any{"data": []any{map[string]any{
				"id": float64(12), "action": "linode_reboot", "status": "finished",
				"entity": map[string]any{"id": float64(123), "type": "linode"},
			}}}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executeLinodeInstanceAction(ctx, "reboot", "123", "", false, request, 0); err != nil {
			t.Fatal(err)
		}
		if posts != 1 || calls != 3 || stateReads != 1 || eventReads != 3 {
			t.Fatalf("reboot dispatches=%d event reads=%d initial reads=%d; want 1, 3, 1", posts, eventReads, stateReads)
		}
	})
}

func TestLinodeSnapshotWaitDoesNotAcceptReplacedSnapshotWithSameLabel(t *testing.T) {
	responses := []map[string]any{
		{"snapshot": map[string]any{"current": map[string]any{"id": float64(7), "label": "nightly", "status": "successful"}}},
		{"id": float64(8), "type": "snapshot", "label": "nightly", "status": "running"}, // POST returns the created backup ID
		{"snapshot": map[string]any{
			"current":     map[string]any{"id": float64(7), "label": "nightly", "status": "successful"},
			"in_progress": map[string]any{"id": float64(8), "label": "nightly", "status": "running"},
		}},
		{"snapshot": map[string]any{"current": map[string]any{"id": float64(8), "label": "nightly", "status": "successful"}}},
	}
	calls := 0
	request := func(_ context.Context, method, path string, _ any) (any, error) {
		if path != "/linode/instances/123/backups" {
			t.Fatalf("backup path = %q", path)
		}
		wantMethod := http.MethodGet
		if calls == 1 {
			wantMethod = http.MethodPost
		}
		if method != wantMethod {
			t.Fatalf("request %d method = %s, want %s", calls, method, wantMethod)
		}
		response := responses[calls]
		calls++
		return response, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executeLinodeInstanceAction(ctx, "snapshot", "123", "nightly", true, request, 0); err != nil {
		t.Fatal(err)
	}
	if calls != len(responses) {
		t.Fatalf("provider requests = %d, want %d", calls, len(responses))
	}
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

func TestCloudRebootWaitRequiresObservedTransition(t *testing.T) {
	statuses := []string{"ACTIVE", "REBOOT", "ACTIVE"}
	reads := 0
	err := waitCloudState(context.Background(), "ACTIVE", "ACTIVE", 0, func(context.Context) (string, error) {
		status := statuses[min(reads, len(statuses)-1)]
		reads++
		return status, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 3 {
		t.Fatalf("health reads = %d, want transition away and back", reads)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reads = 0
	err = waitCloudState(ctx, "ok\tok", "ok\tok", 0, func(context.Context) (string, error) {
		reads++
		if reads == 2 {
			cancel()
		}
		return "ok\tok", nil
	})
	if err == nil {
		t.Fatal("unchanged healthy state falsely proved reboot completion")
	}
}

func TestOpenStackSnapshotWaitsForProviderCompletion(t *testing.T) {
	args, err := openStackInstanceActionArgs("snapshot", "server-123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"server", "backup", "create", "--name", "stepanel-server-123", "--wait", "server-123", "-f", "json"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("OpenStack snapshot arguments = %v, want %v", args, want)
	}
	if _, err := openStackInstanceActionArgs("snapshot", "--malicious"); err == nil {
		t.Fatal("OpenStack snapshot accepted an invalid resource ID")
	}
}

func TestOpenStackSnapshotDoesNotEnterServerStateWaiter(t *testing.T) {
	called := false
	err := executeOpenStackInstanceAction(context.Background(), "snapshot", "server-123",
		func(_ context.Context, args ...string) error {
			called = true
			want := []string{"server", "backup", "create", "--name", "stepanel-server-123", "--wait", "server-123", "-f", "json"}
			if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("OpenStack snapshot command = %v, want %v", args, want)
			}
			return nil
		},
		func(context.Context, string) (string, error) {
			t.Fatal("snapshot must not poll server ACTIVE/SHUTOFF status")
			return "", nil
		}, 0)
	if err != nil || !called {
		t.Fatalf("OpenStack snapshot completion err=%v dispatched=%v", err, called)
	}
}

func TestLinodeDNSRecordPayloadUsesTypedMXAndSRVFields(t *testing.T) {
	toMap := func(payload any) map[string]any {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	mx := cloudDNSRequest{Type: "mx", Name: "@", Target: "mail.example.com", TTL: 2419200, Priority: 10}
	if !cloudDNSRecordValid(mx) {
		t.Fatal("valid Linode MX record rejected")
	}
	if got := toMap(linodeDNSRecordPayload(mx)); got["target"] != "mail.example.com" || got["priority"] != float64(10) || got["ttl_sec"] != float64(2419200) || got["name"] != "@" {
		t.Fatalf("MX payload = %#v", got)
	}
	srv := cloudDNSRequest{Type: "SRV", Service: "https", Protocol: "tcp", Target: "service.example.com", TTL: 3600, Priority: 5, Weight: 20, Port: 443}
	if !cloudDNSRecordValid(srv) {
		t.Fatal("valid Linode SRV record rejected")
	}
	got := toMap(linodeDNSRecordPayload(srv))
	if got["target"] != "service.example.com" || got["service"] != "https" || got["protocol"] != "tcp" || got["priority"] != float64(5) || got["weight"] != float64(20) || got["port"] != float64(443) {
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

func TestCloudOperationKeysAreStableAcrossUTCDateBoundary(t *testing.T) {
	request := durableCloudRequest{Operation: "instance", Action: "stop"}
	first := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if cloudOperationKey(request, "", first) != "" {
		t.Fatal("missing client idempotency key unexpectedly generated a persistent identity")
	}
	key := cloudOperationKey(request, "request-123", first)
	if got := cloudOperationKey(request, "request-123", first.Add(time.Minute)); got != key {
		t.Fatalf("same-day retry key changed: %q != %q", got, key)
	}
	if got := cloudOperationKey(request, "request-123", first.Add(12*time.Hour)); got != key {
		t.Fatalf("stable request key changed at UTC midnight: %q != %q", got, key)
	}
}

func TestLinodeInventoryFailsWhenEveryProviderQueryFails(t *testing.T) {
	failed := func(string) (any, error) { return nil, errors.New("provider unavailable") }
	if inventory, err := linodeInventoryWith(failed); err == nil || len(inventory.Warnings) != 4 {
		t.Fatalf("all-failed inventory = %#v, err=%v; expected warnings and an error", inventory, err)
	}
	partial := func(path string) (any, error) {
		if path == "/domains" {
			return map[string]any{"data": []any{}}, nil
		}
		return nil, errors.New("provider unavailable")
	}
	if inventory, err := linodeInventoryWith(partial); err != nil || len(inventory.Warnings) != 3 {
		t.Fatalf("partial inventory = %#v, err=%v; expected usable partial data and warnings", inventory, err)
	}
}

func TestLinodeDNSRecordIDValidationIsImmediateAndOptionalOnlyForCreate(t *testing.T) {
	for _, test := range []struct {
		id   string
		want bool
	}{{"", true}, {"123", true}, {"../123", false}, {"12x", false}} {
		if got := validOptionalLinodeRecordID(test.id); got != test.want {
			t.Errorf("record ID %q valid = %v, want %v", test.id, got, test.want)
		}
	}
}

func TestLinodeDNSRecordNameUsesProviderLengthLimit(t *testing.T) {
	valid := cloudDNSRequest{Type: "A", Name: strings.Repeat("a", 100), Target: "192.0.2.1", TTL: 300}
	if !cloudDNSRecordValid(valid) {
		t.Fatal("provider-maximum 100-character record name rejected")
	}
	valid.Name += "a"
	if cloudDNSRecordValid(valid) {
		t.Fatal("provider-invalid 101-character record name accepted")
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
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got["address"] != test.want || got["label"] != "backend-1" || got["weight"] != float64(255) {
			t.Errorf("node payload = %#v, want address %q, label, and weight", got, test.want)
		}
		if _, exists := got["port"]; exists {
			t.Errorf("node payload has unsupported separate port field: %#v", got)
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

func TestAWSSnapshotIDUsesTopLevelResponseAndWaitsForThatID(t *testing.T) {
	const snapshotID = "snap-0123456789abcdef0"
	gotID, err := awsSnapshotID([]byte(`{"VolumeId":"vol-0123456789abcdef0","State":"pending","SnapshotId":"` + snapshotID + `"}`))
	if err != nil || gotID != snapshotID {
		t.Fatalf("AWS SnapshotId = %q, %v; want %q", gotID, err, snapshotID)
	}
	if _, err := awsSnapshotID([]byte(`{"Snapshot":{"SnapshotId":"` + snapshotID + `"}}`)); err == nil {
		t.Fatal("nested SnapshotId response was accepted")
	}
	args, err := awsSnapshotWaitArgs(snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ec2", "wait", "snapshot-completed", "--snapshot-ids", snapshotID}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("snapshot waiter args = %v, want %v", args, want)
	}
}

func TestAWSVolumeSnapshotPersistsIDBeforeWaitingAndCanResume(t *testing.T) {
	const snapshotID = "snap-0123456789abcdef0"
	created, checkpointed, waited := 0, false, false
	request := AWSVolumeSnapshot{VolumeID: "vol-0123456789abcdef0"}
	id, err := executeAWSVolumeSnapshotWith(context.Background(), request,
		func(context.Context, AWSVolumeSnapshot) ([]byte, error) {
			created++
			return []byte(`{"SnapshotId":"` + snapshotID + `"}`), nil
		},
		func(_ context.Context, got string) error {
			if got != snapshotID || !checkpointed {
				t.Fatalf("wait got snapshot %q before checkpoint=%v", got, checkpointed)
			}
			waited = true
			return nil
		},
		func(got string) error {
			if got != snapshotID {
				t.Fatalf("checkpoint ID = %q", got)
			}
			checkpointed = true
			return nil
		})
	if err != nil || id != snapshotID || created != 1 || !checkpointed || !waited {
		t.Fatalf("snapshot outcome id=%q err=%v create=%d checkpoint=%v wait=%v", id, err, created, checkpointed, waited)
	}
	request.SnapshotID = snapshotID
	created = 0
	id, err = executeAWSVolumeSnapshotWith(context.Background(), request,
		func(context.Context, AWSVolumeSnapshot) ([]byte, error) {
			created++
			return nil, errors.New("must not create a second snapshot")
		},
		func(_ context.Context, got string) error {
			if got != snapshotID {
				t.Fatalf("resume waited for %q, want %q", got, snapshotID)
			}
			return nil
		}, nil)
	if err != nil || id != snapshotID || created != 0 {
		t.Fatalf("resume outcome id=%q err=%v create=%d", id, err, created)
	}
}

func TestAWSSnapshotCheckpointIsDurableJobPayload(t *testing.T) {
	app, _ := newDNSTestApp(t)
	request := durableCloudRequest{
		Operation: "instance", Provider: "aws", Action: "snapshot",
		ID: "vol-0123456789abcdef0", Actor: "admin",
		AWSVolume: &AWSVolumeSnapshot{VolumeID: "vol-0123456789abcdef0"},
	}
	job, err := app.enqueueCloudJob(request, "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := app.Jobs.Claim(job.ID, "snapshot-worker")
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	const snapshotID = "snap-0123456789abcdef0"
	if err := persistAWSSnapshotID(app.Jobs, claimed, &request, snapshotID); err != nil {
		t.Fatal(err)
	}
	stored, found, err := app.Jobs.GetWithError(job.ID)
	if err != nil || !found {
		t.Fatalf("reload checkpointed job: found=%v err=%v", found, err)
	}
	var restored durableCloudRequest
	if err := json.Unmarshal(stored.Payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.AWSVolume == nil || restored.AWSVolume.SnapshotID != snapshotID {
		t.Fatalf("durable AWS snapshot checkpoint = %#v", restored.AWSVolume)
	}
}

func TestCloudDNSRecordValidation(t *testing.T) {
	valid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "192.0.2.10", TTL: 300}, {Type: "AAAA", Name: "www", Target: "2001:db8::1", TTL: 300}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300, Priority: 10}, {Type: "SRV", Service: "https", Protocol: "tcp", Target: "service.example.com", TTL: 300, Priority: 10, Weight: 5, Port: 443}}
	for _, record := range valid {
		if !cloudDNSRecordValid(record) {
			t.Errorf("valid record rejected: %#v", record)
		}
	}
	invalid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "example.com", TTL: 300}, {Type: "A", Name: "@", Target: "192.0.2.1", TTL: 10}, {Type: "A", Name: "@", Target: "2001:db8::1", TTL: 300}, {Type: "A", Name: "@", Target: "::ffff:192.0.2.1", TTL: 300}, {Type: "AAAA", Name: "@", Target: "192.0.2.10", TTL: 300}, {Type: "AAAA", Name: "@", Target: "::ffff:192.0.2.1", TTL: 300}, {Type: "MX", Name: "@", Target: "10 mail.example.com", TTL: 300}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300, Priority: 256}, {Type: "SRV", Service: "_https", Protocol: "tcp", Target: "service.example.com", TTL: 300, Port: 70000}, {Type: "TXT", Name: "@", Target: "safe\nvalue", TTL: 300}}
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
	srv := map[string]any{"data": []any{map[string]any{
		"type": "SRV", "name": "", "service": "_https.", "protocol": "_tcp",
		"target": "service.example.com.", "ttl_sec": float64(3600),
		"priority": float64(5), "weight": float64(20), "port": float64(443),
	}}}
	desired := cloudDNSRequest{Type: "SRV", Service: "https", Protocol: "tcp", Target: "service.example.com", TTL: 3600, Priority: 5, Weight: 20, Port: 443}
	if !dnsRecordExists(srv, desired) {
		t.Fatal("provider-canonicalized SRV record was not recognized")
	}
	desired.Priority++
	if dnsRecordExists(srv, desired) {
		t.Fatal("SRV record with a different priority was incorrectly treated as configured")
	}
}
