package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamLimiterEnforcesPerPrincipalAndTotalLimits(t *testing.T) {
	var limiter streamLimiter
	releaseA1, ok := limiter.acquire("alice", 2, 3)
	if !ok {
		t.Fatal("first stream rejected")
	}
	if _, ok := limiter.acquire("alice", 2, 3); !ok {
		t.Fatal("second stream within the per-principal limit rejected")
	}
	if _, ok := limiter.acquire("alice", 2, 3); ok {
		t.Fatal("third stream for one principal admitted past the per-principal limit")
	}
	if _, ok := limiter.acquire("bob", 2, 3); !ok {
		t.Fatal("another principal rejected below the total limit")
	}
	if _, ok := limiter.acquire("carol", 2, 3); ok {
		t.Fatal("stream admitted past the total limit")
	}
	releaseA1()
	releaseA1() // idempotent: must not free a second slot
	if status := limiter.status(); status["active"] != 2 {
		t.Fatalf("active after release = %d, want 2", status["active"])
	}
	if _, ok := limiter.acquire("carol", 2, 3); !ok {
		t.Fatal("released slot was not reusable")
	}
	if _, ok := limiter.acquire("dave", 2, 3); ok {
		t.Fatal("double release freed an extra slot")
	}
}

func TestJobEventsRejectsStreamsBeyondLimit(t *testing.T) {
	app := &App{Config: uploadTestConfig(t), Auth: Auth{}, Jobs: NewJobs(), Metrics: NewMetrics()}
	for range maxEventStreamsPerPrincipal {
		if _, ok := app.streams.acquire("", maxEventStreamsPerPrincipal, maxEventStreamsTotal); !ok {
			t.Fatal("could not pre-fill the stream limiter")
		}
	}
	response := httptest.NewRecorder()
	app.jobEvents(response, httptest.NewRequest(http.MethodGet, "/api/jobs/events", nil))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d Retry-After = %q, want 429 with Retry-After", response.Code, response.Header().Get("Retry-After"))
	}
	var metrics bytes.Buffer
	app.Metrics.Write(&metrics)
	if !strings.Contains(metrics.String(), "stepanel_event_streams_rejected_total 1") {
		t.Fatal("rejected stream was not counted")
	}
}

// TestJobEventsEndsStreamAfterLogout guards credential revocation: a stream
// admitted with a session must close at the next heartbeat after logout.
func TestJobEventsEndsStreamAfterLogout(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	previous := jobEventsHeartbeat
	jobEventsHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { jobEventsHeartbeat = previous })
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.ConfigureSessionStore(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	cookies := loginResponse.Result().Cookies()

	app := &App{Config: uploadTestConfig(t), Auth: auth, Jobs: NewJobs(), Metrics: NewMetrics()}
	server := httptest.NewServer(auth.Require(http.HandlerFunc(app.jobEvents)))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/jobs/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewReader(response.Body)
	if line, err := lines.ReadString('\n'); err != nil || line != "event: snapshot\n" {
		t.Fatalf("first line = %q, err = %v", line, err)
	}

	logout := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader("csrf="+cookies[1].Value))
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		logout.AddCookie(cookie)
	}
	auth.Logout(httptest.NewRecorder(), logout)

	closed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, lines)
		closed <- err
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("stream stayed open after logout")
	}
	if status := app.streams.status(); status["active"] != 0 {
		t.Fatalf("stream slot not released: %v", status)
	}
}

func TestStillAuthenticatedWithAuthDisabled(t *testing.T) {
	if !(Auth{}).StillAuthenticated(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("disabled auth must keep streams open")
	}
}

func TestStillAuthenticatedRejectsTokenRequestWithoutBearer(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(withAPIUser("admin"))
	if (Auth{Enabled: true}).StillAuthenticated(request) {
		t.Fatal("token-admitted request without a bearer header stayed authenticated")
	}
}

func TestMetricsExposeUploadAndStreamSeries(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveUploadStaged(1024)
	metrics.ObserveUploadRejected(uploadRejectCapacity)
	metrics.ObserveUploadRejected(-1)
	metrics.ObserveUploadRejected(len(uploadRejectReasons))
	metrics.StreamOpened()
	var out bytes.Buffer
	metrics.Write(&out)
	for _, want := range []string{
		"stepanel_uploads_staged_total 1",
		"stepanel_upload_bytes_total 1024",
		`stepanel_upload_rejections_total{reason="capacity"} 1`,
		`stepanel_upload_rejections_total{reason="stalled"} 0`,
		"stepanel_event_streams_active 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// TestLoggingKeepsAcceptedStreamsOutOfLatencyHistogram ensures a 30-minute
// stream does not count as one 30-minute request.
func TestLoggingKeepsAcceptedStreamsOutOfLatencyHistogram(t *testing.T) {
	metrics := NewMetrics()
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), metrics, false)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/jobs/events", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	var out bytes.Buffer
	metrics.Write(&out)
	if !strings.Contains(out.String(), "stepanel_http_requests_total 1\n") {
		t.Fatalf("stream was observed as an HTTP request:\n%s", out.String())
	}
}
