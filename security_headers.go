package main

import (
	"net/http"
)

// securityHeadersMiddleware returns middleware that sets standard security headers.
// These headers protect against common browser-based attacks like MIME sniffing,
// clickjacking, and XSS. Production deployments must have these headers.
func securityHeadersMiddleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Prevent MIME type sniffing attacks (e.g., .exe served as .js)
			w.Header().Set("X-Content-Type-Options", "nosniff")

			// Prevent clickjacking: disallow framing entirely
			w.Header().Set("X-Frame-Options", "DENY")

			// Enable XSS filter in legacy browsers
			w.Header().Set("X-XSS-Protection", "1; mode=block")

			// Prevent Referer leakage to other origins
			w.Header().Set("Referrer-Policy", "no-referrer")

			// Disable sensitive browser features (least privilege)
			w.Header().Set("Permissions-Policy",
				"accelerometer=(), ambient-light-sensor=(), camera=(), "+
					"document-domain=(), encrypted-media=(), fullscreen=(), "+
					"geolocation=(), gyroscope=(), magnetometer=(), "+
					"microphone=(), midi=(), payment=(), usb=()")

			// Strict Transport Security (production only, if TLS configured)
			if cfg.Production && cfg.TLSCertFile != "" {
				// max-age=31536000 (1 year), include subdomains, preload list
				w.Header().Set("Strict-Transport-Security",
					"max-age=31536000; includeSubDomains; preload")
			}

			// Content Security Policy: strict defaults, no inline scripts
			// This prevents injected scripts from executing
			w.Header().Set("Content-Security-Policy",
				"default-src 'none'; "+
					"script-src 'self'; "+
					"style-src 'self'; "+
					"img-src 'self' data:; "+
					"font-src 'self'; "+
					"connect-src 'self'; "+
					"base-uri 'self'; "+
					"form-action 'self'; "+
					"frame-ancestors 'none'; "+
					"block-all-mixed-content; "+
					"upgrade-insecure-requests")

			// Block mixed content (HTTP resources on HTTPS page)
			w.Header().Set("X-Content-Security-Policy", "block-all-mixed-content")

			next.ServeHTTP(w, r)
		})
	}
}
