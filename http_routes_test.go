package stepanel

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestRegisterRoutesPreservesCoreRoutePatterns(t *testing.T) {
	app := &App{Auth: Auth{}, Metrics: NewMetrics()}
	mux := app.registerRoutes(fstest.MapFS{})
	tests := []struct {
		path    string
		pattern string
	}{
		{"/livez", "/livez"},
		{"/readyz", "/readyz"},
		{"/api/health/operational", "/api/health/operational"},
		{"/api/jobs", "/api/jobs"},
		{"/api/jobs/events", "/api/jobs/events"},
		{"/api/sites/files/example/path", "/api/sites/files/"},
		{"/metrics", "/metrics"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			_, pattern := mux.Handler(httptest.NewRequest("GET", test.path, nil))
			if pattern != test.pattern {
				t.Fatalf("route pattern for %s = %q, want %q", test.path, pattern, test.pattern)
			}
		})
	}
	if app.MetadataCache == nil {
		t.Fatal("route setup did not initialize metadata cache")
	}
}
