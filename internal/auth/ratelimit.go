// Package auth contains authentication policy that is independent of the
// HTTP application assembly and platform helpers.
package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginAttempt struct {
	count int
	start time.Time
}

// Limiter bounds failed-login work per client and bounds the number of client
// keys retained in memory. It intentionally has no persistence or HTTP state.
type Limiter struct {
	mu      sync.Mutex
	attempt map[string]loginAttempt
	lastGC  time.Time
}

const maxKeys = 10_000

// NewLimiter creates a bounded login-attempt limiter.
func NewLimiter() *Limiter {
	return &Limiter{attempt: make(map[string]loginAttempt), lastGC: time.Now()}
}

// Allow admits one attempt when the client remains within the five-attempt,
// fifteen-minute window and the bounded key table has capacity.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) >= time.Minute {
		for candidate, attempt := range l.attempt {
			if now.Sub(attempt.start) >= 15*time.Minute {
				delete(l.attempt, candidate)
			}
		}
		l.lastGC = now
	}
	attempt := l.attempt[key]
	if attempt.start.IsZero() || now.Sub(attempt.start) >= 15*time.Minute {
		if len(l.attempt) >= maxKeys {
			return false
		}
		l.attempt[key] = loginAttempt{count: 1, start: now}
		return true
	}
	if attempt.count >= 5 {
		return false
	}
	attempt.count++
	l.attempt[key] = attempt
	return true
}

// Reset clears the attempt window for one client after successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.attempt, key)
	l.mu.Unlock()
}

// ClientIP extracts the peer address without trusting forwarded headers.
// Reverse-proxy deployments should pass the panel only a trusted transport
// boundary rather than allowing browsers to forge client identity headers.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}

// ClientIPWithTrustedProxy extracts the peer address, trusting X-Forwarded-For
// only when behind a known reverse proxy (indicated by the trustProxy flag).
// When trustProxy is false, behaves identically to ClientIP().
func ClientIPWithTrustedProxy(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// Extract the rightmost IP from X-Forwarded-For (original client is rightmost)
		// Format: Client, Proxy1, Proxy2, ...
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				if ip := strings.TrimSpace(ips[0]); ip != "" {
					return ip
				}
			}
		}
		// Fallback to X-Real-IP if X-Forwarded-For is not present
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	// Default behavior: use RemoteAddr (direct connection)
	return ClientIP(r)
}
