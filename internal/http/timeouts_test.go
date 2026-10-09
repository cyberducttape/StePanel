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

func TestLongOperationPathsGetLongDeadline(t *testing.T) {
	tc := DefaultTimeouts()
	for path, long := range map[string]bool{
		"/api/deployments/run": true, "/api/runner/build": true, "/api/sites/git-deploy": true,
		"/api/composer/shop": true, "/api/node/tooling": true, "/api/python/deploy": true, "/api/staging": true,
		"/api/backups/restore-to-staging": true, "/api/backups/restore-offsite-to-staging": true,
		"/api/deployments": false, "/api/sites/deploy": false, "/api/stagingx": false, "/api/backups": false,
	} {
		if got := IsLongOperationPath(path); got != long {
			t.Errorf("IsLongOperationPath(%q) = %v, want %v", path, got, long)
		}
		var deadline time.Time
		handler := tc.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, _ = r.Context().Deadline()
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		remaining := time.Until(deadline)
		if long && remaining < 59*time.Minute {
			t.Errorf("%s deadline %s, want the 60-minute long-operation class", path, remaining)
		}
		if !long && remaining > tc.LongPoll {
			t.Errorf("%s deadline %s, want an ordinary API deadline", path, remaining)
		}
	}
}

// The browser must not abort a long operation before the server's deadline.
func TestLongOperationPathsMatchClient(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "api.js"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const LONG_OPERATION_PATHS = \[(.*?)\];`).FindSubmatch(source)
	if block == nil {
		t.Fatal("web/static/api.js does not define LONG_OPERATION_PATHS")
	}
	var client []string
	for _, match := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1) {
		client = append(client, string(match[1]))
	}
	if strings.Join(client, ",") != strings.Join(LongOperationPaths, ",") {
		t.Fatalf("client long-operation paths %q differ from server %q", client, LongOperationPaths)
	}
	timeout := regexp.MustCompile(`LONG_OPERATION_TIMEOUT_MS = (\d+) \* 60 \* 1000`).FindSubmatch(source)
	if timeout == nil {
		t.Fatal("web/static/api.js does not define LONG_OPERATION_TIMEOUT_MS in minutes")
	}
	if minutes, _ := strconv.Atoi(string(timeout[1])); time.Duration(minutes)*time.Minute <= DefaultTimeouts().LongOperation {
		t.Fatalf("client long-operation timeout %d minutes must exceed the server's %s", minutes, DefaultTimeouts().LongOperation)
	}
}
