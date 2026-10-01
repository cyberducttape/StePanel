package http

import (
	"net/http"
	"net/http/httptest"
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
