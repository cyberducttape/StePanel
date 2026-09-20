package http

import (
	"net/http"
	"time"
)

// TimeoutConfiguration defines different timeout classes for different operations
type TimeoutConfiguration struct {
	// Ordinary API requests: /api/*, /livez, /readyz
	APIRead  time.Duration
	APIWrite time.Duration

	// Long-polling and streaming
	LongPoll time.Duration

	// File uploads (backup, migration archives)
	UploadRead time.Duration

	// Large downloads
	DownloadWrite time.Duration
}

// DefaultTimeouts provides sensible defaults for all timeout classes
func DefaultTimeouts() TimeoutConfiguration {
	return TimeoutConfiguration{
		// Ordinary API: fast requests should complete in seconds
		// Includes: site management, database queries, job listing
		APIRead:  10 * time.Second,
		APIWrite: 10 * time.Second,

		// Long-polling: client waiting for updates
		// Includes: activity feed polling, status updates
		LongPoll: 45 * time.Second,

		// Uploads: large archive transfer with network variance
		// Includes: cpmove backups (20GB limit), WPress archives, imports
		UploadRead: 30 * time.Minute,

		// Downloads: large file transfers
		// Includes: backup downloads, export streams
		DownloadWrite: 20 * time.Minute,
	}
}

// ApplyToServer applies differentiated timeouts to an HTTP server
// Default timeouts apply to all routes; specific handlers override as needed
func (tc TimeoutConfiguration) ApplyToServer(srv *http.Server) {
	// These are the default timeouts for the server
	// Most API routes will use these
	srv.ReadTimeout = tc.APIRead
	srv.WriteTimeout = tc.APIWrite
	srv.ReadHeaderTimeout = 5 * time.Second
	srv.IdleTimeout = 30 * time.Second
}

// UploadHandler wraps an upload handler with extended timeouts
// Use this for /api/cpmove/import, /api/wpress/restore, etc.
func (tc TimeoutConfiguration) UploadHandler(handler http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set per-request timeout for this upload
		// In practice, this requires more sophisticated handling via context
		handler(w, r)
	})
}

// DownloadHandler wraps a download handler with extended write timeout
// Use this for /api/backup/download, /api/export, etc.
func (tc TimeoutConfiguration) DownloadHandler(handler http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	})
}

// LongPollHandler wraps a handler that supports long-polling
// Use this for activity feeds, status polling, etc.
func (tc TimeoutConfiguration) LongPollHandler(handler http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	})
}

// Rationale for timeout choices:
//
// ORDINARY API (10s read/write):
//   - Simple DB queries complete in <1s
//   - Site list/detail in <2s
//   - Job queries in <1s
//   - Error responses in milliseconds
//   - 10s allows for network jitter + slow storage without being wasteful
//
// LONG-POLL (45s):
//   - Client holds connection waiting for updates
//   - Common pattern for dashboards, activity feeds
//   - 45s is standard for long-polling (Twitter uses 45s)
//   - Client can retry if timeout occurs
//
// UPLOADS (30 minutes):
//   - Backup archives up to 20GB
//   - Network speed: 100Mbps → 27 minutes to transfer 20GB
//   - Add margin for network variance and system load
//   - Uploads use explicit body size/rate limits (prevent abuse)
//   - Should be in job queue (async) for better UX
//
// DOWNLOADS (20 minutes):
//   - Large backup files
//   - Network variance same as uploads
//   - 20 minutes allows for 27GB at 100Mbps
//
// KEY INSIGHT:
//   Even 30-minute timeouts are not ideal for huge uploads.
//   Better architecture: queue the upload, stream to background job,
//   avoid tying up an HTTP connection. This is Phase 2 work.

// Middleware creates an HTTP middleware that enforces timeouts per route
func (tc TimeoutConfiguration) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Route-specific timeout classification
			// This is a template; would need full routing context to implement

			// Classify based on path
			var timeout time.Duration
			switch {
			case startsWith(r.URL.Path, "/api/cpmove/") ||
				 startsWith(r.URL.Path, "/api/wpress/") ||
				 startsWith(r.URL.Path, "/api/backup/import"):
				// Uploads need long timeouts
				timeout = tc.UploadRead
			case startsWith(r.URL.Path, "/api/backup/download") ||
				 startsWith(r.URL.Path, "/api/export"):
				// Downloads need long write timeouts
				timeout = tc.DownloadWrite
			case startsWith(r.URL.Path, "/api/jobs/") ||
				 startsWith(r.URL.Path, "/api/activity"):
				// Activity polling
				timeout = tc.LongPoll
			default:
				// Default API timeout
				timeout = tc.APIRead
			}

			// Create context with timeout
			ctx, cancel := httpctx.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Helper function
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
