package stepanel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	httputil "github.com/cyberducttape/StePanel/internal/http"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthAndMetricsEndpoints(t *testing.T) {
	app := &App{Config: Config{}, Auth: Auth{}, Jobs: NewJobs(), Metrics: NewMetrics()}
	app.Metrics.RestoreStarted()
	app.Metrics.RestoreFinished(nil)

	server := http.NewServeMux()
	server.HandleFunc("/api/health", app.health)
	server.HandleFunc("/metrics", app.metrics)

	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"ok":true`) {
		t.Fatalf("unexpected health response: %d %s", health.Code, health.Body.String())
	}
	metrics := httptest.NewRecorder()
	server.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "stepanel_restore_jobs_completed_total 1") {
		t.Fatalf("unexpected metrics response: %d %s", metrics.Code, metrics.Body.String())
	}
}

func TestEmbeddedDashboardTemplate(t *testing.T) {
	data, err := webAssets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	view, err := template.New("index.html").Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse embedded dashboard: %v", err)
	}
	var rendered bytes.Buffer
	if err := view.Execute(&rendered, map[string]any{"Title": "StePanel", "AssetVersion": "test-assets", "Now": time.Now(), "Servers": []ServiceSummary{{Name: "apache2", Status: "active"}}, "Healthy": 1, "Alerts": 0, "Security": []SecurityCheck{}, "Jobs": []Job{}, "Capabilities": map[string]bool{}, "IsAdministrator": true, "Database": DatabaseAdmin{Engine: "mysql", Version: "default", Host: "local socket", Service: "mysql", Status: "missing", Client: "mariadb", AdminProduct: "phpMyAdmin", AdminURL: "/phpmyadmin"}}); err != nil {
		t.Fatalf("render embedded dashboard: %v", err)
	}
	if strings.Contains(rendered.String(), "Stephan") || !strings.Contains(rendered.String(), "Manage your sites with confidence") {
		t.Fatal("dashboard contains simulated content or is missing its customer workspace")
	}
	if !strings.Contains(rendered.String(), "/static/workspace.css?v=test-assets") {
		t.Fatal("dashboard does not load the workspace design layer")
	}
	if _, err := webAssets.ReadFile("web/static/workspace.css"); err != nil {
		t.Fatalf("workspace design asset is not embedded: %v", err)
	}
	if !strings.Contains(rendered.String(), "appearanceDialog") || !strings.Contains(rendered.String(), "/static/theme.js?v=test-assets") {
		t.Fatal("dashboard is missing the persisted appearance settings surface")
	}
	themeScript, err := webAssets.ReadFile("web/static/theme.js")
	if err != nil {
		t.Fatalf("appearance theme asset is not embedded: %v", err)
	}
	for _, theme := range []string{"classic-green", "classic-amber", "retro-neon", "commodore64", "windows95", "windows31"} {
		if !strings.Contains(string(themeScript), theme) {
			t.Fatalf("appearance theme asset does not expose %q", theme)
		}
	}
}

func TestEmbeddedAssetVersion(t *testing.T) {
	version, err := embeddedAssetVersion()
	if err != nil {
		t.Fatal(err)
	}
	if len(version) != 16 {
		t.Fatalf("asset version = %q, want a 16-character fingerprint", version)
	}
}

func TestNodeDeploymentBrowserPayloadMatchesStrictAPI(t *testing.T) {
	data, err := webAssets.ReadFile("web/static/deploy.js")
	if err != nil {
		t.Fatal(err)
	}
	// Compare without whitespace so the check follows the flow, not the
	// formatting of the source.
	script := strings.Join(strings.Fields(string(data)), "")
	mutations := strings.Count(script, "api.post(") + strings.Count(script, "api.put(") + strings.Count(script, "api.patch(") + strings.Count(script, "api.delete(") + strings.Count(script, "api.request(")
	if !strings.Contains(script, "data.node_version=data.version") || !strings.Contains(script, "api.post('/api/deployments',data)") || mutations != 1 {
		t.Fatal("Node deployment browser flow is not a single durable deployment request")
	}
	for _, legacy := range []string{"/api/node/select", "/api/apps/deploy", "/api/proxy/deploy"} {
		if strings.Contains(script, legacy) {
			t.Fatalf("Node deployment browser flow still calls %s", legacy)
		}
	}
}

func TestLoggingAddsBrowserSecurityHeaders(t *testing.T) {
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, false)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID header is missing")
	}
}

func TestAllowMethodsRejectsUnexpectedMethod(t *testing.T) {
	called := false
	handler := allowMethods(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), http.MethodGet)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/health", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet || called {
		t.Fatalf("status = %d, Allow = %q, called = %v", response.Code, response.Header().Get("Allow"), called)
	}
}

func TestNormalizeAPIErrors(t *testing.T) {
	handler := normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid request", http.StatusBadRequest)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/example", nil))
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" || !strings.Contains(body, `"error":"invalid request"`) || !strings.Contains(body, `"code":400`) || !strings.Contains(body, `"error_code":"invalid_request"`) || !strings.Contains(body, `"retryable":false`) || !strings.Contains(body, `"resource":"/api/example"`) {
		t.Fatalf("unexpected normalized error: %d %s", response.Code, body)
	}
}

func TestNormalizeAPIErrorsDoesNotReflectHTMLTypedBody(t *testing.T) {
	handler := normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		http.Error(w, r.URL.Query().Get("message"), http.StatusBadRequest)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/example?message=%3Cscript%3Ealert(1)%3C%2Fscript%3E", nil))
	if response.Header().Get("Content-Type") != "application/json" || strings.Contains(response.Body.String(), "<script>") {
		t.Fatalf("HTML-typed API error was reflected: content-type=%q body=%s", response.Header().Get("Content-Type"), response.Body.String())
	}
}

// TestNormalizeAPIErrorsTypesUntypedSuccessBody guards against browsers
// sniffing an untyped API body as HTML. A real server is used because
// headers are snapshotted when WriteHeader is forwarded.
func TestNormalizeAPIErrorsTypesUntypedSuccessBody(t *testing.T) {
	server := httptest.NewServer(normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("explicit") != "" {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	})))
	defer server.Close()
	for _, query := range []string{"", "?explicit=1"} {
		response, err := http.Get(server.URL + "/api/example" + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if got := response.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("untyped API body%s content-type = %q, want application/octet-stream", query, got)
		}
	}
}

func TestNormalizeAPIErrorsHidesServerErrorDetail(t *testing.T) {
	handler := normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "open /var/lib/stepanel/secret.db: permission denied", http.StatusInternalServerError)
	}))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/example", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey{}, "req-123"))
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusInternalServerError || strings.Contains(body, "/var/lib") || !strings.Contains(body, `"request_id":"req-123"`) {
		t.Fatalf("server error envelope leaked detail or lost request ID: %d %s", response.Code, body)
	}
}

func TestNormalizeAPIErrorsKeepsExplicitSafeServerMessage(t *testing.T) {
	handler := normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, r, http.StatusServiceUnavailable, "deployment history is unavailable; no changes were made")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/example", nil))
	if !strings.Contains(response.Body.String(), "no changes were made") {
		t.Fatalf("explicit safe message was rewritten: %s", response.Body.String())
	}
}

// TestAPIMiddlewareStreamsAndFlushes guards server-sent events and large
// downloads: successful API responses must reach the client unbuffered.
func TestAPIMiddlewareStreamsAndFlushes(t *testing.T) {
	flushed := make(chan struct{})
	release := make(chan struct{})
	handler := logging(normalizeAPIErrors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "event streaming is unavailable", http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
		flusher.Flush()
		close(flushed)
		<-release
	})), nil, false)
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/jobs/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", response.StatusCode)
	}
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never flushed")
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	close(release)
	if err != nil || line != "event: ping\n" {
		t.Fatalf("first streamed line = %q, err = %v", line, err)
	}
}

func TestDecodeJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	for _, body := range []string{`{"name":"ok","unknown":true}`, `{"name":"ok"}{"name":"again"}`} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		response := httptest.NewRecorder()
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(response, request, 1024, &input); err == nil {
			t.Fatalf("decodeJSON accepted %q", body)
		}
	}
}

func TestAuthenticatedOperationalEndpoints(t *testing.T) {
	root := t.TempDir()
	app := &App{Config: Config{ImportRoot: root, WebRoot: root}, Auth: Auth{}, Jobs: NewJobs(), Metrics: NewMetrics()}
	server := http.NewServeMux()
	server.HandleFunc("/api/services", app.services)
	server.HandleFunc("/api/database", app.database)
	server.HandleFunc("/api/ftp", app.ftpStatus)
	server.HandleFunc("/api/security/audit", app.securityAudit)
	server.HandleFunc("/api/cloud", app.cloudInventory)

	services := httptest.NewRecorder()
	server.ServeHTTP(services, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if services.Code != http.StatusOK || !strings.Contains(services.Body.String(), `"services"`) || !strings.Contains(services.Body.String(), `"apache2"`) || !strings.Contains(services.Body.String(), `"mysql"`) {
		t.Fatalf("unexpected services response: %d %s", services.Code, services.Body.String())
	}
	database := httptest.NewRecorder()
	server.ServeHTTP(database, httptest.NewRequest(http.MethodGet, "/api/database", nil))
	if database.Code != http.StatusOK || !strings.Contains(database.Body.String(), `"engine":"mysql"`) || !strings.Contains(database.Body.String(), `"admin_product":"phpMyAdmin"`) {
		t.Fatalf("unexpected database response: %d %s", database.Code, database.Body.String())
	}
	ftp := httptest.NewRecorder()
	server.ServeHTTP(ftp, httptest.NewRequest(http.MethodGet, "/api/ftp", nil))
	if ftp.Code != http.StatusOK || !strings.Contains(ftp.Body.String(), `"local_user_chroot":true`) {
		t.Fatalf("unexpected FTP response: %d %s", ftp.Code, ftp.Body.String())
	}
	audit := httptest.NewRecorder()
	server.ServeHTTP(audit, httptest.NewRequest(http.MethodGet, "/api/security/audit", nil))
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), `"checks"`) {
		t.Fatalf("unexpected audit response: %d %s", audit.Code, audit.Body.String())
	}
	cloud := httptest.NewRecorder()
	server.ServeHTTP(cloud, httptest.NewRequest(http.MethodGet, "/api/cloud", nil))
	if cloud.Code != http.StatusOK || !strings.Contains(cloud.Body.String(), "no cloud provider configured") {
		t.Fatalf("unexpected cloud response: %d %s", cloud.Code, cloud.Body.String())
	}
}

func TestAuthRequireProtectsAPI(t *testing.T) {
	auth := Auth{Enabled: true, Username: "admin", Secret: "12345678901234567890123456789012"}
	handler := auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestJobsCompleteAndCleanup(t *testing.T) {
	jobs := NewJobs()
	done := make(chan struct{})
	if err := jobs.Submit("job-1", "site", func() (ImportResult, error) {
		close(done)
		return ImportResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-done
	var job Job
	for i := 0; i < 20; i++ {
		var ok bool
		job, ok = jobs.Get("job-1")
		if ok && job.State == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if job.State != "completed" {
		t.Fatalf("job state = %q, want completed", job.State)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobs.Get("missing"); ok {
		t.Fatal("missing job unexpectedly found")
	}
}

func TestJobsListNewestFirst(t *testing.T) {
	jobs := NewJobs()
	jobs.items["old"] = &Job{ID: "old", StartedAt: time.Unix(1, 0)}
	jobs.items["new"] = &Job{ID: "new", StartedAt: time.Unix(2, 0)}
	items := jobs.List(1)
	if len(items) != 1 || items[0].ID != "new" {
		t.Fatalf("jobs = %#v, want newest job", items)
	}
}

func TestJobsRejectConcurrentRestoresForSameSite(t *testing.T) {
	jobs := NewJobs()
	started := make(chan struct{})
	release := make(chan struct{})
	if err := jobs.Submit("job-1", "site", func() (ImportResult, error) {
		close(started)
		<-release
		return ImportResult{}, nil
	}); err != nil {
		t.Fatalf("first restore was rejected: %v", err)
	}
	<-started
	if err := jobs.SubmitWPress("job-2", "site", func() (WPressResult, error) {
		return WPressResult{}, nil
	}); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("concurrent restore error = %v, want ErrJobBusy", err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJobsEnforceConfiguredGlobalCapacity(t *testing.T) {
	jobs := newJobs("", 1)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := jobs.Submit("job-1", "site-one", func() (ImportResult, error) {
		close(started)
		<-release
		return ImportResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := jobs.Submit("job-2", "site-two", func() (ImportResult, error) { return ImportResult{}, nil }); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("capacity error = %v, want ErrJobBusy", err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

// A service action must outlive the server-wide write timeout: the timeout
// middleware extends the write deadline through the logging and API error
// wrappers. Without it the client sees a dropped connection.
func TestServiceOperationOutlivesServerWriteTimeout(t *testing.T) {
	timeouts := httputil.DefaultTimeouts()
	timeouts.ServiceOperation = 5 * time.Second
	handler := logging(normalizeAPIErrors(timeouts.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]string{"path": r.URL.Path})
	}))), nil, false)
	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = 300 * time.Millisecond
	server.Start()
	defer server.Close()
	for path, long := range map[string]bool{"/api/python/shop/restart": true, "/api/sites": false} {
		response, err := http.Post(server.URL+path, "application/json", strings.NewReader("{}"))
		if long {
			if err != nil {
				t.Fatalf("%s: service action was cut off by the server write timeout: %v", path, err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d", path, response.StatusCode)
			}
			continue
		}
		if err == nil {
			_ = response.Body.Close()
			t.Fatalf("%s: ordinary route outlived the 300ms write timeout; the test no longer proves the extension", path)
		}
	}
}

// A client that trickles an ordinary request body must not hold a handler
// past the route's class deadline: the context deadline alone does not
// interrupt a blocked body read, and the server read timeout is sized for
// uploads.
func TestSlowRequestBodyHitsClassReadDeadline(t *testing.T) {
	timeouts := httputil.DefaultTimeouts()
	timeouts.APIRead = 300 * time.Millisecond
	readDone := make(chan time.Duration, 1)
	handler := logging(normalizeAPIErrors(timeouts.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var input map[string]any
		if err := decodeJSON(w, r, 1024, &input); err == nil {
			t.Error("trickled body decoded successfully")
		}
		readDone <- time.Since(started)
	}))), nil, false)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = 10 * time.Second
	server.Start()
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST /api/sites HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 50\r\n\r\n{\"si")); err != nil {
		t.Fatal(err)
	}
	select {
	case elapsed := <-readDone:
		if elapsed > 2*time.Second {
			t.Fatalf("body read blocked %s, want the 300ms class deadline", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler still blocked reading a trickled body after 5s")
	}
}
