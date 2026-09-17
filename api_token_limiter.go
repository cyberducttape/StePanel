package main

import (
	"sync"
	"time"
)

// apiTokenRateLimiter bounds API token usage to prevent abuse of compromised tokens.
// It uses token bucket algorithm: tokens are issued at a constant rate, and requests
// consume tokens. When tokens are exhausted, requests are rate-limited.
type apiTokenRateLimiter struct {
	mu      sync.Mutex
	state   map[string]*tokenBucket
	lastGC  time.Time
	maxKeys int
}

type tokenBucket struct {
	tokens    float64   // Current tokens available
	lastRefill time.Time // When bucket was last refilled
}

const (
	tokensPerMinute = 600.0 // 10 requests per second per token
	refillInterval  = time.Second
	maxTokenKeys    = 10_000
)

// newAPITokenRateLimiter creates a bounded per-token rate limiter.
func newAPITokenRateLimiter() *apiTokenRateLimiter {
	return &apiTokenRateLimiter{
		state:   make(map[string]*tokenBucket),
		lastGC:  time.Now(),
		maxKeys: maxTokenKeys,
	}
}

// allow checks if the token can make a request.
// Returns true if request is allowed, false if rate limit exceeded.
func (l *apiTokenRateLimiter) allow(tokenID string) bool {
	if l == nil {
		return true // Rate limiting disabled
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Run garbage collection periodically to prevent unbounded memory growth.
	// This cleans up unused token entries to prevent memory exhaustion from
	// many different tokens making requests.
	if now.Sub(l.lastGC) >= time.Minute {
		l.garbageCollect()
		l.lastGC = now
	}

	bucket, exists := l.state[tokenID]
	if !exists {
		// New token: check if we have capacity for tracking it
		if len(l.state) >= l.maxKeys {
			// Too many tokens tracked, reject request to prevent DoS
			return false
		}
		bucket = &tokenBucket{
			tokens:    tokensPerMinute,
			lastRefill: now,
		}
		l.state[tokenID] = bucket
	}

	// Refill tokens based on time elapsed since last refill.
	// We calculate tokens as: tokensPerMinute * elapsed_seconds / 60
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	tokensToAdd := (tokensPerMinute / 60.0) * elapsed
	bucket.tokens = min(tokensPerMinute, bucket.tokens+tokensToAdd)
	bucket.lastRefill = now

	// Check if we have tokens available
	if bucket.tokens >= 1.0 {
		bucket.tokens -= 1.0
		return true
	}

	// No tokens available, rate limit this request
	return false
}

// garbageCollect removes old unused token entries to prevent unbounded memory growth.
// Tokens that haven't been used in the last 5 minutes are removed.
func (l *apiTokenRateLimiter) garbageCollect() {
	now := time.Now()
	unusedThreshold := 5 * time.Minute

	for tokenID, bucket := range l.state {
		if now.Sub(bucket.lastRefill) > unusedThreshold {
			delete(l.state, tokenID)
		}
	}
}

// reset clears rate limit state for a token (e.g., after password change).
func (l *apiTokenRateLimiter) reset(tokenID string) {
	if l == nil {
		return
	}

	l.mu.Lock()
	delete(l.state, tokenID)
	l.mu.Unlock()
}

// stats returns current limiter statistics for debugging.
// Returns (tracked_tokens, max_capacity).
func (l *apiTokenRateLimiter) stats() (int, int) {
	if l == nil {
		return 0, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.state), l.maxKeys
}
