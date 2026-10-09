package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTimeoutMiddlewareUsesRouteClass(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		want   time.Duration
		margin time.Duration
	}{
		{name: "upload", path: "/api/cpmove/import", want: 60 * time.Minute, margin: 2 * time.Second},
		{name: "download", path: "/api/backup/download/site", want: 5 * time.Minute, margin: 2 * time.Second},
		{name: "long poll", path: "/api/jobs/123", want: 45 * time.Second, margin: 2 * time.Second},
		{name: "ordinary api", path: "/api/sites", want: 30 * time.Second, margin: 2 * time.Second},
	}

	tc := DefaultTimeouts()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got time.Duration
			handler := tc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("request context has no deadline")
				}
				got = time.Until(deadline)
				w.WriteHeader(http.StatusNoContent)
			}))

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
			if got < test.want-test.margin || got > test.want+test.margin {
				t.Fatalf("context deadline was %s from now, want about %s", got, test.want)
			}
		})
	}
}

func TestTimeoutMiddlewareLeavesEventStreamWithoutDeadline(t *testing.T) {
	handler := DefaultTimeouts().Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deadline, ok := r.Context().Deadline(); ok {
			t.Fatalf("event stream context has deadline %s from now; it must manage its own write deadlines", time.Until(deadline))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/jobs/events", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestServiceOperationPathsGetServiceDeadline(t *testing.T) {
	tc := DefaultTimeouts()
	for path, long := range map[string]bool{
		"/api/python/shop/restart": true, "/api/python/shop/stop": true,
		// Builds and deployments are durable jobs: the request only queues them.
		"/api/deployments/run": false, "/api/runner/build": false, "/api/sites/git-deploy": false,
		"/api/composer/shop/install": false, "/api/node/tooling": false, "/api/staging": false,
		"/api/backups/restore-to-staging": false, "/api/sites/deploy": false, "/api/pythonx": false,
	} {
		if got := IsServiceOperationPath(path); got != long {
			t.Errorf("IsServiceOperationPath(%q) = %v, want %v", path, got, long)
		}
		var deadline time.Time
		handler := tc.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, _ = r.Context().Deadline()
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		remaining := time.Until(deadline)
		if long && remaining < tc.ServiceOperation-time.Minute {
			t.Errorf("%s deadline %s, want the service-operation class", path, remaining)
		}
		if !long && remaining > tc.LongPoll {
			t.Errorf("%s deadline %s, want an ordinary API deadline", path, remaining)
		}
	}
}

// The browser must not abort a service action before the server's deadline.
func TestServiceOperationPathsMatchClient(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "api.js"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const SERVICE_OPERATION_PATHS = \[(.*?)\];`).FindSubmatch(source)
	if block == nil {
		t.Fatal("web/static/api.js does not define SERVICE_OPERATION_PATHS")
	}
	var client []string
	for _, match := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1) {
		client = append(client, string(match[1]))
	}
	if strings.Join(client, ",") != strings.Join(ServiceOperationPaths, ",") {
		t.Fatalf("client service-operation paths %q differ from server %q", client, ServiceOperationPaths)
	}
	timeout := regexp.MustCompile(`SERVICE_OPERATION_TIMEOUT_MS = (\d+) \* 60 \* 1000`).FindSubmatch(source)
	if timeout == nil {
		t.Fatal("web/static/api.js does not define SERVICE_OPERATION_TIMEOUT_MS in minutes")
	}
	if minutes, _ := strconv.Atoi(string(timeout[1])); time.Duration(minutes)*time.Minute <= DefaultTimeouts().ServiceOperation {
		t.Fatalf("client service-operation timeout %d minutes must exceed the server's %s", minutes, DefaultTimeouts().ServiceOperation)
	}
}
