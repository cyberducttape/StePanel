package http

import (
	"io"
	"net"
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
		{name: "cpmove upload", path: "/api/cpmove/inspect", want: 60 * time.Minute, margin: 2 * time.Second},
		{name: "wpress upload", path: "/api/wpress/import", want: 60 * time.Minute, margin: 2 * time.Second},
		{name: "cpmove plan", path: "/api/cpmove/plan", want: 30 * time.Second, margin: 2 * time.Second},
		{name: "cpmove import confirmation", path: "/api/cpmove/import", want: 30 * time.Second, margin: 2 * time.Second},
		{name: "wpress preflight", path: "/api/wpress/preflight", want: 30 * time.Second, margin: 2 * time.Second},
		{name: "upload path prefix", path: "/api/cpmove/inspect/extra", want: 30 * time.Second, margin: 2 * time.Second},
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

func TestUploadReadForCoversLargestArchiveAt50Mbps(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		want  time.Duration
	}{
		{0, 60 * time.Minute},
		{20 << 30, 60 * time.Minute},  // ~57.3 min at 50 Mbps
		{40 << 30, 115 * time.Minute}, // ~114.5 min at 50 Mbps
		{1 << 50, 4 * time.Hour},
	} {
		if got := UploadReadFor(tc.bytes); got != tc.want {
			t.Errorf("UploadReadFor(%d) = %s, want %s", tc.bytes, got, tc.want)
		}
	}
}

// TestSmallCPMoveRequestsCannotTrickleBodies drives a real connection: a
// client that sends a small-route body slower than the API read deadline is
// cut off at that deadline instead of the multi-hour upload window, while an
// archive upload route keeps the long server read timeout.
func TestSmallCPMoveRequestsCannotTrickleBodies(t *testing.T) {
	tc := DefaultTimeouts()
	tc.APIRead = 300 * time.Millisecond
	tc.UploadRead = time.Hour
	type outcome struct {
		err     error
		elapsed time.Duration
	}
	results := make(chan outcome, 1)
	handler := tc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		_, err := io.Copy(io.Discard, r.Body)
		results <- outcome{err: err, elapsed: time.Since(started)}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = tc.UploadRead
	server.Start()
	defer server.Close()

	trickle := func(path string, total time.Duration) outcome {
		t.Helper()
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		const chunks = 10
		if _, err := io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(chunks)+"\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		go func() {
			for i := 0; i < chunks; i++ {
				time.Sleep(total / chunks)
				if _, err := conn.Write([]byte("x")); err != nil {
					return
				}
			}
		}()
		select {
		case result := <-results:
			return result
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: handler never returned", path)
			return outcome{}
		}
	}

	for _, path := range []string{"/api/cpmove/plan", "/api/cpmove/import"} {
		result := trickle(path, 2*time.Second)
		if result.err == nil {
			t.Fatalf("%s: a body trickled past the API read deadline was accepted", path)
		}
		if result.elapsed > time.Second {
			t.Fatalf("%s: trickled body held the handler for %s, want about %s", path, result.elapsed, tc.APIRead)
		}
	}
	if result := trickle("/api/cpmove/inspect", time.Second); result.err != nil {
		t.Fatalf("archive upload route was cut off by the API read deadline: %v", result.err)
	}
}
