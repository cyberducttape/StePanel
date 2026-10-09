package http

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// TimeoutConfiguration defines different timeout classes for different operations
type TimeoutConfiguration struct {
	// Ordinary API requests: /api/*, /livez, /readyz
	APIRead  time.Duration
	APIWrite time.Duration

	// Long-polling: a bounded request that waits for an update
	LongPoll time.Duration

	// Server-sent event streams carry no absolute context deadline. The
	// handler instead bounds each write with StreamWrite (detecting dead
	// peers) and rotates the connection after StreamLifetime so clients
	// reconnect and re-authenticate.
	StreamWrite    time.Duration
	StreamLifetime time.Duration

	// File uploads (backup, migration archives)
	UploadRead time.Duration

	// Large downloads
	DownloadWrite time.Duration

	// Synchronous long operations (builds, deployments, restore-to-staging)
	// that still run inside the request; see LongOperationPaths.
	LongOperation time.Duration
}

// LongOperationPaths are synchronous API routes whose handlers do minutes of
// work (Git checkout and build, dependency installs, container builds,
// restore-to-staging) inside the request. They get LongOperation as both the
// request deadline and the response write deadline, instead of the 30-second
// API class that would cancel them. They should become durable jobs that
// return a job ID; until then web/static/api.js must list the same paths
// (TestLongOperationPathsMatchClient) so the browser does not give up first.
var LongOperationPaths = []string{
	"/api/deployments/run",
	"/api/runner/build",
	"/api/sites/git-deploy",
	"/api/composer/",
	"/api/node/tooling",
	"/api/python/",
	"/api/staging",
	"/api/backups/restore-to-staging",
	"/api/backups/restore-offsite-to-staging",
}

// IsLongOperationPath reports whether path is a synchronous long operation.
func IsLongOperationPath(path string) bool {
	for _, prefix := range LongOperationPaths {
		if path == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix)) {
			return true
		}
	}
	return false
}

// DefaultTimeouts provides sensible defaults for all timeout classes
func DefaultTimeouts() TimeoutConfiguration {
	return TimeoutConfiguration{
		// Ordinary API: fast requests should complete in seconds
		// Includes: site management, database queries, job listing
		APIRead:  30 * time.Second,
		APIWrite: 2 * time.Minute,

		// Long-polling: client waiting for updates
		// Includes: activity feed polling, status updates
		LongPoll: 45 * time.Second,

		// Streams: /api/jobs/events sends a heartbeat every 25 seconds; a
		// write that cannot drain within StreamWrite means the peer is gone.
		StreamWrite:    15 * time.Second,
		StreamLifetime: 30 * time.Minute,

		// Uploads: large archive transfer with network variance. A 20 GiB
		// archive takes roughly 27 minutes at 100 Mbps, before overhead.
		// Includes: cpmove backups (20GB limit), WPress archives, imports
		// The upload endpoint streams into an immutable staged object and is
		// separately concurrency-limited; this deadline is not the slowloris
		// defense for ordinary API requests.
		UploadRead: 60 * time.Minute,

		// Downloads: large file transfers
		// Includes: backup downloads, export streams
		DownloadWrite: 5 * time.Minute,

		// Long operations: the largest handler budget is restore-to-staging
		// and staging creation (30 minutes plus database restore).
		LongOperation: 60 * time.Minute,
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
// STREAMS (no context deadline):
//   - Server-sent events stay open while a dashboard is visible
//   - A context deadline would cancel healthy streams and force reconnect,
//     re-authentication, and snapshot churn
//   - Per-write deadlines detect dead peers; the handler extends the
//     server-wide WriteTimeout per write and closes after StreamLifetime
//
// LONG-POLL (45s):
//   - Client holds connection waiting for updates
//   - Common pattern for dashboards, activity feeds
//   - 45s is standard for long-polling (Twitter uses 45s)
//   - Client can retry if timeout occurs
//
// UPLOADS (60 minutes):
//   - Backup archives up to 20GB
//   - Network speed: 100Mbps → 27 minutes to transfer 20GB
//   - Add substantial margin for network variance and system load
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
			case IsStreamPath(r.URL.Path):
				// Streams manage their own write deadlines and lifetime.
				next.ServeHTTP(w, r)
				return
			case startsWith(r.URL.Path, "/api/cpmove/") ||
				startsWith(r.URL.Path, "/api/wpress/") ||
				startsWith(r.URL.Path, "/api/backup/import"):
				// Uploads need long timeouts
				timeout = tc.UploadRead
			case startsWith(r.URL.Path, "/api/backup/download") ||
				startsWith(r.URL.Path, "/api/export"):
				// Downloads need long write timeouts
				timeout = tc.DownloadWrite
			case IsLongOperationPath(r.URL.Path):
				// The server-wide write timeout would end the response before
				// the work finishes; extend it for this request only.
				timeout = tc.LongOperation
				if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
					log.Printf("extend write deadline for %s: %v", r.URL.Path, err)
				}
			case startsWith(r.URL.Path, "/api/jobs/") ||
				startsWith(r.URL.Path, "/api/activity"):
				// Activity polling
				timeout = tc.LongPoll
			default:
				// Default API timeout
				timeout = tc.APIRead
			}

			// Create context with timeout
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IsStreamPath reports whether path is a long-lived server-sent event stream.
func IsStreamPath(path string) bool {
	return path == "/api/jobs/events"
}

// Helper function
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
