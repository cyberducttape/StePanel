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

	// Synchronous service lifecycle actions; see ServiceOperationPaths.
	ServiceOperation time.Duration
}

// ServiceOperationPaths are synchronous API routes that start, stop, or
// restart a site service inside the request, bounded by the helper's 60-second
// service lifecycle timeout plus the site lock wait. They get ServiceOperation
// as both the request and response write deadline instead of the 30-second
// API class. Work that takes minutes (builds, deployments, dependency
// installs, staging) is a durable job instead and answers in milliseconds.
// web/static/api.js lists the same paths (TestServiceOperationPathsMatchClient)
// so the browser does not give up first.
var ServiceOperationPaths = []string{
	"/api/python/",
}

// UploadPaths are the routes that receive a large archive body. They keep the
// server-wide read timeout (sized by UploadReadFor) and install their own
// idle-body guard in the handler. Every other route, including the small
// cPanel plan and import-confirmation forms, gets an ordinary API deadline,
// so a trickled body cannot hold a small handler for the upload window.
var UploadPaths = []string{
	"/api/cpmove/inspect",
	"/api/wpress/import",
}

// IsUploadPath reports whether path receives a large archive upload.
func IsUploadPath(path string) bool {
	for _, upload := range UploadPaths {
		if path == upload {
			return true
		}
	}
	return false
}

// IsServiceOperationPath reports whether path is a synchronous service action.
func IsServiceOperationPath(path string) bool {
	for _, prefix := range ServiceOperationPaths {
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

		// Uploads: large archive transfer with network variance. This is the
		// floor; the server raises it with UploadReadFor so the largest
		// configured archive (cPanel defaults to 40 GiB) fits at 50 Mbps.
		// Includes: cpmove backups, WPress archives, imports
		// The upload endpoint streams into an immutable staged object and is
		// separately concurrency-limited; this deadline is not the slowloris
		// defense for ordinary API requests.
		UploadRead: 60 * time.Minute,

		// Downloads: large file transfers
		// Includes: backup downloads, export streams
		DownloadWrite: 5 * time.Minute,

		// Service lifecycle actions: the 60-second helper timeout plus
		// waiting for the site lock.
		ServiceOperation: 3 * time.Minute,
	}
}

// Upload ceiling bounds. The idle-body guard fails a stalled upload after
// two minutes, so this ceiling only limits slow but active transfers.
const (
	uploadReadFloor       = 60 * time.Minute
	uploadReadCap         = 4 * time.Hour
	uploadMinBytesPerSec  = 50_000_000 / 8 // 50 Mbps sustained
	uploadReadGranularity = time.Minute
)

// UploadReadFor sizes the upload ceiling so the largest configured archive
// can arrive at a sustained 50 Mbps. It never drops below the 60-minute
// default and never exceeds four hours.
func UploadReadFor(maxUploadBytes int64) time.Duration {
	if maxUploadBytes <= 0 {
		return uploadReadFloor
	}
	seconds := (maxUploadBytes + uploadMinBytesPerSec - 1) / uploadMinBytesPerSec
	if seconds > int64(uploadReadCap/time.Second) {
		return uploadReadCap
	}
	needed := (time.Duration(seconds)*time.Second + uploadReadGranularity - 1).Truncate(uploadReadGranularity)
	return max(uploadReadFloor, needed)
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

// Rationale for timeout choices (values are in DefaultTimeouts):
//
// ORDINARY API (30s read, 2m write):
//   - Site, database, and job queries complete in about a second; the read
//     bound stops slow-body clients, the write bound covers response
//     generation on slow storage
//   - Work that takes minutes runs as a durable job and answers immediately
//
// STREAMS (no context deadline):
//   - Server-sent events stay open while a dashboard is visible
//   - A context deadline would cancel healthy streams and force reconnect,
//     re-authentication, and snapshot churn
//   - Per-write deadlines detect dead peers; the handler extends the
//     server-wide WriteTimeout per write and closes after StreamLifetime
//
// LONG-POLL (45s):
//   - Job and activity polling waits for an update, then the client retries
//
// UPLOADS (60m floor, sized by UploadReadFor, 4h cap):
//   - The ceiling fits the largest configured archive at a sustained 50 Mbps
//     (40 GiB takes about 115 minutes)
//   - The body streams into a staged object under the upload admission
//     limits; an idle-body guard fails a stalled upload after 2 minutes, so
//     the ceiling only bounds slow but active transfers
//
// DOWNLOADS (5m write):
//   - The server-wide WriteTimeout; no route currently streams a large
//     download, so no path selects this class
//
// SERVICE OPERATIONS (3m):
//   - Synchronous service actions: the helper's 60-second lifecycle bound
//     plus the site lock wait

// Middleware creates an HTTP middleware that enforces timeouts per route
func (tc TimeoutConfiguration) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Classify the request by path.
			var timeout time.Duration
			switch {
			case IsStreamPath(r.URL.Path):
				// Streams manage their own write deadlines and lifetime.
				next.ServeHTTP(w, r)
				return
			case IsUploadPath(r.URL.Path):
				// Uploads need long timeouts
				timeout = tc.UploadRead
			case IsServiceOperationPath(r.URL.Path):
				// The server-wide write timeout would end the response before
				// the work finishes; extend it for this request only.
				timeout = tc.ServiceOperation
				if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
					log.Printf("extend write deadline for %s: %v", r.URL.Path, err)
				}
			case startsWith(r.URL.Path, "/api/jobs/"):
				// Activity polling
				timeout = tc.LongPoll
			default:
				// Default API timeout
				timeout = tc.APIRead
			}

			// A context deadline does not interrupt a blocked r.Body read,
			// and the server-wide read timeout is sized for uploads. Bound
			// the network read for every other class too, so a slowly
			// trickled request body cannot hold a handler past its class.
			// Uploads keep the server read timeout plus their idle guard.
			if !IsUploadPath(r.URL.Path) {
				if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
					log.Printf("set read deadline for %s: %v", r.URL.Path, err)
				}
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
